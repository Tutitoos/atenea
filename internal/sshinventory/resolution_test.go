package sshinventory

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResolveStaticFirstValuesAndConditionalIncludes(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user", "config")
	system := filepath.Join(root, "system", "ssh_config")
	writeFixture(t, filepath.Join(root, "user", "parts", "10-first"), "Host selected\n  HostName first.example.invalid\n  User first\n  IdentityFile ~/.ssh/first\n")
	writeFixture(t, filepath.Join(root, "user", "parts", "20-second"), "Host selected\n  HostName second.example.invalid\n  Port 2202\n  IdentityFile ~/.ssh/second\n")
	writeFixture(t, user, "Host other\n Include "+filepath.Join(root, "user", "parts", "*")+"\nHost selected\n Include "+filepath.Join(root, "user", "parts", "*")+"\nHost *\n Port 22\n")
	writeFixture(t, system, "Host selected\n  User system\n  HostKeyAlias reviewed-key\n")
	got, err := ResolveStatic(user, system, "selected")
	if err != nil {
		t.Fatal(err)
	}
	if got.HostName != "first.example.invalid" || got.User != "first" || got.Port != 2202 || got.HostKeyAlias != "reviewed-key" {
		t.Fatalf("wrong first-value resolution: %+v", got)
	}
	if want := []string{"~/.ssh/first", "~/.ssh/second"}; !reflect.DeepEqual(got.IdentityFiles, want) {
		t.Fatalf("identity files = %q, want %q", got.IdentityFiles, want)
	}
	if got.Snapshot == "" || len(got.Sources) != 4 {
		t.Fatalf("missing snapshot/provenance: %+v", got)
	}
}

func TestUserRelativeIncludeUsesOpenSSHHomeRoot(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client unavailable")
	}
	root := t.TempDir()
	config := filepath.Join(root, "config")
	includeName := filepath.Base(root) + "-include"
	userRoot, err := includeRootForConfig(config, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(userRoot, includeName)); err == nil || !os.IsNotExist(err) {
		t.Skip("test Include name already exists in the active SSH directory")
	}
	writeFixture(t, filepath.Join(root, includeName), "Host leaked\nHost selected\n HostName leaked.example.test\n")
	writeFixture(t, config, "Include "+includeName+"\nHost selected\n HostName actual.example.test\n")
	inventory, err := Scan(config, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := aliases(inventory.Hosts); !reflect.DeepEqual(got, []string{"selected"}) {
		t.Fatalf("config-adjacent file was mistaken for OpenSSH Include: %v", got)
	}
	selected, err := ResolveStatic(config, "", "selected")
	if err != nil || selected.HostName != "actual.example.test" {
		t.Fatalf("relative Include changed selected host: %+v, %v", selected, err)
	}
	output, err := exec.Command(ssh, "-G", "-F", config, "selected").CombinedOutput()
	if err != nil || !strings.Contains(string(output), "hostname actual.example.test") {
		t.Fatalf("OpenSSH disagrees about Include root: %v: %s", err, output)
	}
}

func TestUserTildeIncludeUsesHomeDirectory(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client unavailable")
	}
	root := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "child")
	relative, err := filepath.Rel(home, child)
	if err != nil || strings.ContainsAny(relative, " \t\r\n") {
		t.Skip("temporary fixture cannot be expressed as a simple home-relative path")
	}
	writeFixture(t, child, "Host included\nHost selected\n HostName tilde.example.test\n")
	config := filepath.Join(root, "config")
	writeFixture(t, config, "Include ~/"+filepath.ToSlash(relative)+"\nHost selected\n HostName fallback.example.test\n")
	inventory, err := Scan(config, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := aliases(inventory.Hosts); !reflect.DeepEqual(got, []string{"included", "selected"}) {
		t.Fatalf("home-relative Include aliases = %v", got)
	}
	selected, err := ResolveStatic(config, "", "selected")
	if err != nil || selected.HostName != "tilde.example.test" {
		t.Fatalf("home-relative Include resolution = %+v, %v", selected, err)
	}
	output, err := exec.Command(ssh, "-G", "-F", config, "selected").CombinedOutput()
	if err != nil || !strings.Contains(string(output), "hostname tilde.example.test") {
		t.Fatalf("OpenSSH disagrees about tilde Include: %v: %s", err, output)
	}
}

