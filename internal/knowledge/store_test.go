package knowledge

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type testResolver struct {
	bundle EvidenceBundle
	calls  atomic.Int32
}

func (r *testResolver) Resolve(_ context.Context, _ string, _ Scope, _ string, digest string) (EvidenceBundle, error) {
	r.calls.Add(1)
	r.bundle.KnowledgeDigest = digest
	for _, receipt := range []*EvidenceReceipt{&r.bundle.Acceptance, &r.bundle.SolReview, &r.bundle.AstraAudit} {
		if receipt.Digest != "forged" {
			receipt.KnowledgeDigest = digest
			receipt.Digest = EvidenceDigest(*receipt)
		}
	}
	return r.bundle, nil
}

func evidenceBundle(scope Scope, point string) EvidenceBundle {
	source := Source{ID: "workflow:" + point, Kind: "workflow", Locator: "workflow/status", Digest: "tree-1"}
	dependency := Dependency{Source: Source{ID: "provider:" + point, Kind: "provider", Locator: "context/status", Digest: "snapshot-1"}, Provider: ProviderIdentity{Name: "fixture", Version: "1", Instance: "test", ConfigDigest: "config-1"}, Generation: 1, Snapshot: "snapshot-1", Freshness: "fresh", CheckedAt: time.Now().UTC(), TTLSeconds: 3600}
	makeReceipt := func(kind ReceiptKind, id string) EvidenceReceipt {
		role := map[ReceiptKind]string{AcceptanceReceipt: "implement", SolReviewReceipt: "review", AstraAuditReceipt: "audit"}[kind]
		r := EvidenceReceipt{ID: id, Kind: kind, PointID: point, Scope: scope, TreeDigest: "tree-1", Model: "model-1", RequestedModel: "model-1", RequestedEffort: "medium", ObservedEffort: "medium", Backend: "fixture", Role: role, Complete: true, Approved: true, Sources: []Source{source}, Dependencies: []Dependency{dependency}}
		r.Digest = EvidenceDigest(r)
		return r
	}
	return EvidenceBundle{Scope: scope, PointID: point, TreeDigest: "tree-1", Model: "model-1", Acceptance: makeReceipt(AcceptanceReceipt, "accept-"+point), SolReview: makeReceipt(SolReviewReceipt, "sol-"+point), AstraAudit: makeReceipt(AstraAuditReceipt, "astra-"+point)}
}

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "knowledge.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.resolver = &testResolver{}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func permission(project, repository string, read, write, promote bool) Permission {
	return Permission{SubjectID: "owner", ProjectID: project, RepositoryID: repository, Read: read, Write: write, Promote: promote, ProjectMember: true}
}

func candidate(id, project, repository string) Entry {
	return Entry{ID: id, OwnerID: "owner", Scope: Scope{ProjectID: project, RepositoryID: repository}, Kind: Decision,
		Title: "use the verified route", Body: "the route is bounded", Visibility: Project,
		Sources:      []Source{{ID: "git:" + id, Kind: "git", Locator: "docs/decision.md", Digest: "sha256:" + id}},
		Dependencies: []Dependency{{Source: Source{ID: "graph:" + id, Kind: "graph", Locator: "graph_status", Digest: "gen-1"}, Provider: ProviderIdentity{Name: "kivgraph", Version: "0.9.2", Instance: "fixture", ConfigDigest: "cfg"}, Generation: 1, Snapshot: "snap-1", Freshness: "fresh", CheckedAt: time.Now().UTC(), TTLSeconds: 3600}}}
}

func promote(t *testing.T, store *Store, entry Entry) {
	t.Helper()
	store.resolver = &testResolver{bundle: evidenceBundle(entry.Scope, "P00-"+entry.ID)}
	if err := store.PromoteVerified(context.Background(), entry.ID, PromotionGate{WorkflowID: "workflow-fixture", Scope: entry.Scope, PointID: "P00-" + entry.ID}, Permission{SubjectID: "owner", ProjectID: entry.Scope.ProjectID, RepositoryID: entry.Scope.RepositoryID, Promote: true, Read: true, ProjectMember: true}); err != nil {
		t.Fatal(err)
	}
}

