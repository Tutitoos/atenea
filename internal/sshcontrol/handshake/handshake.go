// Package handshake negotiates the desktop/controller protocol after the native
// transport has authenticated both peers. The self-reported identifiers below
// provide compatibility and routing checks, never OS-user authentication.
package handshake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/google/uuid"

	"github.com/Tutitoos/atenea/internal/sshcontrol/wire"
)

const (
	// Major is the current incompatible protocol version.
	Major uint16 = 1
	// Minor is the highest backward-compatible version implemented here.
	Minor uint16 = 0
	// Desktop identifies the graphical process, which initiates the exchange.
	Desktop = "desktop"
	// Controller identifies the independent process, which answers the exchange.
	Controller = "controller"
	// Timeout bounds handshakes even when the caller has no earlier deadline.
	Timeout = 5 * time.Second
)

var (
	// ErrInvalid reports an invalid closed-schema handshake without input text.
	ErrInvalid = errors.New("ssh handshake: invalid message")
	// ErrVersion reports a different, unsupported major protocol version.
	ErrVersion = errors.New("ssh handshake: incompatible protocol")
	// ErrPeer reports an unexpected role or installation identity.
	ErrPeer = errors.New("ssh handshake: unexpected peer")
	// ErrTransport reports an unsuccessful exchange without transport payloads.
	ErrTransport = errors.New("ssh handshake: transport unavailable")
)

// Hello is a process description, not a credential. InstallationID is provided
// by the installed local package/controller, not accepted from a launch URL.
// SessionInstanceID identifies this process lifetime and changes on restart.
// No per-connection execution authorization is carried forward by this message.
type Hello struct {
	ProtocolMajor     uint16 `json:"protocol_major"`
	ProtocolMinor     uint16 `json:"protocol_minor"`
	Component         string `json:"component"`
	InstallationID    string `json:"installation_id"`
	SessionInstanceID string `json:"session_instance_id"`
}

// Result is connection-local negotiation metadata. It grants no permission to
// dispatch work, use credentials, open a different user's window or read history.
type Result struct {
	Peer          Hello
	ProtocolMinor uint16
}

// New constructs a hello with a fresh process-instance identity. Keep this value
// for the process lifetime; the OS transport must authenticate each connection.
func New(component, installationID string) (Hello, error) {
	instance, err := uuid.NewRandom()
	if err != nil {
		return Hello{}, ErrInvalid
	}
	hello := Hello{Major, Minor, component, installationID, instance.String()}
	if err := hello.validate(); err != nil {
		return Hello{}, err
	}
	return hello, nil
}

func canonicalID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}

func (h Hello) validate() error {
	if h.ProtocolMajor == 0 || (h.Component != Desktop && h.Component != Controller) || !canonicalID(h.InstallationID) || !canonicalID(h.SessionInstanceID) {
		return ErrInvalid
	}
	return nil
}

// Decode enforces required, exact-case field names before typed decoding. This
// prevents encoding/json's case-insensitive field matching from accepting two
// differently spelled keys that overwrite the same field. Duplicate JSON keys
// and invalid UTF-8 are rejected by the framing layer, including escaped keys.
func Decode(payload []byte) (Hello, error) {
	if err := wire.Validate(payload); err != nil {
		return Hello{}, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return Hello{}, ErrInvalid
	}
	names := [...]string{"protocol_major", "protocol_minor", "component", "installation_id", "session_instance_id"}
	if len(fields) != len(names) {
		return Hello{}, ErrInvalid
	}
	for _, name := range names {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Hello{}, ErrInvalid
		}
	}
	var hello Hello
	if err := json.Unmarshal(payload, &hello); err != nil {
		return Hello{}, ErrInvalid
	}
	if err := hello.validate(); err != nil {
		return Hello{}, err
	}
	return hello, nil
}

// Negotiate checks role, installed identity and version against trusted local
// configuration. A newer minor selects the common supported version; a newer
// major cannot silently downgrade. The local implementation supports only v1.0.
func Negotiate(local, remote Hello) (Result, error) {
	if err := local.validate(); err != nil {
		return Result{}, err
	}
	if err := remote.validate(); err != nil {
		return Result{}, err
	}
	if local.ProtocolMajor != Major || local.ProtocolMinor != Minor || remote.ProtocolMajor != Major {
		return Result{}, ErrVersion
	}
	if local.Component == remote.Component || local.InstallationID != remote.InstallationID || local.SessionInstanceID == remote.SessionInstanceID {
		return Result{}, ErrPeer
	}
	return Result{Peer: remote, ProtocolMinor: Minor}, nil
}

// Exchange performs one bounded handshake on an already OS-authenticated
// connection. It owns the connection exclusively until returning, clears its
// deadline on success, and closes on every failure. Cancellation interrupts a
// blocked read/write. The caller owns and serializes the connection on success.
func Exchange(ctx context.Context, conn net.Conn, local Hello) (result Result, err error) {
	if conn == nil {
		return Result{}, ErrTransport
	}
	canceled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(canceled) })
	defer func() {
		if !stop() {
			<-canceled
			err = ctx.Err()
		}
		if err != nil {
			result = Result{}
			_ = conn.Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := local.validate(); err != nil {
		return Result{}, err
	}
	if local.ProtocolMajor != Major || local.ProtocolMinor != Minor {
		return Result{}, ErrVersion
	}
	deadline := time.Now().Add(Timeout)
	if earlier, ok := ctx.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return Result{}, ErrTransport
	}
	payload, err := json.Marshal(local)
	if err != nil {
		return Result{}, ErrInvalid
	}
	if local.Component == Desktop {
		if err := wire.WriteFrame(conn, payload); err != nil {
			return Result{}, ErrTransport
		}
	}
	received, err := wire.ReadFrame(conn)
	if err != nil {
		return Result{}, ErrTransport
	}
	remote, err := Decode(received)
	if err != nil {
		return Result{}, err
	}
	result, err = Negotiate(local, remote)
	if err != nil {
		return Result{}, err
	}
	if local.Component == Controller {
		if err := wire.WriteFrame(conn, payload); err != nil {
			return Result{}, ErrTransport
		}
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return Result{}, ErrTransport
	}
	return result, nil
}
