package sshinventory

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConfirmedSingleJumpPinsBindBothSelections(t *testing.T) {
	config, target, jump, pins := jumpFixture(t)
	fingerprint := func(line []byte) (string, string) {
		fields := strings.Fields(string(line))
		blob, err := base64.StdEncoding.DecodeString(fields[2])
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(blob)
		return fields[1] + " " + fields[2], "SHA256:" + base64.RawStdEncoding.EncodeToString(hash[:])
	}
	lines := strings.Split(strings.TrimSpace(string(pins)), "\n")
	candidateTarget, fingerprintTarget := fingerprint([]byte(lines[0]))
	candidateJump, fingerprintJump := fingerprint([]byte(lines[1]))
	matchedTarget, err := MatchSingleJumpED25519HostKey(config, "", target, jump, target, candidateTarget, fingerprintTarget)
	if err != nil {
		t.Fatal(err)
	}
	matchedJump, err := MatchSingleJumpED25519HostKey(config, "", target, jump, jump, candidateJump, fingerprintJump)
	if err != nil {
		t.Fatal(err)
	}
	confirmedTarget, err := ConfirmDirectED25519HostKey(config, "", target, matchedTarget, fingerprintTarget)
	if err != nil {
		t.Fatal(err)
	}
	confirmedJump, err := ConfirmDirectED25519HostKey(config, "", jump, matchedJump, fingerprintJump)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MatchDirectED25519HostKey(config, "", target, candidateTarget, fingerprintTarget); !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("direct matcher accepted proxy: %v", err)
	}
	if _, err := MatchSingleJumpED25519HostKey(config, "", target, jump, target, candidateTarget, fingerprintJump); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("wrong target fingerprint accepted: %v", err)
	}
	if runtime.GOOS == "windows" {
		if plan, err := PrepareConfirmedSingleJumpProbe(config, "", target, jump, confirmedTarget, confirmedJump); plan != nil || !errors.Is(err, ErrProbeUnsupported) {
			t.Fatalf("Windows jump execution accepted: %v", err)
		}
		return
	}
	if plan, err := PrepareConfirmedSingleJumpProbe(config, "", target, jump, confirmedJump, confirmedTarget); plan != nil || !errors.Is(err, ErrChanged) {
		t.Fatalf("swapped approvals accepted: %v", err)
	}
	if plan, err := PrepareConfirmedSingleJumpProbe(config, "", target, jump, confirmedTarget, ConfirmedDirectHostKey{}); plan != nil || !errors.Is(err, ErrChanged) {
		t.Fatalf("missing gateway approval accepted: %v", err)
	}
	plan, err := PrepareConfirmedSingleJumpProbe(config, "", target, jump, confirmedTarget, confirmedJump)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plan.Close() }()
	stored, err := os.ReadFile(filepath.Join(plan.root, "known_hosts"))
	if err != nil || string(stored) != string(pins) {
		t.Fatalf("reviewed pair changed: %v", err)
	}
	writeFixture(t, config, "Host selected\n HostName changed.example.test\n ProxyJump jump\nHost jump\n HostName jump.example.test\n")
	if plan, err := PrepareConfirmedSingleJumpProbe(config, "", target, jump, confirmedTarget, confirmedJump); plan != nil || !errors.Is(err, ErrChanged) {
		t.Fatalf("stale route accepted: %v", err)
	}
}

func syntheticJumpPin(token string, fill byte) []byte {
	var blob []byte
	blob = binary.BigEndian.AppendUint32(blob, uint32(len("ssh-ed25519")))
	blob = append(blob, "ssh-ed25519"...)
	blob = binary.BigEndian.AppendUint32(blob, 32)
	blob = append(blob, []byte(strings.Repeat(string(fill), 32))...)
	return []byte(token + " ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + "\n")
}

func jumpFixture(t *testing.T) (string, Selection, Selection, []byte) {
	t.Helper()
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host selected\n HostName target.example.test\n User destination\n Port 2222\n ProxyJump jump\n IdentityFile /nonexistent/target-key\n RemoteCommand echo wrong\nHost jump\n HostName jump.example.test\n User gateway\n Port 2200\n IdentityFile /nonexistent/jump-key\n LocalCommand echo wrong\n")
	target, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	jump, err := ResolveStatic(config, "", "jump")
	if err != nil {
		t.Fatal(err)
	}
	targetToken, err := directKnownHostToken(target)
	if err != nil {
		t.Fatal(err)
	}
	jumpToken, err := directKnownHostToken(jump)
	if err != nil {
		t.Fatal(err)
	}
	pins := append(syntheticJumpPin(targetToken, 'a'), syntheticJumpPin(jumpToken, 'b')...)
	return config, target, jump, pins
}

func TestPrepareSingleJumpProbeUsesPrivateGatewayConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("single-jump execution is not yet supported on Windows")
	}
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client unavailable")
	}
	config, target, jump, pins := jumpFixture(t)
	plan, err := PrepareSingleJumpProbe(config, "", target, jump, pins)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plan.Close() })
	if plan.jump == nil || plan.jump.Alias != jump.Alias || plan.Snapshot() != target.Snapshot {
		t.Fatal("jump identity or snapshot missing")
	}
	for _, name := range []string{"config", "known_hosts", "global_known_hosts"} {
		file, err := os.Open(filepath.Join(plan.root, name))
		if err != nil {
			t.Fatal(err)
		}
		info, statErr := file.Stat()
		private := statErr == nil && privateTrustFile(info, file)
		_ = file.Close()
		if !private {
			t.Fatalf("%s is not private", name)
		}
	}
	args := plan.Arguments()
	if !strings.Contains(strings.Join(args, "\x00"), "-J\x00jump") {
		t.Fatalf("selected jump route lost: %q", args)
	}
	output, err := exec.Command(ssh, append([]string{"-G"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("target effective config: %v: %s", err, output)
	}
	for _, want := range []string{"hostname target.example.test", "user destination", "port 2222", "proxyjump jump", "clearallforwardings yes"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("target effective config missing %q", want)
		}
	}
	jumpOutput, err := exec.Command(ssh, "-G", "-F", filepath.Join(plan.root, "config"), "jump").CombinedOutput()
	if err != nil {
		t.Fatalf("jump effective config: %v: %s", err, jumpOutput)
	}
	for _, want := range []string{"hostname jump.example.test", "user gateway", "port 2200", "identityfile /nonexistent/jump-key", "stricthostkeychecking true", "controlmaster false"} {
		if !strings.Contains(string(jumpOutput), want) {
			t.Fatalf("jump effective config missing %q", want)
		}
	}
	for _, settings := range [][]byte{output, jumpOutput} {
		if strings.Contains(string(settings), "remotecommand echo wrong") || strings.Contains(string(settings), "localcommand echo wrong") {
			t.Fatal("selected remote or local command reached the private configuration")
		}
	}
	if err := plan.Revalidate(); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, config, "Host selected\n HostName changed.example.test\n User destination\n ProxyJump jump\nHost jump\n HostName jump.example.test\n User gateway\n")
	if err := plan.Revalidate(); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed destination revalidated: %v", err)
	}
}

