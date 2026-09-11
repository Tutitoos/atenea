// Package remotecoordinator contains the coordinator-side transport boundary
// for the atenea.remote.v1 walking skeleton.
//
// This package implements the bounded authenticated negotiation and the
// session-owner/revocation boundary. It does not dispatch capabilities, run
// a heartbeat, or bind a production listener.
//
// Application-level rejections made after the WebSocket handshake use the
// package's closed reason/code taxonomy. RFC 6455 framing violations are a
// transport boundary owned by Gorilla WebSocket v1.5.3: its parser rejects
// them before ServeHTTP can call closeConnection, with code 1002 and a
// structural diagnostic that is not part of this package's reason contract.
package remotecoordinator

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/Tutitoos/atenea/internal/remotedevice"
	"github.com/Tutitoos/atenea/pkg/remoteprotocol"
)

const (
	// ConnectPath is the only path implemented by the walking skeleton.
	ConnectPath = "/remote/v1/connect"

	// DefaultReadTimeout bounds an upgraded connection that never sends a first
	// message. It can be replaced in tests.
	DefaultReadTimeout = 10 * time.Second
	// DefaultWriteTimeout bounds acceptance and close writes.
	DefaultWriteTimeout = 5 * time.Second

	// HeartbeatIntervalMillis is the fixed planned interval in the skeleton.
	HeartbeatIntervalMillis = 15000
	// LivenessDeadlineMillis is the fixed planned deadline in the skeleton.
	LivenessDeadlineMillis = 45000

	// CloseReasonTransport is a sanitized transport rejection reason.
	CloseReasonTransport = "transport_rejected"
	// CloseReasonAuthentication is a sanitized authentication rejection reason.
	CloseReasonAuthentication = "authentication_denied"
	// CloseReasonEnvelope is a sanitized envelope rejection reason.
	CloseReasonEnvelope = "invalid_envelope"
	// CloseReasonBinary is a sanitized unsupported binary-message reason.
	CloseReasonBinary = "binary_not_supported"
	// CloseReasonInvalidUTF8 is a sanitized text-encoding rejection reason.
	CloseReasonInvalidUTF8 = "invalid_utf8"
	// CloseReasonOversize is a sanitized message-size rejection reason.
	CloseReasonOversize = "message_too_large"
	// CloseReasonPolicy is a sanitized policy rejection reason.
	CloseReasonPolicy = "policy_denied"
	// CloseReasonAccepted is a sanitized successful negotiation reason.
	CloseReasonAccepted = "negotiation_complete"
	// CloseReasonTimeout is a sanitized timeout reason.
	CloseReasonTimeout = "timeout"
	// CloseReasonDeviceRevoked is the only revocation close reason.  It is
	// intentionally mapped to the RFC 6455 policy-violation code (1008).
	CloseReasonDeviceRevoked = "device_revoked"
)

var (
	// ErrNilAuthenticator and ErrNilPeerGate identify a programmer error while
	// constructing the boundary. ServeHTTP still fails closed if either
	// dependency is nil.
	ErrNilAuthenticator = errors.New("remote coordinator: nil authenticator")
	// ErrNilPeerGate identifies a missing peer gate dependency.
	ErrNilPeerGate = errors.New("remote coordinator: nil peer gate")
)

// Authenticator is intentionally source-compatible with
// remotedevice.Store.Authenticate. Implementations must authenticate the
// supplied device id together with the certificate identity material.
type Authenticator interface {
	Authenticate(context.Context, remotedevice.AuthenticateRequest) (remotedevice.Device, error)
}

// SessionRegistrar is implemented by the durable registry.  It combines
// authentication and admission so a certificate renewal or device
// revocation cannot interleave between those decisions.
type SessionRegistrar interface {
	AuthenticateAndRegisterSession(context.Context, remotedevice.AuthenticateAndRegisterSessionRequest) (remotedevice.Session, error)
}

