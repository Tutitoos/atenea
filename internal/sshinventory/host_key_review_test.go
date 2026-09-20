package sshinventory

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func ed25519FixtureKey() (string, string) {
	blob := make([]byte, 4+len("ssh-ed25519")+4+32)
	binary.BigEndian.PutUint32(blob[:4], uint32(len("ssh-ed25519")))
	copy(blob[4:], "ssh-ed25519")
	keyLength := 4 + len("ssh-ed25519")
	binary.BigEndian.PutUint32(blob[keyLength:keyLength+4], 32)
	for i := keyLength + 4; i < len(blob); i++ {
		blob[i] = byte(i)
	}
	digest := sha256.Sum256(blob)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob), "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:])
}

func TestMatchDirectED25519HostKeyBindsHostPortAndSnapshot(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host selected\n HostName host.example.test\n User person\n Port 2222\n")
	selection, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	candidate, fingerprint := ed25519FixtureKey()
	entry, err := MatchDirectED25519HostKey(config, "", selection, candidate, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(entry.KnownHostsLine()), "[host.example.test]:2222 ssh-ed25519 ") || entry.Snapshot() != selection.Snapshot {
		t.Fatalf("wrong host binding or snapshot")
	}
	writeFixture(t, config, "Host selected\n HostName changed.example.test\n User person\n Port 2222\n")
	if entry, err := MatchDirectED25519HostKey(config, "", selection, candidate, fingerprint); !errors.Is(err, ErrChanged) || len(entry.KnownHostsLine()) != 0 {
		t.Fatalf("stale config accepted: %v", err)
	}
}

func TestMatchDirectED25519HostKeyRejectsMismatchAndUnsupported(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host selected\n HostName host.example.test\n User person\n HostKeyAlias reviewed-host\n")
	selection, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	candidate, fingerprint := ed25519FixtureKey()
	wrong := "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, sha256.Size))
	if entry, err := MatchDirectED25519HostKey(config, "", selection, candidate, wrong); !errors.Is(err, ErrFingerprintMismatch) || len(entry.KnownHostsLine()) != 0 {
		t.Fatalf("mismatched fingerprint accepted: %v", err)
	}
	entry, err := MatchDirectED25519HostKey(config, "", selection, candidate, fingerprint)
	if err != nil || !strings.HasPrefix(string(entry.KnownHostsLine()), "reviewed-host ssh-ed25519 ") {
		t.Fatalf("HostKeyAlias binding failed: %v", err)
	}
	for _, malformed := range []string{candidate + "\nother.example.test ssh-ed25519 fake", "ssh-rsa AAAA", "ssh-ed25519 AAAA"} {
		if entry, err := MatchDirectED25519HostKey(config, "", selection, malformed, fingerprint); err == nil || len(entry.KnownHostsLine()) != 0 {
			t.Fatalf("malformed candidate accepted")
		}
	}
	writeFixture(t, config, "Host selected\n HostName bad]\n User person\n Port 2222\n")
	malformedHost, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	if entry, err := MatchDirectED25519HostKey(config, "", malformedHost, candidate, fingerprint); !errors.Is(err, ErrUnresolved) || len(entry.KnownHostsLine()) != 0 {
		t.Fatalf("malformed host created known_hosts entry: %v", err)
	}
}
