package handshake

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/sshcontrol/wire"
)

const installation = "c29b3c96-5b91-4a31-a6a4-c8df574acabb"

func hello(t *testing.T, role string) Hello {
	t.Helper()
	h, err := New(role, installation)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestExactSchema(t *testing.T) {
	original := hello(t, Desktop)
	payload, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(payload)
	if err != nil || decoded != original {
		t.Fatalf("valid hello: %v", err)
	}
	s := string(payload)
	inputs := []string{
		strings.Replace(s, `"protocol_major":1`, `"protocol_major":1,"PROTOCOL_MAJOR":2`, 1),
		strings.Replace(s, `"protocol_major"`, `"PROTOCOL_MAJOR"`, 1),
		strings.Replace(s, `"protocol_minor":0,`, "", 1),
		strings.Replace(s, `"protocol_minor":0`, `"protocol_minor":null`, 1),
		strings.Replace(s, `"protocol_minor":0`, `"protocol_minor":65536`, 1),
		strings.Replace(s, `"protocol_minor":0`, `"protocol_minor":1e999`, 1),
		strings.Replace(s, `"protocol_major":1`, `"protocol_major":1,"protocol_major":2`, 1),
		strings.Replace(s, installation, strings.ToUpper(installation), 1),
		strings.Replace(s, installation, "00000000-0000-0000-0000-000000000000", 1),
		strings.Replace(s, `"desktop"`, `"DO_NOT_LOG"`, 1),
	}
	for i, input := range inputs {
		got, err := Decode([]byte(input))
		if !errors.Is(err, ErrInvalid) || got != (Hello{}) {
			t.Fatalf("case %d: accepted malformed hello", i)
		}
		if strings.Contains(err.Error(), "DO_NOT_LOG") {
			t.Fatal("error leaked input")
		}
	}
}

func TestNegotiation(t *testing.T) {
	local, remote := hello(t, Desktop), hello(t, Controller)
	remote.ProtocolMinor = 42
	result, err := Negotiate(local, remote)
	if err != nil || result.ProtocolMinor != 0 {
		t.Fatalf("minor negotiation: %v", err)
	}
	cases := []struct {
		name   string
		modify func(*Hello)
		want   error
	}{
		{"major", func(h *Hello) { h.ProtocolMajor = 2 }, ErrVersion},
		{"role", func(h *Hello) { h.Component = Desktop }, ErrPeer},
		{"installation", func(h *Hello) { h.InstallationID = "8221ecc6-e0de-42fd-b7eb-8edb5e5a0710" }, ErrPeer},
		{"reflection", func(h *Hello) { h.SessionInstanceID = local.SessionInstanceID }, ErrPeer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := remote
			tc.modify(&changed)
			got, err := Negotiate(local, changed)
			if !errors.Is(err, tc.want) || got != (Result{}) {
				t.Fatalf("negotiation: %v", err)
			}
		})
	}
}

func TestExchangePreservesConnectionForNextFrame(t *testing.T) {
	desktop, controller := net.Pipe()
	defer func() { _ = desktop.Close(); _ = controller.Close() }()
	a, b := hello(t, Desktop), hello(t, Controller)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result, err := Exchange(ctx, controller, b)
		if err == nil && result.Peer != a {
			err = ErrPeer
		}
		if err == nil {
			err = wire.WriteFrame(controller, []byte(`{"fixture":"ready"}`))
		}
		done <- err
	}()
	result, err := Exchange(ctx, desktop, a)
	if err != nil || result.Peer != b {
		t.Fatalf("desktop: %v", err)
	}
	if err := desktop.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	payload, err := wire.ReadFrame(desktop)
	if err != nil || string(payload) != `{"fixture":"ready"}` {
		t.Fatalf("next frame: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type observedConn struct {
	net.Conn
	reading chan struct{}
	once    sync.Once
}

func (c *observedConn) Read(p []byte) (int, error) {
	c.once.Do(func() { close(c.reading) })
	return c.Conn.Read(p)
}

func TestCancellationClosesBlockedExchange(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	observed := &observedConn{Conn: a, reading: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	local := hello(t, Controller)
	done := make(chan error, 1)
	go func() { _, err := Exchange(ctx, observed, local); done <- err }()
	select {
	case <-observed.reading:
	case <-time.After(2 * time.Second):
		t.Fatal("exchange did not reach read")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not interrupt read")
	}
	if _, err := b.Write([]byte{1}); err == nil {
		t.Fatal("canceled exchange kept connection open")
	}
}

func TestIncompatiblePeerCannotContinueConnection(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	local, remote := hello(t, Controller), hello(t, Desktop)
	remote.ProtocolMajor = 2
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := Exchange(ctx, a, local); done <- err }()
	payload, err := json.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.WriteFrame(b, payload); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrVersion) {
		t.Fatalf("version: %v", err)
	}
	if err := wire.WriteFrame(b, []byte(`{"operation":"dispatch"}`)); err == nil {
		t.Fatal("failed handshake left connection usable")
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"protocol_major":1,"protocol_minor":0,"component":"desktop","installation_id":"c29b3c96-5b91-4a31-a6a4-c8df574acabb","session_instance_id":"8221ecc6-e0de-42fd-b7eb-8edb5e5a0710"}`))
	f.Add([]byte(`{"PROTOCOL_MAJOR":1}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		h, err := Decode(input)
		if err != nil {
			if h != (Hello{}) {
				t.Fatal("partial hello returned")
			}
			return
		}
		encoded, err := json.Marshal(h)
		if err != nil {
			t.Fatal(err)
		}
		roundtrip, err := Decode(encoded)
		if err != nil || roundtrip != h {
			t.Fatalf("roundtrip: %v", err)
		}
	})
}