// SessionCloser is the durable cleanup half of session ownership.  It is
// deliberately separate from SessionRegistrar so a failed transport can be
// tested with a small fake without implying that revocation was acknowledged.
type SessionCloser interface {
	CloseSession(context.Context, remotedevice.CloseSessionRequest) (remotedevice.Session, error)
}

// DeviceRevoker is the administrative boundary used by RevokeDevice.  The
// handler only routes the resulting durable pending intents; it never writes
// closure.applied or session.closed as a transport acknowledgement.
type DeviceRevoker interface {
	RevokeDevice(context.Context, remotedevice.RevokeDeviceRequest) (remotedevice.RevokeDeviceResponse, error)
}

// PeerGate authorizes the trusted network peer represented by net/http's
// RemoteAddr. Forwarded headers and URL data are never passed to this hook.
type PeerGate interface {
	Allow(context.Context, string) bool
}

// PeerGateFunc adapts a function to PeerGate.
type PeerGateFunc func(context.Context, string) bool

// Allow implements PeerGate.
func (f PeerGateFunc) Allow(ctx context.Context, remoteAddr string) bool {
	return f != nil && f(ctx, remoteAddr)
}

type options struct {
	clock        func() time.Time
	validator    *remoteprotocol.Validator
	readTimeout  time.Duration
	writeTimeout time.Duration
	actor        remotedevice.Actor
	cleanupHook  SessionCleanupHook
}

// Option configures only testable, bounded dependencies of Handler.
type Option func(*options)

// SessionCleanupHook observes the result of the durable non-revocation
// cleanup transaction. It is intentionally an observation hook; it cannot
// acknowledge or mutate a revocation intent.
type SessionCleanupHook func(remotedevice.Session, error)

// WithClock supplies the clock used for the outbound envelope timestamp. A
// nil clock is ignored and the real UTC clock is retained.
func WithClock(clock func() time.Time) Option {
	return func(config *options) {
		if clock != nil {
			config.clock = clock
		}
	}
}

// WithValidator supplies a compiled protocol validator. A nil validator is
// ignored; the canonical embedded validator remains the default.
func WithValidator(validator *remoteprotocol.Validator) Option {
	return func(config *options) {
		if validator != nil {
			config.validator = validator
		}
	}
}

// WithReadTimeout bounds the time spent waiting for the first message.
// Non-positive values are ignored.
func WithReadTimeout(timeout time.Duration) Option {
	return func(config *options) {
		if timeout > 0 {
			config.readTimeout = timeout
		}
	}
}

// WithWriteTimeout bounds the time spent writing the acceptance and close
// frames. Non-positive values are ignored.
func WithWriteTimeout(timeout time.Duration) Option {
	return func(config *options) {
		if timeout > 0 {
			config.writeTimeout = timeout
		}
	}
}

// WithSessionActor configures the coordinator-owned actor recorded for
// durable session registration.  A valid default keeps the walking skeleton
// usable while allowing deployments to provide their explicit policy actor.
func WithSessionActor(actor remotedevice.Actor) Option {
	return func(config *options) {
		if actor.ID != "" && actor.PolicyID != "" {
			config.actor = actor
		}
	}
}

// WithSessionCleanupHook installs a bounded lifecycle observation hook for
// tests and embedding callers. A nil hook is ignored.
func WithSessionCleanupHook(hook SessionCleanupHook) Option {
	return func(config *options) {
		if hook != nil {
			config.cleanupHook = hook
		}
	}
}

type sessionOwner struct {
	conn       *websocket.Conn
	deviceID   string
	sessionID  string
	fence      int64
	closed     bool
	terminal   ownerTerminalCause
	registered bool
	registry   *sessionOwners
	writeMu    sync.Mutex
	closeOne   sync.Once
	closePause *ownerClosePause
}

// ownerClosePause is a deterministic test seam for the close/join boundary.
// It is nil for production owners and cannot alter terminal-cause selection.
type ownerClosePause struct {
	reached chan struct{}
	release <-chan struct{}
	once    sync.Once
}

