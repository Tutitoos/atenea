package sshinventory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareDirectProbe(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host selected\n HostName example.test\n User person@example.test\n Port 2222\n HostKeyAlias reviewed.example.test\n IdentityFile /nonexistent/fixture-key\n")
	selection, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := []byte("reviewed.example.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFixture\n")
	plan, err := PrepareDirectProbe(config, "", selection, knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	root := plan.root
	t.Cleanup(func() { _ = plan.Close() })
	if plan.Snapshot() != selection.Snapshot {
		t.Fatalf("snapshot = %q", plan.Snapshot())
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
	planArgs := plan.Arguments()
	args := strings.Join(planArgs, "\x00")
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
	if planArgs[len(planArgs)-1] != selection.HostName {
		t.Fatal("destination is not final argument")
	}
	planArgs[len(planArgs)-1] = "wrong.example.test"
	if plan.Arguments()[len(plan.Arguments())-1] != selection.HostName {
		t.Fatal("caller modified plan arguments")
	}
	if err := plan.Revalidate(); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, config, "Host selected\n HostName changed.example.test\n User person@example.test\n Port 2222\n HostKeyAlias reviewed.example.test\n IdentityFile /nonexistent/fixture-key\n")
	if err := plan.Revalidate(); !errors.Is(err, ErrChanged) {
		t.Fatalf("stale plan revalidated: %v", err)
	}
	if _, err := ExecuteDirectProbe(context.Background(), "/nonexistent/ssh", plan); !errors.Is(err, ErrChanged) {
		t.Fatalf("stale plan was executed: %v", err)
	}
	if err := plan.Close(); err != nil {
		t.Fatal(err)
	}
	if len(plan.Arguments()) != 0 || plan.Snapshot() != "" || !errors.Is(plan.Revalidate(), ErrUnresolved) {
		t.Fatal("closed plan remains usable")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("temporary root remains: %v", err)
	}
}

func TestPrepareDirectProbeRejectsUnresolvedRoutes(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host selected\n HostName example.test\n User person\n")
	base, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*Selection)
		want error
	}{
		{"host option", func(s *Selection) { s.HostName = "-oProxyCommand=evil" }, ErrChanged},
		{"host account", func(s *Selection) { s.HostName = "person@other.test" }, ErrChanged},
		{"host whitespace", func(s *Selection) { s.HostName = "host name" }, ErrChanged},
		{"key alias option", func(s *Selection) { s.HostKeyAlias = "-oStrictHostKeyChecking=no" }, ErrChanged},
		{"identity none", func(s *Selection) { s.IdentityFiles = []string{"none"} }, ErrChanged},
		{"empty snapshot", func(s *Selection) { s.Snapshot = "" }, ErrUnresolved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection := base
			tt.edit(&selection)
			plan, err := PrepareDirectProbe(config, "", selection, []byte("fixture known hosts"))
			if !errors.Is(err, tt.want) || plan != nil {
				t.Fatalf("plan = %v, err = %v, want %v", plan, err, tt.want)
			}
		})
	}
	if plan, err := PrepareDirectProbe(config, "", base, nil); !errors.Is(err, ErrUnresolved) || plan != nil {
		t.Fatalf("empty trust snapshot: plan = %v, err = %v", plan, err)
	}
	for _, proxy := range []string{"ProxyJump jump.example.test", "ProxyCommand helper example.test"} {
		writeFixture(t, config, "Host selected\n HostName example.test\n User person\n "+proxy+"\n")
		selected, err := ResolveStatic(config, "", "selected")
		if err != nil {
			t.Fatal(err)
		}
		if plan, err := PrepareDirectProbe(config, "", selected, []byte("fixture known hosts")); !errors.Is(err, ErrProbeUnsupported) || plan != nil {
			t.Fatalf("proxy route: plan = %v, err = %v", plan, err)
		}
	}
	writeFixture(t, config, "Host selected\n HostName changed.example.test\n User person\n")
	if plan, err := PrepareDirectProbe(config, "", base, []byte("fixture known hosts")); !errors.Is(err, ErrChanged) || plan != nil {
		t.Fatalf("stale config: plan = %v, err = %v", plan, err)
	}
}

func TestDirectProbeOpenSSHEffectiveOptions(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client unavailable")
	}
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host selected\n HostName example.test\n User person\n Port 2222\n")
	selection, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareDirectProbe(config, "", selection, []byte("example.test ssh-ed25519 fixture\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plan.Close() }()
	args := append([]string{"-G"}, plan.Arguments()...)
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