func TestCandidatePromotionGateAndAppendOnlyEvents(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	entry := candidate("decision-1", "project-a", "repo-a")
	write := permission("project-a", "repo-a", false, true, false)
	if err := store.PutCandidate(ctx, entry, write); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCandidate(ctx, entry, write); err != nil {
		t.Fatalf("idempotent put: %v", err)
	}
	resolver := &testResolver{bundle: evidenceBundle(entry.Scope, "P00")}
	resolver.bundle.AstraAudit.Digest = "forged"
	store.resolver = resolver
	if err := store.PromoteVerified(ctx, entry.ID, PromotionGate{WorkflowID: "workflow-fixture", Scope: entry.Scope, PointID: "P00"}, Permission{SubjectID: "owner", ProjectID: "project-a", RepositoryID: "repo-a", Promote: true, Read: true, ProjectMember: true}); err == nil {
		t.Fatal("forged evidence promoted an entry")
	}
	resolver.bundle = evidenceBundle(entry.Scope, "P01")
	if err := store.PromoteVerified(ctx, entry.ID, PromotionGate{WorkflowID: "workflow-fixture", Scope: entry.Scope, PointID: "P01"}, Permission{SubjectID: "owner", ProjectID: "project-a", RepositoryID: "repo-a", Promote: true, Read: true, ProjectMember: true}); err != nil {
		t.Fatal(err)
	}
	var snapshots int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM knowledge_evidence WHERE project_id=? AND repository_id=? AND point_id=?`, entry.Scope.ProjectID, entry.Scope.RepositoryID, "P01").Scan(&snapshots); err != nil || snapshots != 3 {
		t.Fatalf("evidence snapshots=%d err=%v", snapshots, err)
	}
	rows, err := store.Query(ctx, Query{Scope: entry.Scope, Permission: permission("project-a", "repo-a", true, false, false), Statuses: []Status{Verified}})
	if err != nil || len(rows) != 1 || rows[0].Status != Verified {
		t.Fatalf("verified query = %v, %v", rows, err)
	}
	events, err := store.Events(ctx, entry.Scope, permission("project-a", "repo-a", true, false, false), 20)
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %+v, %v", events, err)
	}
	if _, err := store.db.Exec(`UPDATE knowledge_events SET action='tampered'`); err == nil {
		t.Fatal("event update was allowed")
	}
	if _, err := store.db.Exec(`DELETE FROM knowledge_events`); err == nil {
		t.Fatal("event delete was allowed")
	}
}

func TestScopePermissionAndPrivateACLBeforeProbe(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	entry := candidate("private", "project-a", "repo-a")
	entry.Visibility = Private
	if err := store.PutCandidate(ctx, entry, permission("project-a", "repo-a", false, true, false)); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Query(ctx, Query{Scope: entry.Scope, Permission: Permission{SubjectID: "other", ProjectID: "project-a", RepositoryID: "repo-a", Read: true, ProjectMember: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatal("private entry was revealed")
	}
	var probes atomic.Int32
	probe := func(context.Context, Entry) (ProbeResult, error) {
		probes.Add(1)
		return ProbeResult{Complete: true, Success: true, Dependencies: entry.Dependencies}, nil
	}
	if err := store.Revalidate(ctx, entry.ID, Permission{SubjectID: "other", ProjectID: "project-b", RepositoryID: "repo-a", Read: true}, probe); err == nil {
		t.Fatal("foreign scope revalidated an entry")
	}
	if probes.Load() != 0 {
		t.Fatal("probe ran before scope permission was checked")
	}
}

func TestRevalidateFailureAndDependencyChange(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	entry := candidate("decision-1", "project-a", "repo-a")
	if err := store.PutCandidate(ctx, entry, permission("project-a", "repo-a", false, true, false)); err != nil {
		t.Fatal(err)
	}
	if err := store.Revalidate(ctx, entry.ID, permission("project-a", "repo-a", true, false, false), func(context.Context, Entry) (ProbeResult, error) {
		return ProbeResult{Complete: false, Success: false}, nil
	}); err == nil {
		t.Fatal("partial revalidation accepted")
	}
	changed := append([]Dependency(nil), entry.Dependencies...)
	changed[0].Generation = 2
	changed[0].Source.Digest = "gen-2"
	changed[0].Snapshot = "snap-2"
	if err := store.Revalidate(ctx, entry.ID, permission("project-a", "repo-a", true, false, false), func(context.Context, Entry) (ProbeResult, error) {
		return ProbeResult{Complete: true, Success: true, Sources: entry.Sources, Dependencies: changed}, nil
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Query(ctx, Query{Scope: entry.Scope, Permission: permission("project-a", "repo-a", true, false, false), Statuses: []Status{Stale}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("stale rows = %v, %v", rows, err)
	}
}

func TestScopeIsolationAndSupersede(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	a, b, replacement := candidate("a", "project-a", "repo-a"), candidate("b", "project-b", "repo-b"), candidate("replacement", "project-a", "repo-a")
	for _, item := range []Entry{a, b, replacement} {
		if err := store.PutCandidate(ctx, item, permission(item.Scope.ProjectID, item.Scope.RepositoryID, false, true, false)); err != nil {
			t.Fatal(err)
		}
	}
	promote(t, store, a)
	promote(t, store, replacement)
	if err := store.Supersede(ctx, a.Scope, a.ID, replacement.ID, Permission{SubjectID: "owner", ProjectID: "project-a", RepositoryID: "repo-a", Write: true, Read: true, ProjectMember: true}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Query(ctx, Query{Scope: a.Scope, Permission: permission("project-a", "repo-a", true, false, false), Statuses: []Status{Superseded}})
	if err != nil || len(rows) != 1 || rows[0].Status != Superseded {
		t.Fatalf("superseded = %v, %v", rows, err)
	}
	if err := store.Supersede(ctx, a.Scope, a.ID, replacement.ID, Permission{SubjectID: "owner", ProjectID: "project-a", RepositoryID: "repo-a", Write: true, Read: true, ProjectMember: true}); err == nil {
		t.Fatal("terminal supersede repeated")
	}
}

func TestConcurrentRestartAndBackupRestore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "knowledge.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	write := permission("project-a", "*", false, true, false)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i))
			if err := first.PutCandidate(ctx, candidate(id, "project-a", "repo-"+id), write); err != nil {
				t.Errorf("first put: %v", err)
			}
		}(i)
	}
	for i := 12; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i))
			if err := second.PutCandidate(ctx, candidate(id, "project-a", "repo-"+id), write); err != nil {
				t.Errorf("second put: %v", err)
			}
		}(i)
	}
	wg.Wait()
	_ = first.Close()
	_ = second.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	backup := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := reopened.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	backupStore, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	backupStore.Close()
	restored := filepath.Join(t.TempDir(), "restored.sqlite")
	if err := Restore(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(restored); err != nil {
		t.Fatal(err)
	}
}

func TestDependencyValidationRejectsIncompleteIdentity(t *testing.T) {
	store, _ := testStore(t)
	bad := candidate("bad", "project-a", "repo-a")
	bad.Dependencies[0].Provider = ProviderIdentity{}
	bad.Dependencies[0].Generation = 2
	if err := store.PutCandidate(context.Background(), bad, permission("project-a", "repo-a", false, true, false)); err == nil {
		t.Fatal("dependency without provider identity accepted")
	}
}

func TestContextProviderExcludesCandidatesAndStale(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	verified := candidate("verified", "p", "r")
	stale := candidate("stale", "p", "r")
	if err := store.PutCandidate(ctx, verified, permission("p", "r", false, true, false)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCandidate(ctx, stale, permission("p", "r", false, true, false)); err != nil {
		t.Fatal(err)
	}
	promote(t, store, verified)
	promote(t, store, stale)
	if err := store.MarkStale(ctx, stale.ID, Permission{SubjectID: "owner", ProjectID: "p", RepositoryID: "r", Write: true, Read: true, ProjectMember: true}); err != nil {
		t.Fatal(err)
	}
	entries, err := (ContextProvider{Store: store, Scope: Scope{ProjectID: "p", RepositoryID: "r"}, Permission: permission("p", "r", true, false, false), Probe: func(context.Context, Entry) (ProbeResult, error) {
		return ProbeResult{Complete: true, Success: true, Sources: verified.Sources, Dependencies: verified.Dependencies}, nil
	}}).Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != verified.ID {
		t.Fatalf("prepared context = %+v", entries)
	}
	verified.Dependencies[0].Freshness = "stale"
	if err := store.PutCandidate(ctx, candidate("fresh-copy", "p", "r"), permission("p", "r", false, true, false)); err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgeDigestIgnoresObservationClockButBindsSnapshot(t *testing.T) {
	entry := candidate("digest", "p", "r")
	first := KnowledgeDigest(entry)
	entry.Dependencies[0].CheckedAt = entry.Dependencies[0].CheckedAt.Add(24 * time.Hour)
	entry.Dependencies[0].TTLSeconds = 30
	entry.Dependencies[0].Freshness = "stale"
	if got := KnowledgeDigest(entry); got != first {
		t.Fatalf("observation metadata changed knowledge digest: %s != %s", got, first)
	}
	entry.Dependencies[0].Snapshot = "snap-2"
	if got := KnowledgeDigest(entry); got == first {
		t.Fatal("dependency snapshot change did not change knowledge digest")
	}
}

func TestContextProviderRejectsExpiredProbeObservation(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	entry := candidate("expired-probe", "p", "r")
	if err := store.PutCandidate(ctx, entry, permission("p", "r", false, true, false)); err != nil {
		t.Fatal(err)
	}
	promote(t, store, entry)
	called := false
	got, err := (ContextProvider{Store: store, Scope: entry.Scope, Permission: permission("p", "r", true, false, false), Probe: func(context.Context, Entry) (ProbeResult, error) {
		called = true
		deps := append([]Dependency(nil), entry.Dependencies...)
		deps[0].CheckedAt = time.Now().UTC().Add(-2 * time.Hour)
		deps[0].TTLSeconds = 1
		return ProbeResult{Complete: true, Success: true, Sources: entry.Sources, Dependencies: deps}, nil
	}}).Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !called || len(got) != 0 {
		t.Fatalf("expired probe observation was accepted: called=%v got=%+v", called, got)
	}
}

func TestContextProviderMarksSemanticDependencyChangeStale(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	entry := candidate("semantic-change", "p", "r")
	expectedDigest := KnowledgeDigest(entry)
	if err := store.PutCandidate(ctx, entry, permission("p", "r", false, true, false)); err != nil {
		t.Fatal(err)
	}
	promote(t, store, entry)
	got, err := (ContextProvider{Store: store, Scope: entry.Scope, Permission: permission("p", "r", true, false, false), Probe: func(context.Context, Entry) (ProbeResult, error) {
		deps := append([]Dependency(nil), entry.Dependencies...)
		deps[0].Snapshot, deps[0].Generation, deps[0].CheckedAt = "snap-2", 2, time.Now().UTC()
		return ProbeResult{Complete: true, Success: true, Sources: entry.Sources, Dependencies: deps}, nil
	}}).Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("changed semantic dependency was returned: %+v", got)
	}
	rows, err := store.Query(ctx, Query{Scope: entry.Scope, Permission: permission("p", "r", true, false, false), Statuses: []Status{Stale}})
	if err != nil || len(rows) != 1 || rows[0].KnowledgeDigest != expectedDigest {
		t.Fatalf("stale rows=%+v err=%v", rows, err)
	}
}

func TestResolverRejectsPartialAndForeignEvidence(t *testing.T) {
	store, _ := testStore(t)
	scope := Scope{ProjectID: "p", RepositoryID: "r"}
	entry := candidate("e", scope.ProjectID, scope.RepositoryID)
	if err := store.PutCandidate(context.Background(), entry, permission("p", "r", false, true, false)); err != nil {
		t.Fatal(err)
	}
	resolver := &testResolver{bundle: evidenceBundle(scope, "P")}
	resolver.bundle.AstraAudit.Complete = false
	store.resolver = resolver
	if err := store.PromoteVerified(context.Background(), entry.ID, PromotionGate{WorkflowID: "workflow-fixture", Scope: scope, PointID: "P"}, Permission{SubjectID: "owner", ProjectID: "p", RepositoryID: "r", Promote: true, ProjectMember: true}); err == nil {
		t.Fatal("partial evidence promoted")
	}
	resolver.bundle = evidenceBundle(Scope{ProjectID: "other", RepositoryID: "r"}, "P")
	if err := store.PromoteVerified(context.Background(), entry.ID, PromotionGate{WorkflowID: "workflow-fixture", Scope: scope, PointID: "P"}, Permission{SubjectID: "owner", ProjectID: "p", RepositoryID: "r", Promote: true, ProjectMember: true}); err == nil {
		t.Fatal("foreign evidence promoted")
	}
}

func TestIncompatibleSchemaAndDatabaseSymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "future.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE knowledge_meta(key TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO knowledge_meta(key,value) VALUES('schema_version','999')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("incompatible schema was accepted")
	}
	link := filepath.Join(dir, "link.sqlite")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("database symlink was accepted")
	}
	parentLink := filepath.Join(dir, "parent-link")
	if err := os.Symlink(dir, parentLink); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(parentLink, "child.sqlite")); err == nil {
		t.Fatal("arbitrary parent symlink was accepted")
	}
}

func TestRestoreEscapedURIAndSchemaIntegrity(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "space #? dir")
	source := filepath.Join(dir, "source #?.sqlite")
	store, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored #? space.sqlite")
	if err := Restore(ctx, source, destination); err != nil {
		t.Fatal(err)
	}
	if mode := mustFileMode(t, destination); mode.Perm() != 0o600 {
		t.Fatalf("destination mode=%o", mode.Perm())
	}
}

func mustFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}
