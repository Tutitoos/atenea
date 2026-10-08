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

func TestResolveStaticUsesLocalAccountWhenUserIsAbsent(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client unavailable")
	}
	local, err := localSSHUser()
	if err != nil {
		t.Skipf("local account cannot be represented as a safe SSH user: %v", err)
	}
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host selected\n HostName example.test\n")
	selected, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	if selected.User != local {
		t.Fatalf("implicit user = %q, want local account", selected.User)
	}
	output, err := exec.Command(ssh, "-G", "-F", config, "selected").CombinedOutput()
	if err != nil {
		t.Fatalf("OpenSSH fixture: %v: %s", err, output)
	}
	settings := strings.ReplaceAll(string(output), "\r\n", "\n")
	if !strings.Contains("\n"+settings, "\nuser "+selected.User+"\n") {
		t.Fatal("implicit user differs from OpenSSH effective configuration")
	}
	if err := RevalidateSelection(config, "", selected); err != nil {
		t.Fatalf("implicit user did not revalidate: %v", err)
	}
}

func TestResolveStaticRequiresListedConcreteAlias(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host listed\n HostName listed.example.test\nHost *\n User person\n")
	if _, err := ResolveStatic(config, "", "unlisted.example.test"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("wildcard-only target resolved: %v", err)
	}
	if _, err := ResolveStatic(config, "", "listed"); err != nil {
		t.Fatalf("listed target rejected: %v", err)
	}
}

func TestWindowsDefaultSSHUserKeepsDomainAuthority(t *testing.T) {
	for _, tt := range []struct{ account, computer, want string }{
		{`WORKSTATION\local`, "WORKSTATION", "local"},
		{`workstation\local`, "WORKSTATION.example.test", "local"},
		{`CORP\Person`, "WORKSTATION", `corp\person`},
		{`Person`, "WORKSTATION", "person"},
	} {
		if got := windowsDefaultSSHUser(tt.account, tt.computer); got != tt.want {
			t.Fatalf("Windows default for %q = %q, want %q", tt.account, got, tt.want)
		}
	}
	if !safeAccountArgument(`CORP\person`) {
		t.Fatal("domain-qualified SSH login was rejected")
	}
}

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

func TestResolveStaticSystemDefaultsForRestrictedProbe(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client unavailable")
	}
	root := t.TempDir()
	user := filepath.Join(root, "user", "config")
	system := filepath.Join(root, "system", "ssh_config")
	writeFixture(t, user, "Host selected\n HostName selected.example.test\n User person\n Ciphers chacha20-poly1305@openssh.com\n")
	writeFixture(t, system, "Include "+filepath.Join(root, "system", "conf.d", "*")+"\nHost *\n SendEnv LANG LC_*\n SendEnv -LC_SECRET\n HashKnownHosts yes\n GSSAPIAuthentication yes\n GSSAPIDelegateCredentials no\n Ciphers aes128-ctr\n KexAlgorithms curve25519-sha256\n MACs hmac-sha2-256\n")
	writeFixture(t, filepath.Join(root, "system", "conf.d", "10-defaults.conf"), "Host *\n SendEnv TERM\n")
	selected, err := ResolveStatic(user, system, "selected")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Ciphers != "chacha20-poly1305@openssh.com" || selected.KexAlgorithms != "curve25519-sha256" || selected.MACs != "hmac-sha2-256" {
		t.Fatalf("wrong negotiation policy: %+v", selected)
	}
	plan, err := PrepareDirectProbe(user, system, selected, directPinFixture("selected.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plan.Close() }()
	output, err := exec.Command(ssh, append([]string{"-G"}, plan.Arguments()...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("restricted ssh -G: %v: %s", err, output)
	}
	for _, expected := range []string{"ciphers chacha20-poly1305@openssh.com", "kexalgorithms curve25519-sha256", "macs hmac-sha2-256"} {
		if !strings.Contains(string(output), expected+"\n") {
			t.Fatalf("restricted config lost %q: %s", expected, output)
		}
	}
	if strings.Contains(string(output), "sendenv ") {
		t.Fatal("restricted no-session probe retained SendEnv")
	}
	if strings.Contains(string(output), "gssapiauthentication yes") || strings.Contains(string(output), "hashknownhosts yes") {
		t.Fatal("restricted probe retained suppressed system defaults")
	}
	if err := plan.Revalidate(); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, system, "Host *\n Ciphers aes128-ctr\n KexAlgorithms curve25519-sha256\n MACs hmac-sha2-512\n")
	if err := plan.Revalidate(); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed system policy revalidated: %v", err)
	}
}

