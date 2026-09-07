package mcpactivation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestProposalRequiresObservedEvidenceAndRawPermissions(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "proposals.json"))
	base := Proposal{ID: "new", URL: "http://127.0.0.1/mcp", ProtocolMode: "auto", RequestedProtocolVersion: "2026-07-28", ObservedProtocolVersion: "2026-07-28", Expose: "raw"}
	if _, err := store.Put(base); err == nil {
		t.Fatal("raw proposal without tools/effects was accepted")
	}
	base.Tools, base.Effects = []string{"scan"}, []string{"read"}
	created, err := store.Put(base)
	if err != nil {
		t.Fatal(err)
	}
	if created.EvidenceDigest == "" || created.Status != Proposed {
		t.Fatalf("proposal = %#v", created)
	}
	got, err := store.Get("new")
	if err != nil {
		t.Fatal(err)
	}
	if got.EvidenceDigest != created.EvidenceDigest {
		t.Fatal("persistent proposal lost its evidence binding")
	}
	if _, err := store.Activate("new", "/settings", "different", func(Proposal) error { return nil }); err == nil {
		t.Fatal("changed evidence was activated")
	}
	if _, err := store.Activate("new", "/settings", created.EvidenceDigest, func(Proposal) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(base); err == nil {
		t.Fatal("applied integration was silently replaced")
	}
}

func TestPersistedProposalRejectsTamperingAndBindsCanonicalTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposals.json")
	store := New(path)
	base := Proposal{ID: "bound", URL: "http://127.0.0.1/mcp", ProtocolMode: "auto", RequestedProtocolVersion: "2026-07-28", ObservedProtocolVersion: "2026-07-28", Expose: "off"}
	created, err := store.Put(base)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []Proposal
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	rows[0].URL = "http://127.0.0.1/changed"
	raw, _ = json.Marshal(rows)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := store.Activate("bound", filepath.Join(dir, "settings.toml"), created.EvidenceDigest, func(Proposal) error { called = true; return nil }); err == nil || called {
		t.Fatalf("tampered proposal reached apply: err=%v called=%v", err, called)
	}

	path = filepath.Join(dir, "canonical.json")
	store = New(path)
	created, err = store.Put(base)
	if err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	if err := os.Mkdir(first, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(first)
	if _, err := store.Activate("bound", "settings.toml", created.EvidenceDigest, func(p Proposal) error {
		if !filepath.IsAbs(p.SettingsPath) {
			t.Fatalf("non-absolute target %q", p.SettingsPath)
		}
		return errors.New("crash boundary")
	}); err == nil {
		t.Fatal("failed apply was reported as applied")
	}
	t.Chdir(second)
	called = false
	if _, err := store.Activate("bound", "settings.toml", created.EvidenceDigest, func(Proposal) error { called = true; return nil }); err == nil || called {
		t.Fatalf("relative path rebound after cwd change: err=%v called=%v", err, called)
	}
}

func TestApplyingProposalRecoversAndConcurrentStoresDoNotLoseRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proposals.json")
	store := New(path)
	base := Proposal{ID: "recover", URL: "http://127.0.0.1/mcp", ProtocolMode: "auto", RequestedProtocolVersion: "2026-07-28", ObservedProtocolVersion: "2026-07-28", Expose: "off"}
	created, err := store.Put(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Activate("recover", "/settings", created.EvidenceDigest, func(Proposal) error { return errors.New("crash boundary") }); err == nil {
		t.Fatal("failed apply was reported as applied")
	}
	journal, err := store.Get("recover")
	if err != nil {
		t.Fatal(err)
	}
	if journal.Status != Applying {
		t.Fatalf("status = %s, want applying", journal.Status)
	}
	if _, err := store.Put(base); err == nil {
		t.Fatal("applying proposal was replaced")
	}
	if _, err := store.Activate("recover", "/other-settings", created.EvidenceDigest, func(Proposal) error { return nil }); err == nil {
		t.Fatal("applying proposal changed target settings")
	}
	if _, err := store.Activate("recover", "/settings", created.EvidenceDigest, func(Proposal) error { return nil }); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := base
			p.ID = id
			if _, putErr := New(path).Put(p); putErr != nil {
				t.Errorf("Put(%s): %v", id, putErr)
			}
		}()
	}
	wg.Wait()
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("concurrent rows = %d, want 3", len(rows))
	}
}