type ownerTerminalCause uint8

const (
	ownerTerminalNone ownerTerminalCause = iota
	ownerTerminalNormal
	ownerTerminalProtocol
	ownerTerminalShutdown
	ownerTerminalRevocation
)

type sessionOwners struct {
	mu          sync.Mutex
	lifecycleMu sync.Mutex
	closed      bool
	closeDone   chan struct{}
	owners      map[string]*sessionOwner
}

func newSessionOwners() *sessionOwners {
	return &sessionOwners{closeDone: make(chan struct{}), owners: make(map[string]*sessionOwner)}
}

func (r *sessionOwners) reserve(sessionID, deviceID string, conn *websocket.Conn) (*sessionOwner, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, false
	}
	if _, exists := r.owners[sessionID]; exists {
		return nil, false
	}
	owner := &sessionOwner{conn: conn, deviceID: deviceID, sessionID: sessionID, registry: r}
	r.owners[sessionID] = owner
	return owner, true
}

func (r *sessionOwners) activate(owner *sessionOwner, session remotedevice.Session) {
	if owner == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.owners[owner.sessionID]; !ok || current != owner {
		return
	}
	owner.deviceID = session.DeviceID
	owner.fence = session.Fence
	owner.registered = true
}

func (r *sessionOwners) isClosed(owner *sessionOwner) bool {
	if owner == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return owner.closed
}

func (r *sessionOwners) forIntent(intent remotedevice.ClosureIntent) *sessionOwner {
	r.mu.Lock()
	defer r.mu.Unlock()
	owner := r.owners[intent.SessionID]
	if owner == nil || owner.terminal == ownerTerminalRevocation || owner.deviceID != intent.DeviceID {
		return nil
	}
	// A session records the fence that admitted it; RevokeDevice increments the
	// device fence before creating the intent.  A still-reserved owner has no
	// fence yet, and is also safe to close because an admission racing after
	// revocation will fail in the store transaction.
	if owner.fence != 0 && intent.Fence != owner.fence+1 {
		return nil
	}
	owner.closed = true
	owner.terminal = ownerTerminalRevocation
	return owner
}

func (r *sessionOwners) forDevice(deviceID string, fence int64) []*sessionOwner {
	r.mu.Lock()
	defer r.mu.Unlock()
	owners := make([]*sessionOwner, 0)
	for _, owner := range r.owners {
		if owner.terminal == ownerTerminalRevocation || owner.deviceID != deviceID {
			continue
		}
		if owner.fence != 0 && owner.fence+1 != fence {
			continue
		}
		owner.closed = true
		owner.terminal = ownerTerminalRevocation
		owners = append(owners, owner)
	}
	return owners
}

func (r *sessionOwners) closeAll(h *Handler, reason string) {
	r.mu.Lock()
	if r.closed {
		done := r.closeDone
		r.mu.Unlock()
		<-done
		return
	}
	r.closed = true
	owners := make([]*sessionOwner, 0, len(r.owners))
	for _, owner := range r.owners {
		if !owner.closed {
			owner.closed = true
			owner.terminal = ownerTerminalShutdown
		}
		// Join every owner still present, including one whose terminal cause was
		// already recorded but whose closeOne/writeMu operation has not completed.
		// owner.close preserves an existing cause (notably revocation).
		owners = append(owners, owner)
	}
	r.mu.Unlock()
	closeOwners(h, owners, reason)
	close(r.closeDone)
}

func closeOwners(h *Handler, owners []*sessionOwner, reason string) {
	var wg sync.WaitGroup
	wg.Add(len(owners))
	for _, owner := range owners {
		go func(owner *sessionOwner) {
			defer wg.Done()
			owner.close(h, reason)
		}(owner)
	}
	wg.Wait()
}

