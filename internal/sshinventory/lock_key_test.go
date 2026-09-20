package sshinventory

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchedDirectAccountKeyGroupsAliasesAndInvalidatesChanges(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host first\n HostName first.example.test\n User person\nHost second\n HostName second.example.test\n User person\nHost other-account\n HostName first.example.test\n User another\n")
	candidate, fingerprint := ed25519FixtureKey()
	matched := func(alias, key, digest string) (Selection, DirectHostKeyEntry, string) {
		t.Helper()
		selection, err := ResolveStatic(config, "", alias)
		if err != nil {
			t.Fatal(err)
		}
		entry, err := MatchDirectED25519HostKey(config, "", selection, key, digest)
		if err != nil {
			t.Fatal(err)
		}
		id, err := MatchedDirectAccountKey(config, "", selection, entry)
		if err != nil || len(id) != 64 {
			t.Fatalf("matched key = %q, %v", id, err)
		}
		return selection, entry, id
	}
	first, firstEntry, firstID := matched("first", candidate, fingerprint)
	_, _, secondID := matched("second", candidate, fingerprint)
	if firstID != secondID {
		t.Fatal("same host key and account split across aliases")
	}
	_, _, otherAccountID := matched("other-account", candidate, fingerprint)
	if firstID == otherAccountID {
		t.Fatal("different accounts share an identity")
	}
	otherBlob := []byte("different fixture host key")
	otherDigest := sha256.Sum256(otherBlob)
	otherCandidate, _ := ed25519FixtureKey()
	// Modify only the 32-byte public key field while retaining a valid ED25519 blob.
	fields := strings.Fields(otherCandidate)
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	copy(blob[len(blob)-32:], otherDigest[:])
	changedKey := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)
	changedFingerprint := sha256.Sum256(blob)
	_, _, changedID := matched("first", changedKey, "SHA256:"+base64.RawStdEncoding.EncodeToString(changedFingerprint[:]))
	if firstID == changedID {
		t.Fatal("changed host key retained the identity")
	}
	second, err := ResolveStatic(config, "", "second")
	if err != nil {
		t.Fatal(err)
	}
	if id, err := MatchedDirectAccountKey(config, "", second, firstEntry); id != "" || !errors.Is(err, ErrChanged) {
		t.Fatalf("entry from a different alias accepted: %q, %v", id, err)
	}
	writeFixture(t, config, "Host first\n HostName changed.example.test\n User person\n")
	if id, err := MatchedDirectAccountKey(config, "", first, firstEntry); id != "" || !errors.Is(err, ErrChanged) {
		t.Fatalf("stale config accepted: %q, %v", id, err)
	}
}

func TestResolvedAliasesShareProvisionalLock(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	writeFixture(t, config, "Host first second\n HostName target.example.test\n User person\n Port 2222\n")
	first, err := ResolveStatic(config, "", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveStatic(config, "", "second")
	if err != nil {
		t.Fatal(err)
	}
	firstKey, err := ProvisionalDirectLockKey(first)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := ProvisionalDirectLockKey(second)
	if err != nil || firstKey != secondKey {
		t.Fatalf("aliases bypassed the same account lock: %v", err)
	}
	writeFixture(t, config, "Host renamed second\n HostName target.example.test\n User person\n Port 2222\n")
	renamed, err := ResolveStatic(config, "", "renamed")
	if err != nil {
		t.Fatal(err)
	}
	renamedKey, err := ProvisionalDirectLockKey(renamed)
	if err != nil || firstKey != renamedKey {
		t.Fatalf("renamed alias bypassed the same account lock: %v", err)
	}
	if err := RevalidateSelection(config, "", first); !errors.Is(err, ErrChanged) {
		t.Fatalf("old config snapshot retained authorization: %v", err)
	}
}

func TestProvisionalDirectLockKeyGroupsResolvedAliases(t *testing.T) {
	first := Selection{Alias: "first", HostName: "HOST.Example.Test", User: "person", Port: 22, Snapshot: "config-a"}
	second := Selection{Alias: "second", HostName: "host.example.test.", User: "person", Port: 22, Snapshot: "config-b", HostKeyAlias: "other-trust-name"}
	a, err := ProvisionalDirectLockKey(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ProvisionalDirectLockKey(second)
	if err != nil {
		t.Fatal(err)
	}
	if a == "" || a != b || len(a) != 64 {
		t.Fatalf("same direct endpoint/account has different lock keys")
	}
	for _, changed := range []Selection{
		{Alias: "other", HostName: "different.example.test", User: "person", Port: 22, Snapshot: "config-a"},
		{Alias: "first", HostName: "host.example.test", User: "other", Port: 22, Snapshot: "config-a"},
		{Alias: "first", HostName: "host.example.test", User: "person", Port: 2222, Snapshot: "config-a"},
	} {
		other, err := ProvisionalDirectLockKey(changed)
		if err != nil || other == a {
			t.Fatalf("distinct direct endpoint/account reused key: %v", err)
		}
	}
}

func TestProvisionalDirectLockKeyNormalizesIPv6(t *testing.T) {
	first := Selection{HostName: "[2001:0db8::1]", User: "person", Port: 22, Snapshot: "fixture"}
	second := Selection{HostName: "2001:db8:0:0:0:0:0:1", User: "person", Port: 22, Snapshot: "fixture"}
	a, err := ProvisionalDirectLockKey(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ProvisionalDirectLockKey(second)
	if err != nil || a != b {
		t.Fatalf("equivalent IPv6 literals have different lock keys: %v", err)
	}
}

func TestProvisionalDirectLockKeyRejectsUnsupportedRoute(t *testing.T) {
	base := Selection{HostName: "host.example.test", User: "person", Port: 22, Snapshot: "fixture"}
	base.ProxyJump = "gateway"
	if key, err := ProvisionalDirectLockKey(base); key != "" || !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("proxy route: key %q, err %v", key, err)
	}
	base.ProxyJump = ""
	base.Snapshot = ""
	if key, err := ProvisionalDirectLockKey(base); key != "" || !errors.Is(err, ErrUnresolved) {
		t.Fatalf("missing snapshot: key %q, err %v", key, err)
	}
}
