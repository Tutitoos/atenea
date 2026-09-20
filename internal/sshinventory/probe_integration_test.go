package sshinventory

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This fixture uses a loopback-only sshd and disposable keys. It never reads
// the developer's SSH config, keys, known_hosts, or a remote device.
func TestDirectProbeControlledServerHostKeys(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("rootless sshd fixture requires a Unix account; Windows needs native acceptance")
	}
	ssh, sshErr := exec.LookPath("ssh")
	sshd, sshdErr := exec.LookPath("sshd")
	keygen, keygenErr := exec.LookPath("ssh-keygen")
	if sshErr != nil || sshdErr != nil || keygenErr != nil {
		t.Skip("OpenSSH client, server and key generator are required")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range []string{"host", "client"} {
		cmd := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(root, name))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generate fixture key: %v: %s", err, output)
		}
	}
	clientPublic, err := os.ReadFile(filepath.Join(root, "client.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "authorized_keys"), clientPublic, 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	serverConfig := filepath.Join(root, "sshd_config")
	serverText := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nAuthorizedKeysFile %s\nPidFile %s\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nUsePAM no\nPermitRootLogin prohibit-password\nLogLevel ERROR\n", port, filepath.Join(root, "host"), filepath.Join(root, "authorized_keys"), filepath.Join(root, "sshd.pid"))
	if err := os.WriteFile(serverConfig, []byte(serverText), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(sshd, "-t", "-f", serverConfig).CombinedOutput(); err != nil {
		t.Skipf("sshd fixture configuration unavailable: %v: %s", err, output)
	}
	server := exec.Command(sshd, "-D", "-e", "-f", serverConfig)
	server.Stderr = io.Discard
	if err := server.Start(); err != nil {
		t.Skipf("sshd fixture cannot start: %v", err)
	}
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ready := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		conn, dialErr := net.DialTimeout("tcp4", address, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		t.Skip("sshd fixture did not listen on loopback")
	}

	clientConfig := filepath.Join(root, "client_config")
	writeFixture(t, clientConfig, fmt.Sprintf("Host selected\n HostName 127.0.0.1\n User %s\n Port %d\n IdentityFile %s\n", account.Username, port, filepath.Join(root, "client")))
	selection, err := ResolveStatic(clientConfig, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	hostPublic, err := os.ReadFile(filepath.Join(root, "host.pub"))
	if err != nil {
		t.Fatal(err)
	}
	hostFields := strings.Fields(string(hostPublic))
	clientFields := strings.Fields(string(clientPublic))
	if len(hostFields) < 2 || len(clientFields) < 2 {
		t.Fatal("fixture public key malformed")
	}
	hostEntry := fmt.Sprintf("[127.0.0.1]:%d %s %s\n", port, hostFields[0], hostFields[1])
	wrongEntry := fmt.Sprintf("[127.0.0.1]:%d %s %s\n", port, clientFields[0], clientFields[1])
	unrelatedEntry := fmt.Sprintf("other.example.test %s %s\n", hostFields[0], hostFields[1])

	for _, tt := range []struct {
		name       string
		knownHosts string
		want       string
		timeout    bool
	}{
		{"known and authenticated", hostEntry, "Authenticated to", true},
		{"unknown", unrelatedEntry, "Host key verification failed", false},
		{"changed", wrongEntry, "REMOTE HOST IDENTIFICATION HAS CHANGED", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := PrepareDirectProbe(clientConfig, "", selection, []byte(tt.knownHosts))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = plan.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			args := append([]string{"-v"}, plan.Args...)
			cmd := exec.CommandContext(ctx, ssh, args...)
			cmd.Env = append(os.Environ(), "LC_ALL=C")
			output, runErr := cmd.CombinedOutput()
			if !strings.Contains(string(output), tt.want) {
				t.Fatalf("missing expected OpenSSH outcome %q: %v", tt.want, runErr)
			}
			if tt.timeout && ctx.Err() != context.DeadlineExceeded {
				t.Fatalf("authenticated -N session ended unexpectedly: %v", runErr)
			}
			if !tt.timeout && ctx.Err() != nil {
				t.Fatalf("host key rejection did not finish promptly: %v", runErr)
			}
			stored, err := os.ReadFile(filepath.Join(plan.root, "known_hosts"))
			if err != nil || string(stored) != tt.knownHosts {
				t.Fatalf("diagnostic modified known_hosts: %v", err)
			}
		})
	}
}