func (r *sessionOwners) finish(owner *sessionOwner) (session remotedevice.Session, registered bool, cause ownerTerminalCause) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.owners[owner.sessionID]; !ok || current != owner {
		return remotedevice.Session{}, false, ownerTerminalNone
	}
	delete(r.owners, owner.sessionID)
	if owner.terminal == ownerTerminalNone {
		owner.terminal = ownerTerminalProtocol
	}
	return remotedevice.Session{ID: owner.sessionID, DeviceID: owner.deviceID, Fence: owner.fence}, owner.registered, owner.terminal
}

func (owner *sessionOwner) close(h *Handler, reason string) {
	if owner == nil || h == nil {
		return
	}
	if owner.registry != nil {
		owner.registry.markClose(owner, reason)
	}
	owner.closeOne.Do(func() {
		if pause := owner.closePause; pause != nil {
			pause.once.Do(func() { close(pause.reached) })
			<-pause.release
		}
		owner.writeMu.Lock()
		defer owner.writeMu.Unlock()
		if owner.registry != nil {
			reason = owner.registry.closeReason(owner, reason)
		}
		h.closeConnection(owner.conn, reason)
	})
}

func (r *sessionOwners) closeReason(owner *sessionOwner, fallback string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if owner != nil && owner.terminal == ownerTerminalRevocation {
		return CloseReasonDeviceRevoked
	}
	return fallback
}

func (r *sessionOwners) markClose(owner *sessionOwner, reason string) {
	if owner == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.owners[owner.sessionID]; ok && current == owner {
		owner.closed = true
		cause := ownerTerminalProtocol
		switch reason {
		case CloseReasonTransport:
			cause = ownerTerminalShutdown
		case CloseReasonDeviceRevoked:
			cause = ownerTerminalRevocation
		}
		if owner.terminal == ownerTerminalNone || cause == ownerTerminalRevocation {
			owner.terminal = cause
		}
	}
}

