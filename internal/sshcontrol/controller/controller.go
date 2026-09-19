// Package controller implements the fixture-only local desktop service.
// Native peer checks belong to localipc; no message here authorizes SSH work.
package controller

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Tutitoos/atenea/internal/sshcontrol/handshake"
	"github.com/Tutitoos/atenea/internal/sshcontrol/localipc"
	"github.com/Tutitoos/atenea/internal/sshcontrol/wire"
)

const requestTimeout = 5 * time.Second

// ErrProtocol reports a disallowed fixture operation or malformed response.
var ErrProtocol = errors.New("ssh controller: invalid fixture request")

// Status contains only controller lifecycle data. It contains no host or secret.
type Status struct {
	State    string `json:"state"`
	Protocol string `json:"protocol"`
}

type request struct {
	Operation string `json:"operation"`
}

// Root selects the dedicated state directory for the current OS account.
func Root() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "atenea", "ssh-desktop"), nil
}

// InstallationID creates a stable, user-local identifier. The native transport
// authenticates the peer; this identifier is only a compatibility check.
func InstallationID(root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", localipc.ErrPrivateRoot
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", localipc.ErrPrivateRoot
	}
	if err := prepareRoot(root); err != nil {
		return "", err
	}
	path := filepath.Join(root, "installation.id")
	if data, err := os.ReadFile(path); err == nil {
		id := string(data)
		parsed, parseErr := uuid.Parse(id)
		if parseErr != nil || parsed == uuid.Nil || parsed.String() != id {
			return "", ErrProtocol
		}
		return id, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	id, err := uuid.NewRandomFromReader(rand.Reader)
	if err != nil {
		return "", err
	}
	// Write a private temporary file before publishing the final path. Linking
	// within the same directory is atomic and cannot replace another winner.
	f, err := os.CreateTemp(root, ".installation-*.tmp")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.WriteString(id.String()); err != nil {
		_ = f.Close()
		return "", err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Link(f.Name(), path); errors.Is(err, os.ErrExist) {
		return InstallationID(root)
	} else if err != nil {
		return "", err
	}
	return id.String(), nil
}

// Serve owns the authenticated endpoint until cancellation or an explicit stop.
// Closing the graphical window does not close this listener.
func Serve(ctx context.Context, root, installationID string) error {
	hello, err := handshake.New(handshake.Controller, installationID)
	if err != nil {
		return err
	}
	listener, err := localipc.Listen(root)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(serveCtx, func() { _ = listener.Close() })
	defer stop()
	var workers sync.WaitGroup
	defer workers.Wait()
	slots := make(chan struct{}, 8)
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if serveCtx.Err() != nil {
				return nil
			}
			return acceptErr
		}
		select {
		case slots <- struct{}{}:
			workers.Add(1)
			go func() { defer workers.Done(); defer func() { <-slots }(); serveConn(serveCtx, conn, hello, cancel) }()
		default:
			_ = conn.Close()
		}
	}
}

func serveConn(ctx context.Context, conn net.Conn, hello handshake.Hello, stop context.CancelFunc) {
	defer func() { _ = conn.Close() }()
	negotiation, err := handshake.Exchange(ctx, conn, hello)
	if err != nil || negotiation.Peer.Component != handshake.Desktop {
		return
	}
	_ = conn.SetDeadline(time.Now().Add(requestTimeout))
	payload, err := wire.ReadFrame(conn)
	if err != nil {
		return
	}
	req, err := decodeRequest(payload)
	if err != nil {
		return
	}
	switch req.Operation {
	case "status":
		response, _ := json.Marshal(Status{State: "running", Protocol: "1.0"})
		_ = wire.WriteFrame(conn, response)
	case "stop":
		response, _ := json.Marshal(Status{State: "stopping", Protocol: "1.0"})
		if wire.WriteFrame(conn, response) == nil {
			stop()
		}
	}
}

func decodeRequest(payload []byte) (request, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil || len(fields) != 1 || fields["operation"] == nil {
		return request{}, ErrProtocol
	}
	var operation string
	if json.Unmarshal(fields["operation"], &operation) != nil || (operation != "status" && operation != "stop") {
		return request{}, ErrProtocol
	}
	return request{Operation: operation}, nil
}

// Call makes one bounded fixture request over a freshly authenticated channel.
func Call(ctx context.Context, root, installationID, operation string) (Status, error) {
	if operation != "status" && operation != "stop" {
		return Status{}, ErrProtocol
	}
	hello, err := handshake.New(handshake.Desktop, installationID)
	if err != nil {
		return Status{}, err
	}
	conn, err := localipc.Dial(root, requestTimeout)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := handshake.Exchange(ctx, conn, hello); err != nil {
		return Status{}, err
	}
	_ = conn.SetDeadline(time.Now().Add(requestTimeout))
	payload, _ := json.Marshal(request{Operation: operation})
	if err := wire.WriteFrame(conn, payload); err != nil {
		return Status{}, err
	}
	response, err := wire.ReadFrame(conn)
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := json.Unmarshal(response, &status); err != nil {
		return Status{}, ErrProtocol
	}
	if status.Protocol != "1.0" || (status.State != "running" && status.State != "stopping") {
		return Status{}, fmt.Errorf("%w: invalid status", ErrProtocol)
	}
	return status, nil
}
