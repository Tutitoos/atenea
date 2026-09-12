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
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
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
	// DefaultAcknowledgementTimeout bounds the interval from a delivered
	// revocation event to the complete textual event_ack message.
	DefaultAcknowledgementTimeout = 30 * time.Second

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

	// maxWireSequence is the largest sequence accepted by atenea.remote.v1.
	// Sequence arithmetic never wraps: when this value is reached, another
	// message cannot be sent or accepted on that owner.
	maxWireSequence    uint64 = 4294967295
	maxRevocationFence int64  = 9007199254740991
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

// ClosureIntentApplier is the durable acknowledgement boundary. The
// coordinator invokes it only after a canonical event_ack has been bound to
// the exact authenticated socket owner and delivered intent.
type ClosureIntentApplier interface {
	ApplyClosureIntent(context.Context, remotedevice.ApplyClosureIntentRequest) (remotedevice.ApplyClosureIntentResponse, error)
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
	clock                 func() time.Time
	validator             *remoteprotocol.Validator
	readTimeout           time.Duration
	writeTimeout          time.Duration
	ackTimeout            time.Duration
	actor                 remotedevice.Actor
	cleanupHook           SessionCleanupHook
	beforeAcceptanceWrite func()
	afterRevocationWrite  func()
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

// WithAcknowledgementTimeout bounds the interval allowed for an event_ack
// after a revoked event is delivered. A non-positive value is ignored.
func WithAcknowledgementTimeout(timeout time.Duration) Option {
	return func(config *options) {
		if timeout > 0 {
			config.ackTimeout = timeout
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
	conn             *websocket.Conn
	deviceID         string
	sessionID        string
	fence            int64
	closed           bool
	terminal         ownerTerminalCause
	registered       bool
	registry         *sessionOwners
	writeMu          sync.Mutex
	closeOne         sync.Once
	closePause       *ownerClosePause
	accepted         bool
	inboundSequence  uint64
	outboundSequence uint64
	pending          *ownerRevocationDelivery
	queued           *ownerRevocationRequest
	applying         bool
	applyDone        chan struct{}
}

type ownerRevocationDelivery struct {
	intent    remotedevice.ClosureIntent
	actor     remotedevice.Actor
	sequence  uint64
	delivered bool
}

type ownerRevocationRequest struct {
	intent remotedevice.ClosureIntent
	actor  remotedevice.Actor
}

type revocationDeliveryReservation uint8

const (
	revocationDeliveryRejected revocationDeliveryReservation = iota
	revocationDeliverySkippedApplying
	revocationDeliveryReserved
)

type revocationDeliveryStatus uint8

const (
	revocationDeliveryFailed revocationDeliveryStatus = iota
	revocationDeliverySkipped
	revocationDeliverySucceeded
)

type revocationDeliveryResult struct {
	status revocationDeliveryStatus
	reason string
}

type revocationOwnerRoute uint8

const (
	revocationOwnerRejected revocationOwnerRoute = iota
	revocationOwnerQueuedBeforeAcceptance
	revocationOwnerDeliver
)

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
	ownerTerminalRevocationPending
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

func (r *sessionOwners) markAccepted(owner *sessionOwner) (*ownerRevocationRequest, bool) {
	if owner == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.owners[owner.sessionID]; !ok || current != owner || owner.closed || owner.terminal != ownerTerminalNone {
		return nil, false
	}
	owner.accepted = true
	queued := owner.queued
	owner.queued = nil
	return queued, true
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
	if owner == nil || owner.closed || owner.terminal == ownerTerminalRevocation || owner.terminal == ownerTerminalRevocationPending || owner.applying || !owner.accepted || owner.deviceID != intent.DeviceID {
		return nil
	}
	// A session records the fence that admitted it; RevokeDevice increments the
	// device fence before creating the intent.  A still-reserved owner has no
	// fence yet, and is also safe to close because an admission racing after
	// revocation will fail in the store transaction.
	if owner.fence < 0 || owner.fence == maxRevocationFence || intent.Fence != owner.fence+1 {
		return nil
	}
	return owner
}

func (r *sessionOwners) routeRevocationIntent(intent remotedevice.ClosureIntent, actor remotedevice.Actor) (*sessionOwner, *ownerRevocationRequest, revocationOwnerRoute) {
	if intent.State != "pending" || actor.ID == "" || actor.PolicyID == "" {
		return nil, nil, revocationOwnerRejected
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	owner := r.owners[intent.SessionID]
	if owner == nil || owner.closed || owner.terminal == ownerTerminalRevocation || owner.terminal == ownerTerminalRevocationPending || owner.applying || owner.deviceID != intent.DeviceID || owner.fence < 0 || owner.fence == maxRevocationFence || intent.Fence != owner.fence+1 {
		return nil, nil, revocationOwnerRejected
	}
	if !owner.accepted {
		if owner.queued == nil {
			owner.queued = &ownerRevocationRequest{intent: intent, actor: actor}
		}
		return nil, owner.queued, revocationOwnerQueuedBeforeAcceptance
	}
	return owner, nil, revocationOwnerDeliver
}

func (r *sessionOwners) reserveRevocationDelivery(owner *sessionOwner, intent remotedevice.ClosureIntent, actor remotedevice.Actor) (*ownerRevocationDelivery, revocationDeliveryReservation) {
	if owner == nil || intent.State != "pending" || actor.ID == "" || actor.PolicyID == "" {
		return nil, revocationDeliveryRejected
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.owners[owner.sessionID]; !ok || current != owner || owner.closed || owner.terminal != ownerTerminalNone || !owner.accepted || owner.deviceID != intent.DeviceID || owner.sessionID != intent.SessionID {
		return nil, revocationDeliveryRejected
	}
	if owner.applying {
		return nil, revocationDeliverySkippedApplying
	}
	if owner.fence < 0 || owner.fence == maxRevocationFence || intent.Fence != owner.fence+1 {
		return nil, revocationDeliveryRejected
	}
	sequence, ok := nextWireSequence(owner.outboundSequence)
	if !ok {
		return nil, revocationDeliveryRejected
	}
	delivery := &ownerRevocationDelivery{intent: intent, actor: actor, sequence: sequence}
	owner.outboundSequence = sequence
	owner.pending = delivery
	return delivery, revocationDeliveryReserved
}

func (r *sessionOwners) failRevocationDelivery(owner *sessionOwner, delivery *ownerRevocationDelivery) {
	if owner == nil || delivery == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.owners[owner.sessionID]; ok && current == owner && owner.pending == delivery {
		owner.pending = nil
		owner.closed = true
		if owner.terminal == ownerTerminalNone {
			owner.terminal = ownerTerminalRevocationPending
		}
	}
}

func (r *sessionOwners) beginRevocationAcknowledgement(owner *sessionOwner, sequence uint64, eventID string, fence int64) (*ownerRevocationDelivery, bool) {
	if owner == nil {
		return nil, false
	}
	// Delivery holds writeMu from WriteMessage through the delivered-state
	// publication. Waiting here makes an immediate ACK observe the final write
	// outcome instead of racing the publication window.
	owner.writeMu.Lock()
	defer owner.writeMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.owners[owner.sessionID]; !ok || current != owner || owner.closed || owner.terminal != ownerTerminalNone || owner.pending == nil || !owner.pending.delivered || owner.applying || owner.pending.intent.ID != eventID || owner.pending.intent.Fence != fence {
		return nil, false
	}
	expected, ok := nextWireSequence(owner.inboundSequence)
	if !ok || sequence != expected {
		return nil, false
	}
	owner.inboundSequence = sequence
	owner.applying = true
	owner.applyDone = make(chan struct{})
	delivery := *owner.pending
	return &delivery, true
}

func (r *sessionOwners) markRevocationDelivered(owner *sessionOwner, delivery *ownerRevocationDelivery) bool {
	if owner == nil || delivery == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.owners[owner.sessionID]; !ok || current != owner || owner.pending != delivery || owner.closed || owner.terminal != ownerTerminalNone {
		return false
	}
	delivery.delivered = true
	return true
}

func (r *sessionOwners) finishRevocationAcknowledgement(owner *sessionOwner, delivery *ownerRevocationDelivery, applied bool) {
	if owner == nil {
		return
	}
	r.mu.Lock()
	if current, ok := r.owners[owner.sessionID]; ok && current == owner {
		if owner.pending != nil && delivery != nil && owner.pending.sequence == delivery.sequence && owner.pending.intent.ID == delivery.intent.ID {
			owner.pending = nil
		}
		owner.applying = false
		if applied {
			owner.closed = true
			owner.terminal = ownerTerminalRevocation
		} else {
			owner.closed = true
			owner.terminal = ownerTerminalRevocationPending
		}
		if owner.applyDone != nil {
			close(owner.applyDone)
			owner.applyDone = nil
		}
	}
	r.mu.Unlock()
}

func (r *sessionOwners) markRevocationFailure(owner *sessionOwner) {
	if owner == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.owners[owner.sessionID]; ok && current == owner {
		owner.pending = nil
		owner.closed = true
		if owner.terminal == ownerTerminalNone {
			owner.terminal = ownerTerminalRevocationPending
		}
	}
}

func (r *sessionOwners) hasPendingRevocation(owner *sessionOwner) bool {
	if owner == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return owner.pending != nil || owner.applying || owner.terminal == ownerTerminalRevocationPending
}

func (r *sessionOwners) waitForApply(owner *sessionOwner) {
	if owner == nil {
		return
	}
	r.mu.Lock()
	done := owner.applyDone
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}

func nextWireSequence(current uint64) (uint64, bool) {
	if current >= maxWireSequence {
		return 0, false
	}
	return current + 1, true
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
		if owner.applying {
			// An acknowledgement is in the durable apply critical section. The
			// close operation below waits for that section so shutdown cannot
			// close the socket before the store commit decides its terminal cause.
			owners = append(owners, owner)
			continue
		}
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
		owner.registry.waitForApply(owner)
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
		ackTimeout:   DefaultAcknowledgementTimeout,
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
	result, err := revoker.RevokeDevice(ctx, req)
	if err != nil {
		h.owners.lifecycleMu.Unlock()
		return remotedevice.RevokeDeviceResponse{}, err
	}
	type deliveryTarget struct {
		owner  *sessionOwner
		intent remotedevice.ClosureIntent
	}
	owners := make([]deliveryTarget, 0, len(result.Intents))
	for _, intent := range result.Intents {
		// RevokeDevice returns the durable history on an idempotent retry. Only
		// pending intents are delivery work; an applied historical intent must
		// never be emitted again.
		if intent.State != "pending" {
			continue
		}
		owner, _, route := h.owners.routeRevocationIntent(intent, result.Revocation.Actor)
		if route == revocationOwnerDeliver && owner != nil {
			owners = append(owners, deliveryTarget{owner: owner, intent: intent})
		}
	}
	type deliveryFailure struct {
		owner  *sessionOwner
		reason string
	}
	failures := make([]deliveryFailure, 0)
	for _, target := range owners {
		// The actor comes from the durable revocation returned by the store. It
		// is deliberately never substituted with the peer identity or the
		// handler's session-registration actor.
		outcome := h.deliverRevocation(target.owner, target.intent, result.Revocation.Actor)
		if outcome.status == revocationDeliveryFailed {
			// Defer owner.close until after lifecycleMu is released. An owner may
			// concurrently be in ApplyClosureIntent; owner.close joins that apply,
			// which itself needs lifecycleMu.
			failures = append(failures, deliveryFailure{owner: target.owner, reason: outcome.reason})
		}
	}
	h.owners.lifecycleMu.Unlock()
	for _, failure := range failures {
		// Delivery failure closes this owner with a non-revocation reason and
		// leaves the durable intent pending for a later retry.
		h.failRevocationOwner(failure.owner, failure.reason)
	}
	return result, nil
}

func (h *Handler) deliverRevocation(owner *sessionOwner, intent remotedevice.ClosureIntent, actor remotedevice.Actor) revocationDeliveryResult {
	if h == nil || owner == nil {
		return revocationDeliveryResult{status: revocationDeliveryFailed, reason: CloseReasonTransport}
	}
	delivery, reservation := h.owners.reserveRevocationDelivery(owner, intent, actor)
	if reservation == revocationDeliverySkippedApplying {
		return revocationDeliveryResult{status: revocationDeliverySkipped}
	}
	if reservation != revocationDeliveryReserved {
		return revocationDeliveryResult{status: revocationDeliveryFailed, reason: CloseReasonTransport}
	}
	event := revokedEventEnvelope{
		Protocol:    remoteprotocol.Subprotocol,
		Version:     remotedevice.ProtocolVersion,
		MessageType: "event",
		SessionID:   intent.SessionID,
		DeviceID:    intent.DeviceID,
		Sequence:    delivery.sequence,
		SentAt:      wireTimestamp(h.now()),
		Payload: revokedEventPayload{
			EventID:    intent.ID,
			EventType:  "revoked",
			OccurredAt: wireTimestamp(intent.CreatedAt),
			Data: revokedEventData{
				Kind:         "revoked",
				Scope:        "session",
				RevocationID: intent.RevocationID,
				Reason:       string(intent.Reason),
				EffectiveAt:  wireTimestamp(intent.CreatedAt),
				Fence:        intent.Fence,
			},
		},
	}
	encoded, err := json.Marshal(event)
	if err != nil || h.options.validator == nil || h.options.validator.Validate(encoded) != nil {
		h.owners.failRevocationDelivery(owner, delivery)
		return revocationDeliveryResult{status: revocationDeliveryFailed, reason: CloseReasonEnvelope}
	}
	if owner.conn == nil {
		h.owners.failRevocationDelivery(owner, delivery)
		return revocationDeliveryResult{status: revocationDeliveryFailed, reason: CloseReasonTransport}
	}
	owner.writeMu.Lock()
	if err := owner.conn.SetWriteDeadline(h.deadline(h.options.writeTimeout)); err != nil {
		owner.writeMu.Unlock()
		h.owners.failRevocationDelivery(owner, delivery)
		return revocationDeliveryResult{status: revocationDeliveryFailed, reason: CloseReasonTransport}
	}
	err = owner.conn.WriteMessage(websocket.TextMessage, encoded)
	if err == nil {
		if hook := h.options.afterRevocationWrite; hook != nil {
			// The hook is a deterministic test seam. It runs while writeMu is
			// held so an ACK cannot pass the write outcome publication.
			hook()
		}
		// Set the deadline on the underlying net.Conn rather than calling the
		// Gorilla read method from this writer goroutine. Gorilla permits one
		// reader, but does not permit concurrent read-method calls; net.Conn
		// deadline updates are safe while that reader is blocked.
		if underlying := owner.conn.UnderlyingConn(); underlying == nil {
			err = errors.New("remote coordinator: owner connection has no underlying transport")
		} else {
			err = underlying.SetReadDeadline(h.deadline(h.options.ackTimeout))
		}
	}
	if err == nil {
		// Publish the delivery while the writer lock is still held. A reader
		// may observe an ACK as soon as the frame reaches the peer; it must not
		// be eligible for ApplyClosureIntent until this successful write is
		// durably reflected in the owner state.
		if !h.owners.markRevocationDelivered(owner, delivery) {
			err = errors.New("remote coordinator: owner delivery was superseded")
		}
	}
	owner.writeMu.Unlock()
	if err != nil {
		h.owners.failRevocationDelivery(owner, delivery)
		return revocationDeliveryResult{status: revocationDeliveryFailed, reason: CloseReasonTransport}
	}
	return revocationDeliveryResult{status: revocationDeliverySucceeded}
}

func (h *Handler) deliverQueuedRevocation(owner *sessionOwner, request *ownerRevocationRequest) bool {
	if h == nil || owner == nil || request == nil {
		return true
	}
	// Acceptance is published while its write lock is held. Serialize the
	// queued flush with administrative replay, but release the lifecycle lock
	// before any failure path can wait for an in-flight apply.
	h.owners.lifecycleMu.Lock()
	if h.owners.hasPendingRevocation(owner) {
		h.owners.lifecycleMu.Unlock()
		return true
	}
	outcome := h.deliverRevocation(owner, request.intent, request.actor)
	h.owners.lifecycleMu.Unlock()
	if outcome.status == revocationDeliveryFailed {
		h.failRevocationOwner(owner, outcome.reason)
		return false
	}
	return true
}

func (h *Handler) failRevocationOwner(owner *sessionOwner, reason string) {
	if h == nil || owner == nil {
		return
	}
	h.owners.markRevocationFailure(owner)
	owner.close(h, reason)
}

func (h *Handler) rejectRevocationOwner(owner *sessionOwner, reason string) {
	if h == nil || owner == nil {
		return
	}
	if h.owners.hasPendingRevocation(owner) {
		h.failRevocationOwner(owner, reason)
		return
	}
	owner.close(h, reason)
}

func (h *Handler) applyRevocationAcknowledgement(ctx context.Context, owner *sessionOwner, delivery *ownerRevocationDelivery) bool {
	if h == nil || owner == nil || delivery == nil {
		return false
	}
	applier, ok := h.authenticator.(ClosureIntentApplier)
	if !ok {
		h.owners.finishRevocationAcknowledgement(owner, delivery, false)
		h.failRevocationOwner(owner, CloseReasonAuthentication)
		return false
	}
	requestID := acknowledgementRequestID(owner, delivery)
	// The lifecycle mutex is the coordinator's single serialization point for
	// admission, revocation, cleanup, and durable acknowledgement application.
	h.owners.lifecycleMu.Lock()
	if ctx == nil {
		ctx = context.Background()
	}
	response, err := applier.ApplyClosureIntent(ctx, remotedevice.ApplyClosureIntentRequest{
		EventID: delivery.intent.ID, DeviceID: delivery.intent.DeviceID, SessionID: delivery.intent.SessionID,
		Fence: delivery.intent.Fence, Actor: delivery.actor, RequestID: requestID,
	})
	h.owners.lifecycleMu.Unlock()
	if err != nil || response.Intent.ID != delivery.intent.ID || response.Intent.DeviceID != delivery.intent.DeviceID || response.Intent.SessionID != delivery.intent.SessionID || response.Intent.Fence != delivery.intent.Fence || response.Intent.State != "applied" {
		h.owners.finishRevocationAcknowledgement(owner, delivery, false)
		h.failRevocationOwner(owner, CloseReasonPolicy)
		return false
	}
	h.owners.finishRevocationAcknowledgement(owner, delivery, true)
	owner.close(h, CloseReasonDeviceRevoked)
	return true
}

func acknowledgementRequestID(owner *sessionOwner, delivery *ownerRevocationDelivery) string {
	if owner == nil || delivery == nil {
		return "ack-invalid"
	}
	hash := sha256.New()
	for _, value := range []string{"atenea.remote.ack", owner.deviceID, owner.sessionID, delivery.intent.ID, strconv.FormatInt(delivery.intent.Fence, 10), strconv.FormatUint(delivery.sequence, 10)} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("ack-%x", hash.Sum(nil))
}

func wireTimestamp(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	value = value.UTC()
	millis := value.UnixMilli()
	if millis < 0 || millis > 253402300799999 {
		return 0
	}
	return millis
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
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
	if hook := h.options.beforeAcceptanceWrite; hook != nil {
		// Deterministic test seam: the durable admission has completed, but the
		// acceptance frame has not been written or published yet.
		hook()
	}
	owner.writeMu.Lock()
	err = conn.SetWriteDeadline(h.deadline(h.options.writeTimeout))
	deadlineFailed := err != nil
	if err == nil {
		err = conn.WriteMessage(websocket.TextMessage, encoded)
	}
	var queued *ownerRevocationRequest
	accepted := false
	if err == nil {
		// Clear the admission deadline before publishing acceptance. Once
		// markAccepted succeeds, an administrative replay may install the
		// acknowledgement deadline concurrently; there must be no later reset
		// that could erase it.
		if err = conn.SetReadDeadline(time.Time{}); err == nil {
			queued, accepted = h.owners.markAccepted(owner)
		}
	}
	owner.writeMu.Unlock()
	if err != nil {
		// A failed accept never publishes an eligible owner. The durable
		// session is cleaned up by finishOwner after this transport close.
		if deadlineFailed {
			owner.close(h, CloseReasonTimeout)
		} else {
			owner.close(h, CloseReasonTransport)
		}
		return
	}
	if !accepted {
		owner.close(h, CloseReasonTransport)
		return
	}
	if registered {
		// A registered connection is owned until its peer closes it or a linked
		// pending revocation intent closes it.  No heartbeat or recovery loop is
		// introduced in this issue.
		if queued != nil && !h.deliverQueuedRevocation(owner, queued) {
			return
		}
		for {
			messageType, reader, readErr := conn.NextReader()
			if readErr != nil {
				if h.owners.hasPendingRevocation(owner) && isTimeoutError(readErr) {
					h.rejectRevocationOwner(owner, CloseReasonTimeout)
					return
				}
				h.owners.markReadTermination(owner, readErr)
				return
			}
			// Before a revoked event is delivered, v1 has no post-negotiation
			// dispatch in this bounded slice. Do not drain arbitrary frames: an
			// incomplete fragment must be rejected immediately. Once an intent is
			// pending, the bounded body read below is the acknowledgement parser.
			if !h.owners.hasPendingRevocation(owner) {
				if messageType == websocket.TextMessage || messageType == websocket.BinaryMessage {
					owner.close(h, CloseReasonPolicy)
					return
				}
				owner.close(h, CloseReasonEnvelope)
				return
			}
			if messageType == websocket.BinaryMessage {
				// The message type is known from the frame header. Reject before
				// consuming an attacker-controlled binary body.
				h.rejectRevocationOwner(owner, CloseReasonBinary)
				return
			}
			raw, bodyErr := readControlMessage(reader)
			if bodyErr != nil {
				reason := CloseReasonEnvelope
				if isTimeoutError(bodyErr) {
					reason = CloseReasonTimeout
				}
				h.rejectRevocationOwner(owner, reason)
				return
			}
			if err := conn.SetReadDeadline(time.Time{}); err != nil {
				h.rejectRevocationOwner(owner, CloseReasonTransport)
				return
			}
			if len(raw) > remoteprotocol.MaxControlMessageSize {
				h.rejectRevocationOwner(owner, CloseReasonOversize)
				return
			}
			if messageType != websocket.TextMessage || !utf8.Valid(raw) {
				if messageType == websocket.TextMessage && !utf8.Valid(raw) {
					h.rejectRevocationOwner(owner, CloseReasonInvalidUTF8)
					return
				}
				h.rejectRevocationOwner(owner, CloseReasonEnvelope)
				return
			}
			if h.options.validator == nil || h.options.validator.Validate(raw) != nil {
				h.rejectRevocationOwner(owner, CloseReasonEnvelope)
				return
			}
			ack, ok := decodeEventAck(raw)
			if !ok || ack.Protocol != remoteprotocol.Subprotocol || ack.Version != remotedevice.ProtocolVersion || ack.MessageType != "event_ack" || ack.SessionID != owner.sessionID || ack.DeviceID != owner.deviceID || ack.Payload.Kind != "event_ack" || ack.Payload.EventType != "revoked" {
				h.rejectRevocationOwner(owner, CloseReasonPolicy)
				return
			}
			delivery, ok := h.owners.beginRevocationAcknowledgement(owner, ack.Sequence, ack.Payload.EventID, ack.Payload.Fence)
			if !ok {
				h.rejectRevocationOwner(owner, CloseReasonPolicy)
				return
			}
			if !h.applyRevocationAcknowledgement(r.Context(), owner, delivery) {
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
	// A revoked event that was delivered but not durably applied must not be
	// converted into a normal session.closed audit when the peer disconnects.
	// The intent remains pending for a later owner; successful application uses
	// ownerTerminalRevocation and also skips this cleanup.
	shouldHook := registered && cause != ownerTerminalRevocation && cause != ownerTerminalRevocationPending && !h.owners.hasPendingRevocation(owner)
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

type revokedEventEnvelope struct {
	Protocol    string              `json:"protocol"`
	Version     string              `json:"version"`
	MessageType string              `json:"message_type"`
	SessionID   string              `json:"session_id"`
	DeviceID    string              `json:"device_id"`
	Sequence    uint64              `json:"sequence"`
	SentAt      int64               `json:"sent_at"`
	Payload     revokedEventPayload `json:"payload"`
}

type revokedEventPayload struct {
	EventID    string           `json:"event_id"`
	EventType  string           `json:"event_type"`
	OccurredAt int64            `json:"occurred_at"`
	Data       revokedEventData `json:"data"`
}

type revokedEventData struct {
	Kind         string `json:"kind"`
	Scope        string `json:"scope"`
	RevocationID string `json:"revocation_id"`
	Reason       string `json:"reason"`
	EffectiveAt  int64  `json:"effective_at"`
	Fence        int64  `json:"fence"`
}

type eventAckEnvelope struct {
	Protocol    string          `json:"protocol"`
	Version     string          `json:"version"`
	MessageType string          `json:"message_type"`
	SessionID   string          `json:"session_id"`
	DeviceID    string          `json:"device_id"`
	Sequence    uint64          `json:"sequence"`
	SentAt      int64           `json:"sent_at"`
	Payload     eventAckPayload `json:"payload"`
}

type eventAckPayload struct {
	Kind           string `json:"kind"`
	EventType      string `json:"event_type"`
	EventID        string `json:"event_id"`
	Fence          int64  `json:"fence"`
	AcknowledgedAt int64  `json:"acknowledged_at"`
}

func readControlMessage(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("remote coordinator: nil message reader")
	}
	return io.ReadAll(io.LimitReader(reader, remoteprotocol.MaxControlMessageSize+1))
}

func decodeEventAck(raw []byte) (eventAckEnvelope, bool) {
	var ack eventAckEnvelope
	if json.Unmarshal(raw, &ack) != nil {
		return eventAckEnvelope{}, false
	}
	return ack, true
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