func TestPrepareSingleJumpProbeRejectsUnreviewedOrChangedRoutes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("single-jump execution is not yet supported on Windows")
	}
	config, target, jump, pins := jumpFixture(t)
	targetToken, err := directKnownHostToken(target)
	if err != nil {
		t.Fatal(err)
	}
	jumpToken, err := directKnownHostToken(jump)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		edit func(*Selection, *Selection)
		pins []byte
		want error
	}{
		{"missing jump pin", nil, syntheticJumpPin("[target.example.test]:2222", 'a'), ErrProbeUnsupported},
		{"unrelated pin", nil, append(append([]byte(nil), pins...), syntheticJumpPin("other.example.test", 'c')...), ErrProbeUnsupported},
		{"duplicate target pin", nil, append(append([]byte(nil), pins...), syntheticJumpPin(targetToken, 'c')...), ErrProbeUnsupported},
		{"shell-like alias", func(target, jump *Selection) { target.ProxyJump = "jump;bad"; jump.Alias = "jump;bad" }, pins, ErrChanged},
		{"changed jump", func(target, jump *Selection) { jump.HostName = "other.example.test" }, pins, ErrChanged},
		{"nested jump", func(target, jump *Selection) { jump.ProxyJump = "another" }, pins, ErrChanged},
		{"proxy command", func(target, jump *Selection) { target.ProxyCommand = "helper" }, pins, ErrChanged},
		{"missing target pin", nil, syntheticJumpPin(jumpToken, 'b'), ErrProbeUnsupported},
	} {
		t.Run(tt.name, func(t *testing.T) {
			destination, gateway := target, jump
			if tt.edit != nil {
				tt.edit(&destination, &gateway)
			}
			plan, err := PrepareSingleJumpProbe(config, "", destination, gateway, tt.pins)
			if !errors.Is(err, tt.want) || plan != nil {
				t.Fatalf("plan = %v, error = %v, want %v", plan, err, tt.want)
			}
		})
	}
}

func TestValidateJumpPinsRequiresTwoCanonicalLines(t *testing.T) {
	target := "[target.example.test]:2222"
	jump := "[jump.example.test]:2200"
	pins := append(syntheticJumpPin(target, 'a'), syntheticJumpPin(jump, 'b')...)
	if err := validateJumpPins(pins, target, jump); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		candidate []byte
		want      error
	}{
		{append([]byte(nil), pins[:len(pins)-1]...), ErrUnresolved},
		{append(append([]byte(nil), pins...), '\n'), ErrProbeUnsupported},
		{[]byte(strings.Replace(string(pins), " ssh-ed25519 ", "\tssh-ed25519\t", 1)), ErrUnresolved},
		{[]byte(strings.ReplaceAll(string(pins), "\n", "\r\n")), ErrUnresolved},
	} {
		if err := validateJumpPins(tt.candidate, target, jump); !errors.Is(err, tt.want) {
			t.Fatalf("noncanonical two-pin snapshot accepted: %v", err)
		}
	}
}

func TestSingleJumpRejectsCollidingKnownHostIdentity(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host selected\n HostName target.example.test\n User destination\n HostKeyAlias shared-pin\n ProxyJump jump\nHost jump\n HostName jump.example.test\n User gateway\n HostKeyAlias shared-pin\n")
	target, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	jump, err := ResolveStatic(config, "", "jump")
	if err != nil {
		t.Fatal(err)
	}
	candidate, digest := ed25519FixtureKey()
	if entry, err := MatchSingleJumpED25519HostKey(config, "", target, jump, target, candidate, digest); len(entry.KnownHostsLine()) != 0 || !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("two destinations sharing a pin identity accepted: %v", err)
	}
	if runtime.GOOS != "windows" {
		if plan, err := PrepareSingleJumpProbe(config, "", target, jump, syntheticJumpPin("shared-pin", 'a')); plan != nil || !errors.Is(err, ErrProbeUnsupported) {
			t.Fatalf("one pin for both hops accepted: %v", err)
		}
	}
}
