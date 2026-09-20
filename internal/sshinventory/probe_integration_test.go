package sshinventory

import (
	"context"
	"errors"
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
	banner := filepath.Join(root, "banner")
	if err := os.WriteFile(banner, []byte("Authenticated to 127.0.0.1 using publickey.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serverText := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nAuthorizedKeysFile %s\nPidFile %s\nBanner %s\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nUsePAM no\nPermitRootLogin prohibit-password\nLogLevel ERROR\n", port, filepath.Join(root, "host"), filepath.Join(root, "authorized_keys"), filepath.Join(root, "sshd.pid"), banner)
	// Newer sshd penalizes several expected negative fixture connections from
	// loopback. Probe support first so older OpenSSH builds still run this test.
	if err := os.WriteFile(serverConfig, []byte(serverText+"PerSourcePenalties no\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(sshd, "-t", "-f", serverConfig).Run(); err != nil {
		if err := os.WriteFile(serverConfig, []byte(serverText), 0o600); err != nil {
			t.Fatal(err)
		}
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
	fingerprintOutput, err := exec.Command(keygen, "-l", "-E", "sha256", "-f", filepath.Join(root, "host.pub")).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture host fingerprint: %v", err)
	}
	fingerprintFields := strings.Fields(string(fingerprintOutput))
	if len(fingerprintFields) < 2 {
		t.Fatal("fixture host fingerprint missing")
	}
	matched, err := MatchDirectED25519HostKey(clientConfig, "", selection, hostFields[0]+" "+hostFields[1], fingerprintFields[1])
	if err != nil || string(matched.KnownHostsLine()) != hostEntry {
		t.Fatalf("reviewed entry differs from OpenSSH fixture: %v", err)
	}
	wrongEntry := fmt.Sprintf("[127.0.0.1]:%d %s %s\n", port, clientFields[0], clientFields[1])
	unrelatedEntry := fmt.Sprintf("other.example.test %s %s\n", hostFields[0], hostFields[1])

	for _, tt := range []struct {
		name       string
		knownHosts string
		want       string
		timeout    bool
		failure    ProbeFailureKind
		revokeKey  bool
	}{
		{"known and authenticated", hostEntry, "Authenticated to", true, ProbeFailureUnknown, false},
		{"unknown", unrelatedEntry, "Host key verification failed", false, ProbeFailureHostKeyUnknown, false},
		{"changed", wrongEntry, "REMOTE HOST IDENTIFICATION HAS CHANGED", false, ProbeFailureHostKeyChanged, false},
		{"authentication rejected", hostEntry, "Permission denied (publickey)", false, ProbeFailureAuthRejected, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.revokeKey {
				if err := os.WriteFile(filepath.Join(root, "authorized_keys"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := PrepareDirectProbe(clientConfig, "", selection, []byte(tt.knownHosts))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = plan.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			clientLog := filepath.Join(root, "client-"+strings.ReplaceAll(tt.name, " ", "-")+".log")
			args := append([]string{"-v", "-E", clientLog}, plan.Arguments()...)
			cmd := exec.CommandContext(ctx, ssh, args...)
			cmd.Env = append(os.Environ(), "LC_ALL=C")
			output, runErr := cmd.CombinedOutput()
			logBytes, logErr := os.ReadFile(clientLog)
			if logErr != nil {
				t.Fatal(logErr)
			}
			if tt.revokeKey {
				if !strings.Contains(string(output), "Authenticated to") {
					t.Fatal("fixture did not send a forged authentication banner")
				}
				if strings.Contains(string(logBytes), "Authenticated to") {
					t.Fatal("server banner appeared as client authentication evidence")
				}
			}
			if !strings.Contains(string(logBytes), tt.want) {
				t.Fatalf("missing expected OpenSSH outcome %q: %v", tt.want, runErr)
			}
			if tt.timeout && ctx.Err() != context.DeadlineExceeded {
				t.Fatalf("authenticated -N session ended unexpectedly: %v", runErr)
			}
			if !tt.timeout && ctx.Err() != nil {
				t.Fatalf("host key rejection did not finish promptly: %v", runErr)
			}
			if !tt.timeout {
				exitErr, ok := runErr.(*exec.ExitError)
				if !ok {
					t.Fatalf("expected OpenSSH failure exit: %v", runErr)
				}
				if got := ClassifyOpenSSHFailure(exitErr.ExitCode(), string(logBytes), false); got != tt.failure {
					t.Fatalf("failure hint = %q, want %q", got, tt.failure)
				}
			}
			result, executeErr := ExecuteDirectProbe(context.Background(), ssh, plan)
			if executeErr != nil {
				t.Fatalf("bounded probe: %v", executeErr)
			}
			if result.ClientReportedAuthenticated != tt.timeout {
				t.Fatalf("client authentication result = %+v", result)
			}
			if !tt.timeout && result.Failure != tt.failure {
				t.Fatalf("bounded probe failure = %q, want %q", result.Failure, tt.failure)
			}
			stored, err := os.ReadFile(filepath.Join(plan.root, "known_hosts"))
			if err != nil || string(stored) != tt.knownHosts {
				t.Fatalf("diagnostic modified known_hosts: %v", err)
			}
		})
	}

	if err := os.WriteFile(filepath.Join(root, "authorized_keys"), clientPublic, 0o600); err != nil {
		t.Fatal(err)
	}
	confirmed, err := ConfirmDirectED25519HostKey(clientConfig, "", selection, matched, fingerprintFields[1])
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenPrivateDirectTrustStore(filepath.Join(root, "app-trust"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Enroll(clientConfig, "", selection, confirmed); err != nil {
		t.Fatal(err)
	}
	enrolledPlan, err := store.PrepareEnrolledDirectProbe(clientConfig, "", selection)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = enrolledPlan.Close() }()
	enrolledResult, err := ExecuteDirectProbe(context.Background(), ssh, enrolledPlan)
	if err != nil || !enrolledResult.ClientReportedAuthenticated {
		t.Fatalf("enrolled pin did not authenticate to fixture: %+v, %v", enrolledResult, err)
	}
	rotatedOutput, err := exec.Command(keygen, "-l", "-E", "sha256", "-f", filepath.Join(root, "client.pub")).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture replacement fingerprint: %v", err)
	}
	rotatedFields := strings.Fields(string(rotatedOutput))
	if len(rotatedFields) < 2 {
		t.Fatal("fixture replacement fingerprint missing")
	}
	rotatedEntry, err := MatchDirectED25519HostKey(clientConfig, "", selection, clientFields[0]+" "+clientFields[1], rotatedFields[1])
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := ConfirmDirectED25519HostKey(clientConfig, "", selection, rotatedEntry, rotatedFields[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Rotate(clientConfig, "", selection, fingerprintFields[1], rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteDirectProbe(context.Background(), ssh, enrolledPlan); !errors.Is(err, ErrChanged) {
		t.Fatalf("pre-rotation enrolled plan remained executable: %v", err)
	}
	rotatedPlan, err := store.PrepareEnrolledDirectProbe(clientConfig, "", selection)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rotatedPlan.Close() }()
	rotatedResult, err := ExecuteDirectProbe(context.Background(), ssh, rotatedPlan)
	if err != nil || rotatedResult.ClientReportedAuthenticated || rotatedResult.Failure != ProbeFailureHostKeyChanged {
		t.Fatalf("rotated pin silently trusted the old server: %+v, %v", rotatedResult, err)
	}
	noKeyConfig := filepath.Join(root, "no_key_config")
	writeFixture(t, noKeyConfig, fmt.Sprintf("Host selected\n HostName 127.0.0.1\n User %s\n Port %d\n", account.Username, port))
	noKeySelection, err := ResolveStatic(noKeyConfig, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	noKeyPlan, err := PrepareDirectProbe(noKeyConfig, "", noKeySelection, []byte(hostEntry))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = noKeyPlan.Close() }()
	noKeyResult, err := ExecuteDirectProbe(context.Background(), ssh, noKeyPlan)
	if err != nil || noKeyResult.ClientReportedAuthenticated || noKeyResult.Failure != ProbeFailureAuthRequired {
		t.Fatalf("no selected credential: %+v, %v", noKeyResult, err)
	}
	noneConfig := filepath.Join(root, "none_key_config")
	writeFixture(t, noneConfig, fmt.Sprintf("Host selected\n HostName 127.0.0.1\n User %s\n Port %d\n IdentityFile none\n", account.Username, port))
	noneSelection, err := ResolveStatic(noneConfig, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	nonePlan, err := PrepareDirectProbe(noneConfig, "", noneSelection, []byte(hostEntry))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = nonePlan.Close() }()
	noneResult, err := ExecuteDirectProbe(context.Background(), ssh, nonePlan)
	if err != nil || noneResult.ClientReportedAuthenticated || noneResult.Failure != ProbeFailureAuthRequired {
		t.Fatalf("explicitly disabled selected identity: %+v, %v", noneResult, err)
	}
	missingKeyConfig := filepath.Join(root, "missing_key_config")
	writeFixture(t, missingKeyConfig, fmt.Sprintf("Host selected\n HostName 127.0.0.1\n User %s\n Port %d\n IdentityFile %s\n", account.Username, port, filepath.Join(root, "absent-client-key")))
	missingKeySelection, err := ResolveStatic(missingKeyConfig, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	missingKeyPlan, err := PrepareDirectProbe(missingKeyConfig, "", missingKeySelection, []byte(hostEntry))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = missingKeyPlan.Close() }()
	missingKeyResult, err := ExecuteDirectProbe(context.Background(), ssh, missingKeyPlan)
	if err != nil || missingKeyResult.ClientReportedAuthenticated || missingKeyResult.Failure != ProbeFailureAuthRequired {
		t.Fatalf("missing selected credential: %+v, %v", missingKeyResult, err)
	}
	aliasConfig := filepath.Join(root, "alias_config")
	writeFixture(t, aliasConfig, fmt.Sprintf("Host selected\n HostName 127.0.0.1\n User %s\n Port %d\n HostKeyAlias reviewed-host\n IdentityFile %s\n", account.Username, port, filepath.Join(root, "client")))
	aliasSelection, err := ResolveStatic(aliasConfig, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	aliasEntry, err := MatchDirectED25519HostKey(aliasConfig, "", aliasSelection, hostFields[0]+" "+hostFields[1], fingerprintFields[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(aliasEntry.KnownHostsLine()), "reviewed-host ssh-ed25519 ") {
		t.Fatal("HostKeyAlias was not used as known-hosts identity")
	}
	aliasPlan, err := PrepareDirectProbe(aliasConfig, "", aliasSelection, aliasEntry.KnownHostsLine())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = aliasPlan.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	aliasLog := filepath.Join(root, "alias-client.log")
	aliasCommand := exec.CommandContext(ctx, ssh, append([]string{"-v", "-E", aliasLog}, aliasPlan.Arguments()...)...)
	aliasCommand.Env = append(os.Environ(), "LC_ALL=C")
	aliasCommand.Stdout = io.Discard
	aliasCommand.Stderr = io.Discard
	aliasErr := aliasCommand.Run()
	aliasLogBytes, err := os.ReadFile(aliasLog)
	if err != nil {
		t.Fatal(err)
	}
	aliasAuthenticated := strings.Contains(string(aliasLogBytes), "Authenticated to 127.0.0.1 (")
	if !aliasAuthenticated || ctx.Err() != context.DeadlineExceeded {
		code := -1
		if exitErr, ok := aliasErr.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		t.Fatalf("HostKeyAlias and nonstandard-port pin did not authenticate: client marker=%v deadline=%v failure=%s", aliasAuthenticated, ctx.Err(), ClassifyOpenSSHFailure(code, string(aliasLogBytes), false))
	}
	jumpConfig := filepath.Join(root, "jump_client_config")
	writeFixture(t, jumpConfig, fmt.Sprintf("Host selected\n HostName 127.0.0.1\n User %s\n Port %d\n HostKeyAlias target-pin\n ProxyJump jump\n IdentityFile %s\nHost jump\n HostName 127.0.0.1\n User %s\n Port %d\n HostKeyAlias jump-pin\n IdentityFile %s\n", account.Username, port, filepath.Join(root, "client"), account.Username, port, filepath.Join(root, "client")))
	jumpTarget, err := ResolveStatic(jumpConfig, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	jumpHost, err := ResolveStatic(jumpConfig, "", "jump")
	if err != nil {
		t.Fatal(err)
	}
	keyText := hostFields[0] + " " + hostFields[1] + "\n"
	jumpPins := []byte("target-pin " + keyText + "jump-pin " + keyText)
	jumpPlan, err := PrepareSingleJumpProbe(jumpConfig, "", jumpTarget, jumpHost, jumpPins)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = jumpPlan.Close() }()
	jumpResult, err := ExecuteDirectProbe(context.Background(), ssh, jumpPlan)
	if err != nil || !jumpResult.ClientReportedAuthenticated {
		t.Fatalf("single jump did not authenticate through two reviewed pins: %+v, %v", jumpResult, err)
	}
	for _, tt := range []struct{ name, pins string }{
		{"changed jump pin", "target-pin " + keyText + "jump-pin " + clientFields[0] + " " + clientFields[1] + "\n"},
		{"changed destination pin", "target-pin " + clientFields[0] + " " + clientFields[1] + "\n" + "jump-pin " + keyText},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := PrepareSingleJumpProbe(jumpConfig, "", jumpTarget, jumpHost, []byte(tt.pins))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = plan.Close() }()
			result, err := ExecuteDirectProbe(context.Background(), ssh, plan)
			if err != nil || result.ClientReportedAuthenticated {
				t.Fatalf("changed pin authenticated: %+v, %v", result, err)
			}
		})
	}
}
