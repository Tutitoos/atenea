package sshinventory

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareDirectProbe(t *testing.T) {
	selection := Selection{
		HostName: "example.test", User: "person@example.test", Port: 2222,
		HostKeyAlias: "reviewed.example.test", Snapshot: "fixture-snapshot",
		IdentityFiles: []string{"/nonexistent/fixture-key"},
	}
	knownHosts := []byte("reviewed.example.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFixture\n")
	plan, err := PrepareDirectProbe(selection, knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	root := plan.root
	t.Cleanup(func() { _ = plan.Close() })
	if plan.Snapshot != selection.Snapshot {
		t.Fatalf("snapshot = %q", plan.Snapshot)
	}
	if got, err := os.ReadFile(filepath.Join(root, "known_hosts")); err != nil || string(got) != string(knownHosts) {
		t.Fatalf("known_hosts = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{"config", "known_hosts"} {
			info, err := os.Stat(filepath.Join(root, name))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("%s permissions: %v, %v", name, info, err)
			}
		}
	}
	args := strings.Join(plan.Args, "\x00")
	for _, required := range []string{"-N", "-T", "-n", "BatchMode=yes", "ClearAllForwardings=yes", "ControlMaster=no", "ControlPath=none", "ForwardAgent=no", "IdentitiesOnly=yes", "IdentityAgent=none", "StrictHostKeyChecking=yes", "UpdateHostKeys=no", "HostKeyAlias=reviewed.example.test"} {
		if !strings.Contains(args, required) {
			t.Errorf("missing %q", required)
		}
	}
	for _, forbidden := range []string{"-L", "-R", "-D", "ProxyCommand=", "ProxyJump="} {
		if strings.Contains(args, forbidden) {
			t.Errorf("unexpected %q", forbidden)
		}
	}
	if plan.Args[len(plan.Args)-1] != selection.HostName {
		t.Fatal("destination is not final argument")
	}
	if err := plan.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("temporary root remains: %v", err)
	}
}

func TestPrepareDirectProbeRejectsUnresolvedRoutes(t *testing.T) {
	base := Selection{HostName: "example.test", User: "person", Port: 22, Snapshot: "fixture"}
	tests := []struct {
		name string
		edit func(*Selection)
		want error
	}{
		{"proxy jump", func(s *Selection) { s.ProxyJump = "jump" }, ErrProbeUnsupported},
		{"proxy command", func(s *Selection) { s.ProxyCommand = "helper" }, ErrProbeUnsupported},
		{"host option", func(s *Selection) { s.HostName = "-oProxyCommand=evil" }, ErrUnresolved},
		{"host account", func(s *Selection) { s.HostName = "person@other.test" }, ErrUnresolved},
		{"host whitespace", func(s *Selection) { s.HostName = "host name" }, ErrUnresolved},
		{"key alias option", func(s *Selection) { s.HostKeyAlias = "-oStrictHostKeyChecking=no" }, ErrUnresolved},
		{"identity none", func(s *Selection) { s.IdentityFiles = []string{"none"} }, ErrUnresolved},
		{"empty snapshot", func(s *Selection) { s.Snapshot = "" }, ErrUnresolved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection := base
			tt.edit(&selection)
			plan, err := PrepareDirectProbe(selection, []byte("fixture known hosts"))
			if !errors.Is(err, tt.want) || plan != nil {
				t.Fatalf("plan = %v, err = %v, want %v", plan, err, tt.want)
			}
		})
	}
	if plan, err := PrepareDirectProbe(base, nil); !errors.Is(err, ErrUnresolved) || plan != nil {
		t.Fatalf("empty trust snapshot: plan = %v, err = %v", plan, err)
	}
}

func TestDirectProbeOpenSSHEffectiveOptions(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client unavailable")
	}
	plan, err := PrepareDirectProbe(Selection{
		HostName: "example.test", User: "person", Port: 2222, Snapshot: "fixture",
	}, []byte("example.test ssh-ed25519 fixture\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plan.Close() }()
	args := append([]string{"-G"}, plan.Args...)
	output, err := exec.Command(ssh, args...).CombinedOutput() // -G prints config; no connection.
	if err != nil {
		t.Fatalf("ssh -G: %v: %s", err, output)
	}
	settings := string(output)
	for _, required := range []string{"batchmode yes", "clearallforwardings yes", "controlmaster false", "forwardagent no", "stricthostkeychecking true", "requesttty false", "sessiontype none"} {
		if !strings.Contains(settings, required) {
			t.Errorf("effective OpenSSH config lacks %q", required)
		}
	}
	for _, line := range strings.Split(settings, "\n") {
		if strings.HasPrefix(line, "controlpath ") || strings.HasPrefix(line, "proxycommand ") || strings.HasPrefix(line, "proxyjump ") {
			t.Errorf("effective config includes a shared or proxy route: %s", line)
		}
	}
}