func (r *sessionOwners) markReadTermination(owner *sessionOwner, err error) {
	if owner == nil {
		return
	}
	cause := ownerTerminalProtocol
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		switch closeErr.Code {
		case websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived:
			cause = ownerTerminalNormal
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.owners[owner.sessionID]; ok && current == owner && owner.terminal == ownerTerminalNone {
		owner.closed = true
		owner.terminal = cause
	}
}

// Handler is an http.Handler for exactly GET /remote/v1/connect. It is not a
// production listener and has no lifecycle goroutine of its own.
type Handler struct {
	authenticator Authenticator
	peerGate      PeerGate
	options       options
	upgrader      websocket.Upgrader
	owners        *sessionOwners
}

// NewHandler constructs the isolated WSS negotiation endpoint. The
// dependencies are deliberately explicit so tests and future integration
// layers can supply the registry and network policy without global state.
func NewHandler(authenticator Authenticator, peerGate PeerGate, opts ...Option) *Handler {
	config := options{
		clock:        func() time.Time { return time.Now().UTC() },
		validator:    remoteprotocol.New(),
		readTimeout:  DefaultReadTimeout,
		writeTimeout: DefaultWriteTimeout,
		actor:        remotedevice.Actor{ID: "coordinator", PolicyID: "remote"},
	}
	for _, option := range opts {
		if option != nil {
			option(&config)
		}
	}
	return &Handler{
		authenticator: authenticator,
		peerGate:      peerGate,
		options:       config,
		owners:        newSessionOwners(),
		upgrader: websocket.Upgrader{
			HandshakeTimeout: DefaultReadTimeout,
			// Compression is deliberately disabled for the first transport. The
			// bound is applied to the complete control message below.
			EnableCompression: false,
			Subprotocols:      []string{remoteprotocol.Subprotocol},
			Error: func(w http.ResponseWriter, _ *http.Request, status int, _ error) {
				writeHTTPError(w, status)
			},
			CheckOrigin: func(r *http.Request) bool {
				return !hasOrigin(r)
			},
		},
	}
}

// Close is terminal and idempotent. It closes every currently owned
// connection, prevents later reservations, and leaves durable cleanup to the
// corresponding ServeHTTP owner defer.
func (h *Handler) Close() error {
	if h != nil && h.owners != nil {
		h.owners.closeAll(h, CloseReasonTransport)
	}
	return nil
}

// RevokeDevice persists the revocation and then routes only the returned
// device/session/fence-linked pending intents to their live socket owners.
// The registry remains the source of truth for pending work; closing a
// socket is not treated as an acknowledgement.
func (h *Handler) RevokeDevice(ctx context.Context, req remotedevice.RevokeDeviceRequest) (remotedevice.RevokeDeviceResponse, error) {
	if h == nil || h.authenticator == nil {
		return remotedevice.RevokeDeviceResponse{}, ErrNilAuthenticator
	}
	revoker, ok := h.authenticator.(DeviceRevoker)
	if !ok {
		return remotedevice.RevokeDeviceResponse{}, errors.New("remote coordinator: authenticator does not support revocation")
	}
	// Serialize the durable revocation with admission and non-revocation
	// session cleanup. This gives the owner terminal cause a single linearization
	// point: either cleanup wins and revocation sees a closed session, or
	// revocation wins and cleanup must preserve the active/pending intent.
	h.owners.lifecycleMu.Lock()
	defer h.owners.lifecycleMu.Unlock()
	result, err := revoker.RevokeDevice(ctx, req)
	if err != nil {
		return remotedevice.RevokeDeviceResponse{}, err
	}
	owners := make([]*sessionOwner, 0, len(result.Intents))
	for _, intent := range result.Intents {
		if owner := h.owners.forIntent(intent); owner != nil {
			owners = append(owners, owner)
		}
	}
	deviceID := result.Device.ID
	if deviceID == "" {
		deviceID = req.DeviceID
	}
	owners = append(owners, h.owners.forDevice(deviceID, result.Device.Fence)...)
	closeOwners(h, owners, CloseReasonDeviceRevoked)
	return result, nil
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.authenticator == nil {
		writeHTTPError(w, http.StatusInternalServerError)
		return
	}
	if h.peerGate == nil {
		writeHTTPError(w, http.StatusInternalServerError)
		return
	}
	if r == nil || r.Method != http.MethodGet || r.URL == nil || r.URL.Path != ConnectPath {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	if r.TLS == nil || !verifiedClientCertificate(r.TLS) {
		writeHTTPError(w, http.StatusUnauthorized)
		return
	}
	if strings.TrimSpace(r.RemoteAddr) == "" || !h.peerGate.Allow(r.Context(), r.RemoteAddr) {
		writeHTTPError(w, http.StatusForbidden)
		return
	}
	if hasOrigin(r) || !hasExactSubprotocol(r) {
		writeHTTPError(w, http.StatusBadRequest)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// The upgrader has already emitted a generic HTTP response. No peer
		// detail is rendered or logged here.
		return
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(h.deadline(h.options.readTimeout)); err != nil {
		h.closeConnection(conn, CloseReasonTimeout)
		return
	}

	messageType, reader, err := conn.NextReader()
	if err != nil {
		reason := CloseReasonEnvelope
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			reason = CloseReasonTimeout
		}
		h.closeConnection(conn, reason)
		return
	}
	// Read at most one byte past the protocol limit. This keeps the memory
	// bound explicit while allowing this handler to send its own sanitized
	// reason instead of Gorilla's generic 1009 reason. LimitReader also covers
	// a fragmented data message as one logical first message.
	raw, err := io.ReadAll(io.LimitReader(reader, remoteprotocol.MaxControlMessageSize+1))
	if err != nil {
		reason := CloseReasonEnvelope
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			reason = CloseReasonTimeout
		}
		h.closeConnection(conn, reason)
		return
	}
	if messageType == websocket.BinaryMessage {
		h.closeConnection(conn, CloseReasonBinary)
		return
	}
	if len(raw) > remoteprotocol.MaxControlMessageSize {
		h.closeConnection(conn, CloseReasonOversize)
		return
	}
	if messageType != websocket.TextMessage || !utf8.Valid(raw) {
		if messageType == websocket.TextMessage && !utf8.Valid(raw) {
			h.closeConnection(conn, CloseReasonInvalidUTF8)
			return
		}
		h.closeConnection(conn, CloseReasonEnvelope)
		return
	}
	if err := h.options.validator.Validate(raw); err != nil {
		h.closeConnection(conn, CloseReasonEnvelope)
		return
	}
	offer, ok := decodeOffer(raw)
	if !ok || !validOffer(offer) {
		h.closeConnection(conn, CloseReasonPolicy)
		return
	}
	owner, reserved := h.owners.reserve(offer.Envelope.SessionID, offer.Envelope.DeviceID, conn)
	if !reserved {
		h.closeConnection(conn, CloseReasonPolicy)
		return
	}
	defer h.finishOwner(owner)

	identity, ok := verifiedIdentity(r.TLS)
	if !ok {
		owner.close(h, CloseReasonAuthentication)
		return
	}
	registrar, supportsSessions := h.authenticator.(SessionRegistrar)
	_, supportsCleanup := h.authenticator.(SessionCloser)
	if !supportsSessions || !supportsCleanup {
		// Authentication without the matching durable admission primitive would
		// authorize a socket before a session commit. Fail closed rather than
		// silently falling back to the legacy read-only authenticator. Cleanup is
		// required too: a transport termination must not orphan an active row.
		owner.close(h, CloseReasonAuthentication)
		return
	}
	h.owners.lifecycleMu.Lock()
	session, err := registrar.AuthenticateAndRegisterSession(r.Context(), remotedevice.AuthenticateAndRegisterSessionRequest{
		SessionID:       offer.Envelope.SessionID,
		DeviceID:        offer.Envelope.DeviceID,
		Fingerprint:     identity.fingerprint,
		PublicKeyDigest: append([]byte(nil), identity.spkiDigest...),
		Actor:           h.options.actor,
		RequestID:       offer.Envelope.SessionID,
		Platform:        offer.Platform,
		Architecture:    offer.Architecture,
	})
	registered := err == nil
	matched := registered
	if registered {
		h.owners.activate(owner, session)
	}
	h.owners.lifecycleMu.Unlock()
	wipeBytes(identity.spkiDigest)
	if !matched {
		owner.close(h, CloseReasonAuthentication)
		return
	}
	if h.owners.isClosed(owner) {
		// Handler.Close or a concurrent terminal owner event won while the
		// admission transaction was in flight. The deferred cleanup below will
		// close the durable session unless the event was a revocation.
		owner.close(h, CloseReasonTransport)
		return
	}

	accept := acceptanceEnvelope{Protocol: remoteprotocol.Subprotocol, Version: remotedevice.ProtocolVersion, MessageType: "negotiation", SessionID: offer.Envelope.SessionID, DeviceID: offer.Envelope.DeviceID, Sequence: 0, SentAt: h.now().UnixMilli(), Payload: acceptancePayload{Phase: "accept", Role: "coordinator", AcceptedVersion: remotedevice.ProtocolVersion, AcceptedMode: "background", Grants: map[string]any{}, HeartbeatIntervalMillis: HeartbeatIntervalMillis, LivenessDeadlineMillis: LivenessDeadlineMillis}}
	encoded, err := json.Marshal(accept)
	if err != nil || h.options.validator.Validate(encoded) != nil {
		owner.close(h, CloseReasonEnvelope)
		return
	}
	if err := conn.SetWriteDeadline(h.deadline(h.options.writeTimeout)); err != nil {
		owner.close(h, CloseReasonTimeout)
		return
	}
	owner.writeMu.Lock()
	err = conn.WriteMessage(websocket.TextMessage, encoded)
	owner.writeMu.Unlock()
	if err != nil {
		return
	}
	if registered {
		// A registered connection is owned until its peer closes it or a linked
		// pending revocation intent closes it.  No heartbeat or recovery loop is
		// introduced in this issue.
		if err := conn.SetReadDeadline(time.Time{}); err != nil {
			return
		}
		for {
			messageType, _, readErr := conn.NextReader()
			if readErr != nil {
				h.owners.markReadTermination(owner, readErr)
				return
			}
			// v1 has no post-negotiation dispatch in this bounded slice. Do not
			// read the returned payload: a fragmented data frame without a
			// continuation must be rejected immediately instead of waiting for an
			// unbounded reader completion.
			if messageType == websocket.TextMessage || messageType == websocket.BinaryMessage {
				owner.close(h, CloseReasonPolicy)
				return
			}
		}
	}
	h.closeConnection(conn, CloseReasonAccepted)
}

func (h *Handler) finishOwner(owner *sessionOwner) {
	if h == nil || owner == nil || h.owners == nil {
		return
	}
	h.owners.lifecycleMu.Lock()
	session, registered, cause := h.owners.finish(owner)
	closer, hasCloser := h.authenticator.(SessionCloser)
	hook := h.options.cleanupHook
	var cleanupErr error
	shouldHook := registered && cause != ownerTerminalRevocation
	if shouldHook && hasCloser {
		cleanupReason := remotedevice.SessionCloseReasonProtocolError
		if cause == ownerTerminalNormal || cause == ownerTerminalShutdown {
			cleanupReason = remotedevice.SessionCloseReasonAdministrator
		}
		_, cleanupErr = closer.CloseSession(context.Background(), remotedevice.CloseSessionRequest{
			SessionID: session.ID,
			DeviceID:  session.DeviceID,
			Actor:     h.options.actor,
			Reason:    cleanupReason,
			RequestID: session.ID,
		})
	}
	h.owners.lifecycleMu.Unlock()
	if shouldHook && hook != nil {
		hook(session, cleanupErr)
	}
}

func (h *Handler) now() time.Time {
	now := h.options.clock()
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

func (h *Handler) deadline(timeout time.Duration) time.Time {
	return time.Now().Add(timeout)
}

func (h *Handler) closeConnection(conn *websocket.Conn, reason string) {
	if conn == nil {
		return
	}
	deadline := h.deadline(h.options.writeTimeout)
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(closeCode(reason), reason), deadline)
	_ = conn.Close()
}

// closeCode maps only application-level reasons. Gorilla's RFC 6455 parser
// can emit CloseProtocolError (1002) before the handler sees an invalid frame;
// that transport diagnostic is intentionally outside this taxonomy.
func closeCode(reason string) int {
	switch reason {
	case CloseReasonAccepted:
		return websocket.CloseNormalClosure
	case CloseReasonBinary:
		return websocket.CloseUnsupportedData
	case CloseReasonInvalidUTF8:
		return websocket.CloseInvalidFramePayloadData
	case CloseReasonOversize:
		return websocket.CloseMessageTooBig
	default:
		return websocket.ClosePolicyViolation
	}
}

func writeHTTPError(w http.ResponseWriter, status int) {
	http.Error(w, "remote connection rejected", status)
}

func hasOrigin(r *http.Request) bool {
	if r == nil {
		return false
	}
	for key := range r.Header {
		if strings.EqualFold(key, "Origin") {
			return true
		}
	}
	return false
}

func hasExactSubprotocol(r *http.Request) bool {
	if r == nil {
		return false
	}
	var tokens []string
	for _, value := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				return false
			}
			tokens = append(tokens, token)
		}
	}
	return len(tokens) == 1 && tokens[0] == remoteprotocol.Subprotocol
}

