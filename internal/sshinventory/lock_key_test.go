package sshinventory

import (
	"errors"
	"path/filepath"
	"testing"
)

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