func TestResolveStaticRejectsUnsafeSystemOptions(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "config")
	system := filepath.Join(root, "ssh_config")
	writeFixture(t, user, "Host selected\n HostName selected.example.test\n")
	for _, option := range []string{
		"SendEnv", "SendEnv -", "SendEnv LC_GOOD bad/name",
		"Ciphers aes128-ctr,", "KexAlgorithms +", "MACs hmac-sha2-256;helper",
		"HashKnownHosts maybe", "GSSAPIAuthentication maybe",
		"KnownHostsCommand /tmp/helper",
	} {
		t.Run(option, func(t *testing.T) {
			writeFixture(t, system, "Host *\n "+option+"\n")
			selection, err := ResolveStatic(user, system, "selected")
			if !errors.Is(err, ErrUnresolved) || selection.Snapshot != "" {
				t.Fatalf("unsafe system option resolved: %+v, %v", selection, err)
			}
		})
	}
	marker := filepath.Join(root, "helper-was-run")
	writeFixture(t, system, "Host *\n ProxyCommand "+sideEffectCommand(t, marker)+"\n")
	selection, err := ResolveStatic(user, system, "selected")
	if err != nil {
		t.Fatalf("proxy route should be recorded for review: %v", err)
	}
	if _, err := PrepareDirectProbe(user, system, selection, directPinFixture("selected.example.test")); !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("arbitrary system proxy route reached direct probe: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("system proxy helper executed: %v", err)
	}
}

func TestResolveStaticPreservesAlgorithmListModifiers(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client unavailable")
	}
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host selected\n HostName selected.example.test\n Ciphers -aes128-ctr\n KexAlgorithms ^curve25519-sha256\n MACs +hmac-sha2-256\n")
	selection, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	if selection.Ciphers != "-aes128-ctr" || selection.KexAlgorithms != "^curve25519-sha256" || selection.MACs != "+hmac-sha2-256" {
		t.Fatalf("list modifiers changed: %+v", selection)
	}
	plan, err := PrepareDirectProbe(config, "", selection, directPinFixture("selected.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plan.Close() }()
	output, err := exec.Command(ssh, append([]string{"-G"}, plan.Arguments()...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("restricted ssh -G: %v: %s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "ciphers ") && strings.Contains(","+strings.TrimPrefix(line, "ciphers ")+",", ",aes128-ctr,") {
			t.Fatalf("removed cipher survived: %s", line)
		}
		if strings.HasPrefix(line, "kexalgorithms ") && !strings.HasPrefix(line, "kexalgorithms curve25519-sha256,") {
			t.Fatalf("preferred KEX did not lead the list: %s", line)
		}
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
	command := sideEffectCommand(t, marker)
	config := filepath.Join(root, "config")
	writeFixture(t, config, "Host selected\n  ProxyCommand "+command+"\n  HostName example.invalid\n")
	got, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProxyCommand != command {
		t.Fatalf("ProxyCommand was not preserved: %q", got.ProxyCommand)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("proxy ran during resolution: %v", err)
	}
	writeFixture(t, config, "Host selected\n ProxyCommand sh -c 'echo altered'\n")
	if _, err := ResolveStatic(config, "", "selected"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("quoted ProxyCommand was reconstructed: %v", err)
	}
	writeFixture(t, config, "Match exec \""+command+"\"\nHost selected\n  HostName example.invalid\n")
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

func TestResolveStaticDoesNotStripProxyCommandHash(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client unavailable")
	}
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host selected\n HostName example.test\n User person\n ProxyCommand none # extra command text\n")
	output, err := exec.Command(ssh, "-G", "-F", config, "selected").CombinedOutput()
	if err != nil {
		t.Fatalf("ssh -G: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "proxycommand none # extra command text") {
		t.Fatalf("fixture did not preserve the command suffix: %s", output)
	}
	if selected, err := ResolveStatic(config, "", "selected"); !errors.Is(err, ErrUnresolved) || selected.Snapshot != "" {
		t.Fatalf("ProxyCommand suffix was treated as a disabled proxy: %+v, %v", selected, err)
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