func verifiedClientCertificate(state *tls.ConnectionState) bool {
	return state != nil && len(state.VerifiedChains) > 0 && len(state.VerifiedChains[0]) > 0 && state.VerifiedChains[0][0] != nil
}

type certificateIdentity struct {
	fingerprint string
	spkiDigest  []byte
}

func verifiedIdentity(state *tls.ConnectionState) (certificateIdentity, bool) {
	if !verifiedClientCertificate(state) {
		return certificateIdentity{}, false
	}
	leaf := state.VerifiedChains[0][0]
	if len(leaf.Raw) == 0 || len(leaf.RawSubjectPublicKeyInfo) == 0 {
		return certificateIdentity{}, false
	}
	fingerprint := sha256.Sum256(leaf.Raw)
	spki := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return certificateIdentity{fingerprint: hex.EncodeToString(fingerprint[:]), spkiDigest: append([]byte(nil), spki[:]...)}, true
}

type wireEnvelope struct {
	Protocol    string          `json:"protocol"`
	Version     string          `json:"version"`
	MessageType string          `json:"message_type"`
	SessionID   string          `json:"session_id"`
	DeviceID    string          `json:"device_id"`
	Sequence    uint64          `json:"sequence"`
	Payload     json.RawMessage `json:"payload"`
}