func TestResolveStaticNeverRunsDynamicMatchOrProxy(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "would-have-run")
	config := filepath.Join(root, "config")
	writeFixture(t, config, "Host selected\n  ProxyCommand touch "+marker+"\n  HostName example.invalid\n")
	got, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProxyCommand != "touch "+marker {
		t.Fatalf("ProxyCommand was not preserved: %q", got.ProxyCommand)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("proxy ran during resolution: %v", err)
	}
	writeFixture(t, config, "Host selected\n ProxyCommand sh -c 'echo altered'\n")
	if _, err := ResolveStatic(config, "", "selected"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("quoted ProxyCommand was reconstructed: %v", err)
	}
	writeFixture(t, config, "Match exec \"touch "+marker+"\"\nHost selected\n  HostName example.invalid\n")
	if _, err := ResolveStatic(config, "", "selected"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("dynamic Match accepted: %v", err)
	}
	writeFixture(t, config, "Match originalhost selected user fixture\nHost selected\n  HostName example.invalid\n")
	if _, err := ResolveStatic(config, "", "selected"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("combined Match criteria accepted without evaluation: %v", err)
	}
	writeFixture(t, config, "Match originalhost [s]elected\nHost selected\n  HostName example.invalid\n")
	if _, err := ResolveStatic(config, "", "selected"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("unsupported originalhost pattern syntax accepted: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Match exec ran during resolution: %v", err)
	}
}

func TestResolveStaticRejectsDynamicTargetAndPreservesProxyOrder(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	writeFixture(t, config, "Host selected\n ProxyJump gateway.example.invalid\n ProxyCommand echo bypass\n HostName target.example.invalid\n")
	got, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProxyJump != "gateway.example.invalid" || got.ProxyCommand != "" {
		t.Fatalf("proxy precedence changed: %+v", got)
	}
	writeFixture(t, config, "Host selected\n HostName %h.example.invalid\n")
	if _, err := ResolveStatic(config, "", "selected"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("dynamic HostName accepted: %v", err)
	}
	writeFixture(t, config, "Host selected\n CanonicalizeHostname yes\n")
	if _, err := ResolveStatic(config, "", "selected"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("canonicalization accepted: %v", err)
	}
}

func TestRevalidateSelectionRejectsConfigAndSelectionChanges(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host first second\n HostName target.example.test\n User person\n")
	first, err := ResolveStatic(config, "", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveStatic(config, "", "second")
	if err != nil {
		t.Fatal(err)
	}
	if err := RevalidateSelection(config, "", first); err != nil {
		t.Fatal(err)
	}
	if first.HostName != second.HostName || first.User != second.User || first.Port != second.Port {
		t.Fatal("aliases should resolve to the same provisional account")
	}
	tampered := first
	tampered.HostName = "other.example.test"
	if err := RevalidateSelection(config, "", tampered); !errors.Is(err, ErrChanged) {
		t.Fatalf("modified selected host: %v", err)
	}
	writeFixture(t, config, "Host renamed second\n HostName target.example.test\n User person\n")
	if err := RevalidateSelection(config, "", second); !errors.Is(err, ErrChanged) {
		t.Fatalf("renamed alias changed snapshot: %v", err)
	}
	if err := RevalidateSelection(config, "", first); !errors.Is(err, ErrChanged) && !errors.Is(err, ErrUnresolved) {
		t.Fatalf("removed alias must invalidate selection: %v", err)
	}
}
