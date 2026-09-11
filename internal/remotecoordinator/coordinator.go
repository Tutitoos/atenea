// Package remotecoordinator contains the coordinator-side transport boundary
// for the atenea.remote.v1 walking skeleton.
//
// This package intentionally implements one authenticated negotiation and
// nothing beyond it. It does not register a durable session, dispatch
// capabilities, run a heartbeat, or bind a production listener.
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
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
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
}

// Option configures only testable, bounded dependencies of Handler.
type Option func(*options)

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

// Handler is an http.Handler for exactly GET /remote/v1/connect. It is not a
// production listener and has no lifecycle goroutine of its own.
type Handler struct {
	authenticator Authenticator
	peerGate      PeerGate
	options       options
	upgrader      websocket.Upgrader
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

	identity, ok := verifiedIdentity(r.TLS)
	if !ok {
		h.closeConnection(conn, CloseReasonAuthentication)
		return
	}
	device, err := h.authenticator.Authenticate(r.Context(), remotedevice.AuthenticateRequest{
		DeviceID:        offer.Envelope.DeviceID,
		Fingerprint:     identity.fingerprint,
		PublicKeyDigest: append([]byte(nil), identity.spkiDigest...),
	})
	matched := err == nil && deviceMatchesOffer(device, offer, identity)
	wipeBytes(identity.spkiDigest)
	if !matched {
		h.closeConnection(conn, CloseReasonAuthentication)
		return
	}

	accept := acceptanceEnvelope{Protocol: remoteprotocol.Subprotocol, Version: remotedevice.ProtocolVersion, MessageType: "negotiation", SessionID: offer.Envelope.SessionID, DeviceID: offer.Envelope.DeviceID, Sequence: 0, SentAt: h.now().UnixMilli(), Payload: acceptancePayload{Phase: "accept", Role: "coordinator", AcceptedVersion: remotedevice.ProtocolVersion, AcceptedMode: "background", Grants: map[string]any{}, HeartbeatIntervalMillis: HeartbeatIntervalMillis, LivenessDeadlineMillis: LivenessDeadlineMillis}}
	encoded, err := json.Marshal(accept)
	if err != nil || h.options.validator.Validate(encoded) != nil {
		h.closeConnection(conn, CloseReasonEnvelope)
		return
	}
	if err := conn.SetWriteDeadline(h.deadline(h.options.writeTimeout)); err != nil {
		h.closeConnection(conn, CloseReasonTimeout)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, encoded); err != nil {
		return
	}
	h.closeConnection(conn, CloseReasonAccepted)
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

func deviceMatchesOffer(device remotedevice.Device, offer negotiationOffer, identity certificateIdentity) bool {
	if device.ID != offer.Envelope.DeviceID || device.Platform != offer.Platform || device.Architecture != offer.Architecture || device.State != "active" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(device.Certificate.Fingerprint), []byte(identity.fingerprint)) != 1 || subtle.ConstantTimeCompare(device.Certificate.PublicKeyDigest, identity.spkiDigest) != 1 {
		return false
	}
	return true
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