type negotiationOffer struct {
	Envelope         wireEnvelope
	Phase            string                    `json:"phase"`
	Role             string                    `json:"role"`
	AgentVersion     string                    `json:"agent_version"`
	Platform         remotedevice.Platform     `json:"platform"`
	Architecture     remotedevice.Architecture `json:"architecture"`
	SupportedVersion []string                  `json:"supported_versions"`
	Modes            []string                  `json:"modes"`
	Capabilities     []string                  `json:"capabilities"`
}

func decodeOffer(raw []byte) (negotiationOffer, bool) {
	var envelope wireEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return negotiationOffer{}, false
	}
	var payload struct {
		Phase            string                    `json:"phase"`
		Role             string                    `json:"role"`
		AgentVersion     string                    `json:"agent_version"`
		Platform         remotedevice.Platform     `json:"platform"`
		Architecture     remotedevice.Architecture `json:"architecture"`
		SupportedVersion []string                  `json:"supported_versions"`
		Modes            []string                  `json:"modes"`
		Capabilities     []string                  `json:"capabilities"`
	}
	if json.Unmarshal(envelope.Payload, &payload) != nil {
		return negotiationOffer{}, false
	}
	return negotiationOffer{Envelope: envelope, Phase: payload.Phase, Role: payload.Role, AgentVersion: payload.AgentVersion, Platform: payload.Platform, Architecture: payload.Architecture, SupportedVersion: payload.SupportedVersion, Modes: payload.Modes, Capabilities: payload.Capabilities}, true
}

func validOffer(offer negotiationOffer) bool {
	if offer.Envelope.Protocol != remoteprotocol.Subprotocol || offer.Envelope.Version != remotedevice.ProtocolVersion || offer.Envelope.MessageType != "negotiation" || offer.Envelope.Sequence != 0 || offer.Phase != "offer" || offer.Role != "agent" || offer.AgentVersion == "" || offer.Envelope.SessionID == "" || offer.Envelope.DeviceID == "" {
		return false
	}
	if len(offer.SupportedVersion) != 1 || offer.SupportedVersion[0] != remotedevice.ProtocolVersion || len(offer.Capabilities) != 0 {
		return false
	}
	for _, mode := range offer.Modes {
		if mode == "background" {
			return true
		}
	}
	return false
}

func wipeBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type acceptanceEnvelope struct {
	Protocol    string            `json:"protocol"`
	Version     string            `json:"version"`
	MessageType string            `json:"message_type"`
	SessionID   string            `json:"session_id"`
	DeviceID    string            `json:"device_id"`
	Sequence    uint64            `json:"sequence"`
	SentAt      int64             `json:"sent_at"`
	Payload     acceptancePayload `json:"payload"`
}

type acceptancePayload struct {
	Phase                   string         `json:"phase"`
	Role                    string         `json:"role"`
	AcceptedVersion         string         `json:"accepted_version"`
	AcceptedMode            string         `json:"accepted_mode"`
	Grants                  map[string]any `json:"grants"`
	HeartbeatIntervalMillis int            `json:"heartbeat_interval_ms"`
	LivenessDeadlineMillis  int            `json:"liveness_deadline_ms"`
}
