// Package knowledge is the new, verified knowledge store. It is intentionally
// independent from notebook, history, and result-cache data: legacy records
// are legacy_unverified and are never silently imported or promoted.
package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const schemaVersion = 1

// Kind is part of ATENEA's public orchestration contract.
type Kind string

const (
	// Decision is part of ATENEA's public orchestration contract.
	Decision Kind = "decision"
	// Convention is part of ATENEA's public orchestration contract.
	Convention Kind = "convention"
	// Solution is part of ATENEA's public orchestration contract.
	Solution Kind = "solution"
	// Hypothesis is part of ATENEA's public orchestration contract.
	Hypothesis Kind = "hypothesis"
	// Fact is part of ATENEA's public orchestration contract.
	Fact Kind = "fact"
)

// Status is part of ATENEA's public orchestration contract.
type Status string

const (
	// Candidate is part of ATENEA's public orchestration contract.
	Candidate Status = "candidate"
	// Verified is part of ATENEA's public orchestration contract.
	Verified Status = "verified"
	// Stale is part of ATENEA's public orchestration contract.
	Stale Status = "stale"
	// Superseded is part of ATENEA's public orchestration contract.
	Superseded Status = "superseded"
)

// Visibility is part of ATENEA's public orchestration contract.
type Visibility string

const (
	// Private is part of ATENEA's public orchestration contract.
	Private Visibility = "private"
	// Project is part of ATENEA's public orchestration contract.
	Project Visibility = "project"
)

// Scope is part of ATENEA's public orchestration contract.
type Scope struct {
	ProjectID    string `json:"project_id"`
	RepositoryID string `json:"repository_id"`
}

// Permission is part of ATENEA's public orchestration contract.
type Permission struct {
	SubjectID     string
	ProjectID     string
	RepositoryID  string
	Read          bool
	Write         bool
	Promote       bool
	ProjectMember bool
}

// Source is part of ATENEA's public orchestration contract.
type Source struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Locator string `json:"locator"`
	Digest  string `json:"digest"`
}

// Dependency is part of ATENEA's public orchestration contract.
type Dependency struct {
	Source     Source           `json:"source"`
	Provider   ProviderIdentity `json:"provider,omitempty"`
	Generation int64            `json:"generation,omitempty"`
	Snapshot   string           `json:"snapshot,omitempty"`
	Freshness  string           `json:"freshness,omitempty"`
	CheckedAt  time.Time        `json:"checked_at,omitempty"`
	TTLSeconds int64            `json:"ttl_seconds,omitempty"`
}

// ProviderIdentity is part of ATENEA's public orchestration contract.
type ProviderIdentity struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Instance     string `json:"instance"`
	ConfigDigest string `json:"config_digest"`
}

// Entry is part of ATENEA's public orchestration contract.
type Entry struct {
	ID           string       `json:"id"`
	Scope        Scope        `json:"scope"`
	OwnerID      string       `json:"owner_id"`
	Kind         Kind         `json:"kind"`
	Status       Status       `json:"status"`
	Title        string       `json:"title"`
	Body         string       `json:"body"`
	Visibility   Visibility   `json:"visibility"`
	Sources      []Source     `json:"sources"`
	Dependencies []Dependency `json:"dependencies"`
	// KnowledgeDigest binds the candidate content to the workflow evidence
	// that is allowed to promote it. It is derived from the fields below and
	// never accepted as an independent assertion.
	KnowledgeDigest string    `json:"knowledge_digest"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Event is part of ATENEA's public orchestration contract.
type Event struct {
	Sequence  int64     `json:"sequence"`
	EntryID   string    `json:"entry_id"`
	Action    string    `json:"action"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

// PromotionGate is part of ATENEA's public orchestration contract.
type PromotionGate struct {
	WorkflowID string
	Scope      Scope
	PointID    string
}

// ReceiptKind is part of ATENEA's public orchestration contract.
type ReceiptKind string

const (
	// AcceptanceReceipt is part of ATENEA's public orchestration contract.
	AcceptanceReceipt ReceiptKind = "point_accepted"
	// SolReviewReceipt is part of ATENEA's public orchestration contract.
	SolReviewReceipt ReceiptKind = "sol_review_approved"
	// AstraAuditReceipt is part of ATENEA's public orchestration contract.
	AstraAuditReceipt ReceiptKind = "astra_audit_approved"
)

// EvidenceReceipt is supplied only by an EvidenceResolver. It cannot be
// inserted by a caller; Store persists a validated snapshot during promotion.
type EvidenceReceipt struct {
	ID              string       `json:"id"`
	Kind            ReceiptKind  `json:"kind"`
	PointID         string       `json:"point_id"`
	Scope           Scope        `json:"scope"`
	Digest          string       `json:"digest"`
	TreeDigest      string       `json:"tree_digest"`
	Model           string       `json:"model"`
	RequestedModel  string       `json:"requested_model"`
	RequestedEffort string       `json:"requested_effort"`
	ObservedEffort  string       `json:"observed_effort"`
	Backend         string       `json:"backend"`
	Role            string       `json:"role"`
	Complete        bool         `json:"complete"`
	Approved        bool         `json:"approved"`
	Sources         []Source     `json:"sources"`
	Dependencies    []Dependency `json:"dependencies"`
	KnowledgeDigest string       `json:"knowledge_digest"`
}

// EvidenceBundle is part of ATENEA's public orchestration contract.
type EvidenceBundle struct {
	Scope           Scope
	PointID         string
	TreeDigest      string
	Model           string
	KnowledgeDigest string
	Acceptance      EvidenceReceipt
	SolReview       EvidenceReceipt
	AstraAudit      EvidenceReceipt
}

// EvidenceResolver is the only authority accepted by the promotion gate. A
// resolver reads persisted workflow/acceptance state; callers cannot submit
// booleans or receipt rows directly.
type EvidenceResolver interface {
	Resolve(ctx context.Context, workflowID string, scope Scope, pointID, knowledgeDigest string) (EvidenceBundle, error)
}

// ProbeResult is part of ATENEA's public orchestration contract.
type ProbeResult struct {
	Complete     bool
	Success      bool
	Sources      []Source
	Dependencies []Dependency
}

// Query is part of ATENEA's public orchestration contract.
type Query struct {
	Scope      Scope
	Permission Permission
	Kinds      []Kind
	Statuses   []Status
}

// Store is part of ATENEA's public orchestration contract.
type Store struct {
	db       *sql.DB
	resolver EvidenceResolver
}

// OpenOption is part of ATENEA's public orchestration contract.
type OpenOption func(*Store)

// WithEvidenceResolver is part of ATENEA's public orchestration contract.
func WithEvidenceResolver(resolver EvidenceResolver) OpenOption {
	return func(store *Store) { store.resolver = resolver }
}

// Open is part of ATENEA's public orchestration contract.
func Open(path string, options ...OpenOption) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("knowledge: database path is required")
	}
	if path != ":memory:" {
		var err error
		path, err = secureDatabasePath(path)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("knowledge: create database directory: %w", err)
		}
	}
	dsn, err := sqliteDSN(path, false)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("knowledge: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range []string{
		"PRAGMA busy_timeout = 2500",
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("knowledge: %s: %w", statement, err)
		}
	}
	store := &Store{db: db}
	for _, option := range options {
		if option != nil {
			option(store)
		}
	}
	if err := store.schema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if path != ":memory:" {
		if err := secureDatabaseFiles(path); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return store, nil
}

// Close is part of ATENEA's public orchestration contract.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) schema() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS knowledge_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS knowledge_entries (
            project_id TEXT NOT NULL,
            repository_id TEXT NOT NULL,
            id TEXT NOT NULL,
            owner_id TEXT NOT NULL,
            kind TEXT NOT NULL CHECK(kind IN ('decision','convention','solution','hypothesis','fact')),
            status TEXT NOT NULL CHECK(status IN ('candidate','verified','stale','superseded')),
            title TEXT NOT NULL,
            body TEXT NOT NULL,
            visibility TEXT NOT NULL CHECK(visibility IN ('private','project')),
            sources_json TEXT NOT NULL,
            dependencies_json TEXT NOT NULL,
			knowledge_digest TEXT NOT NULL DEFAULT '',
            created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL,
            PRIMARY KEY(project_id,repository_id,id)
        )`,
		`CREATE TABLE IF NOT EXISTS knowledge_events (
            sequence INTEGER PRIMARY KEY AUTOINCREMENT,
            project_id TEXT NOT NULL,
            repository_id TEXT NOT NULL,
            entry_id TEXT NOT NULL,
            action TEXT NOT NULL,
            payload_json TEXT NOT NULL,
            created_at TEXT NOT NULL,
			FOREIGN KEY(project_id,repository_id,entry_id) REFERENCES knowledge_entries(project_id,repository_id,id)
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge_evidence (
			project_id TEXT NOT NULL,
			repository_id TEXT NOT NULL,
			point_id TEXT NOT NULL,
			kind TEXT NOT NULL CHECK(kind IN ('point_accepted','sol_review_approved','astra_audit_approved')),
			receipt_id TEXT NOT NULL,
			digest TEXT NOT NULL,
			tree_digest TEXT NOT NULL,
			model TEXT NOT NULL,
			requested_model TEXT NOT NULL DEFAULT '',
			requested_effort TEXT NOT NULL DEFAULT '',
			observed_effort TEXT NOT NULL DEFAULT '',
			backend TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT '',
			knowledge_digest TEXT NOT NULL DEFAULT '',
			complete INTEGER NOT NULL CHECK(complete IN (0,1)),
			approved INTEGER NOT NULL CHECK(approved IN (0,1)),
			sources_json TEXT NOT NULL,
			dependencies_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY(project_id,repository_id,point_id,kind,receipt_id)
		)`,
		`CREATE INDEX IF NOT EXISTS knowledge_entries_scope ON knowledge_entries(project_id, repository_id, status)`,
		`CREATE TRIGGER IF NOT EXISTS knowledge_events_no_update BEFORE UPDATE ON knowledge_events BEGIN SELECT RAISE(ABORT,'knowledge events are append-only'); END`,
		`CREATE TRIGGER IF NOT EXISTS knowledge_events_no_delete BEFORE DELETE ON knowledge_events BEGIN SELECT RAISE(ABORT,'knowledge events are append-only'); END`,
		`CREATE TRIGGER IF NOT EXISTS knowledge_evidence_no_update BEFORE UPDATE ON knowledge_evidence BEGIN SELECT RAISE(ABORT,'knowledge evidence is append-only'); END`,
		`CREATE TRIGGER IF NOT EXISTS knowledge_evidence_no_delete BEFORE DELETE ON knowledge_evidence BEGIN SELECT RAISE(ABORT,'knowledge evidence is append-only'); END`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("knowledge: schema: %w", err)
		}
	}
	var version string
	err = tx.QueryRow(`SELECT value FROM knowledge_meta WHERE key='schema_version'`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.Exec(`INSERT INTO knowledge_meta(key,value) VALUES('schema_version',?)`, schemaVersion)
	} else if err == nil && version != fmt.Sprint(schemaVersion) {
		return fmt.Errorf("knowledge: incompatible schema version %s", version)
	}
	if err != nil {
		return fmt.Errorf("knowledge: schema version: %w", err)
	}
	// These additive migrations keep databases created by the first P15
	// vertical readable while making the new binding fields durable.
	for _, add := range []struct{ table, column, ddl string }{
		{"knowledge_entries", "knowledge_digest", "ALTER TABLE knowledge_entries ADD COLUMN knowledge_digest TEXT NOT NULL DEFAULT ''"},
		{"knowledge_evidence", "requested_model", "ALTER TABLE knowledge_evidence ADD COLUMN requested_model TEXT NOT NULL DEFAULT ''"},
		{"knowledge_evidence", "requested_effort", "ALTER TABLE knowledge_evidence ADD COLUMN requested_effort TEXT NOT NULL DEFAULT ''"},
		{"knowledge_evidence", "observed_effort", "ALTER TABLE knowledge_evidence ADD COLUMN observed_effort TEXT NOT NULL DEFAULT ''"},
		{"knowledge_evidence", "backend", "ALTER TABLE knowledge_evidence ADD COLUMN backend TEXT NOT NULL DEFAULT ''"},
		{"knowledge_evidence", "role", "ALTER TABLE knowledge_evidence ADD COLUMN role TEXT NOT NULL DEFAULT ''"},
		{"knowledge_evidence", "knowledge_digest", "ALTER TABLE knowledge_evidence ADD COLUMN knowledge_digest TEXT NOT NULL DEFAULT ''"},
	} {
		var exists int
		err := tx.QueryRow(`SELECT 1 FROM pragma_table_info(?) WHERE name=?`, add.table, add.column).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.Exec(add.ddl); err != nil {
				return fmt.Errorf("knowledge: migration %s.%s: %w", add.table, add.column, err)
			}
		} else if err != nil {
			return fmt.Errorf("knowledge: migration inspect %s.%s: %w", add.table, add.column, err)
		}
	}
	return tx.Commit()
}

// PutCandidate is part of ATENEA's public orchestration contract.
func (s *Store) PutCandidate(ctx context.Context, entry Entry, permission Permission) error {
	if err := validateEntry(entry); err != nil {
		return err
	}
	if err := authorizeEntry(entry, permission, permission.Write); err != nil {
		return err
	}
	digest := KnowledgeDigest(entry)
	if entry.KnowledgeDigest != "" && entry.KnowledgeDigest != digest {
		return errors.New("knowledge: candidate knowledge digest mismatch")
	}
	entry.KnowledgeDigest = digest
	entry.Status = Candidate
	return s.withBusyRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		existing, err := scanEntry(tx.QueryRowContext(ctx, selectEntrySQL+" WHERE project_id=? AND repository_id=? AND id=?", entry.Scope.ProjectID, entry.Scope.RepositoryID, entry.ID))
		if err == nil {
			if existing.Status == Candidate {
				if entriesEqual(existing, entry) {
					return tx.Commit()
				}
				return errors.New("knowledge: candidate id already exists with different content")
			}
			return fmt.Errorf("knowledge: entry %q already exists with status %s", entry.ID, existing.Status)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := time.Now().UTC()
		entry.CreatedAt, entry.UpdatedAt = now, now
		if err := insertEntry(ctx, tx, entry); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, entry.Scope, entry.ID, "candidate_created", entry); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// PromoteVerified is part of ATENEA's public orchestration contract.
func (s *Store) PromoteVerified(ctx context.Context, id string, gate PromotionGate, permission Permission) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("knowledge: entry id is required")
	}
	if err := validateScope(gate.Scope); err != nil {
		return err
	}
	if strings.TrimSpace(gate.WorkflowID) == "" || strings.TrimSpace(gate.PointID) == "" {
		return errors.New("knowledge: workflow and acceptance point are required")
	}
	if !permission.Promote {
		return errors.New("knowledge: promotion permission denied")
	}
	if err := authorizeScope(gate.Scope, permission, true); err != nil {
		return err
	}
	if s.resolver == nil {
		return errors.New("knowledge: authoritative evidence resolver is not configured")
	}
	entry, err := s.get(ctx, id, permission)
	if err != nil {
		return err
	}
	if entry.Status != Candidate {
		return fmt.Errorf("knowledge: only candidates can be promoted, got %s", entry.Status)
	}
	digest := KnowledgeDigest(entry)
	if entry.KnowledgeDigest == "" || entry.KnowledgeDigest != digest {
		return errors.New("knowledge: candidate knowledge digest is missing or invalid")
	}
	bundle, err := s.resolver.Resolve(ctx, gate.WorkflowID, gate.Scope, gate.PointID, digest)
	if err != nil {
		return fmt.Errorf("knowledge: resolve acceptance evidence: %w", err)
	}
	if err := validateEvidenceBundle(bundle, gate.Scope, gate.PointID, digest); err != nil {
		return err
	}
	return s.withBusyRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		entry, err := scanEntry(tx.QueryRowContext(ctx, selectEntrySQL+" WHERE project_id=? AND repository_id=? AND id=?", gate.Scope.ProjectID, gate.Scope.RepositoryID, id))
		if err != nil {
			return err
		}
		if err := authorizeEntry(entry, permission, true); err != nil {
			return err
		}
		if entry.Status != Candidate {
			return fmt.Errorf("knowledge: only candidates can be promoted, got %s", entry.Status)
		}
		if KnowledgeDigest(entry) != digest || entry.KnowledgeDigest != digest {
			return errors.New("knowledge: candidate changed before promotion")
		}
		for _, receipt := range []EvidenceReceipt{bundle.Acceptance, bundle.SolReview, bundle.AstraAudit} {
			if err := insertEvidenceSnapshot(ctx, tx, receipt); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE knowledge_entries SET status='verified',updated_at=? WHERE project_id=? AND repository_id=? AND id=? AND status='candidate'`, time.Now().UTC().Format(time.RFC3339Nano), gate.Scope.ProjectID, gate.Scope.RepositoryID, id)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return errors.New("knowledge: promotion compare-and-swap failed")
		}
		payload := map[string]any{"workflow_id": gate.WorkflowID, "point_id": gate.PointID, "knowledge_digest": digest, "tree_digest": bundle.TreeDigest, "model": bundle.Model, "acceptance": bundle.Acceptance, "sol_review": bundle.SolReview, "astra_audit": bundle.AstraAudit}
		if err := insertEvent(ctx, tx, entry.Scope, id, "verified", payload); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// QueryVerified returns only accepted knowledge whose provider/source
// dependencies are currently fresh. It is the safe read path for context
// preparation; candidates, stale entries and legacy records are excluded.
func (s *Store) QueryVerified(ctx context.Context, scope Scope, permission Permission) ([]Entry, error) {
	entries, err := s.Query(ctx, Query{Scope: scope, Permission: permission, Statuses: []Status{Verified}})
	if err != nil {
		return nil, err
	}
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		fresh := true
		for _, dependency := range entry.Dependencies {
			if !strings.EqualFold(dependency.Freshness, "fresh") || dependency.CheckedAt.IsZero() || dependency.TTLSeconds <= 0 || time.Now().UTC().After(dependency.CheckedAt.Add(time.Duration(dependency.TTLSeconds)*time.Second)) {
				fresh = false
				break
			}
		}
		if fresh {
			result = append(result, entry)
		}
	}
	return result, nil
}

// AcceptanceGate is the production boundary for verified promotion. Callers
// cannot promote with self-reported booleans; all three persisted receipts
// are resolved and checked by PromoteVerified.
type AcceptanceGate struct{ Store *Store }

// Promote is part of ATENEA's public orchestration contract.
func (g AcceptanceGate) Promote(ctx context.Context, id string, gate PromotionGate, permission Permission) error {
	if g.Store == nil {
		return errors.New("knowledge: acceptance gate has no store")
	}
	return g.Store.PromoteVerified(ctx, id, gate, permission)
}

func validateEvidenceBundle(bundle EvidenceBundle, scope Scope, pointID, knowledgeDigest string) error {
	if bundle.Scope != scope || bundle.PointID != pointID || strings.TrimSpace(bundle.TreeDigest) == "" || bundle.KnowledgeDigest != knowledgeDigest {
		return errors.New("knowledge: evidence scope, point, and tree digest must match")
	}
	expected := []struct {
		receipt EvidenceReceipt
		kind    ReceiptKind
	}{{bundle.Acceptance, AcceptanceReceipt}, {bundle.SolReview, SolReviewReceipt}, {bundle.AstraAudit, AstraAuditReceipt}}
	for _, item := range expected {
		r := item.receipt
		if r.Kind != item.kind || r.Scope != scope || r.PointID != pointID || strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.Digest) == "" || strings.TrimSpace(r.TreeDigest) == "" || strings.TrimSpace(r.Model) == "" || strings.TrimSpace(r.RequestedModel) == "" || strings.TrimSpace(r.RequestedEffort) == "" || strings.TrimSpace(r.ObservedEffort) == "" || strings.TrimSpace(r.Backend) == "" || strings.TrimSpace(r.Role) == "" || r.RequestedModel != r.Model || r.RequestedEffort != r.ObservedEffort || r.TreeDigest != bundle.TreeDigest || r.KnowledgeDigest != knowledgeDigest || !r.Complete || !r.Approved {
			return fmt.Errorf("knowledge: invalid %s evidence", item.kind)
		}
		expectedRole := map[ReceiptKind]string{AcceptanceReceipt: "implement", SolReviewReceipt: "review", AstraAuditReceipt: "audit"}[item.kind]
		if !strings.EqualFold(r.Role, expectedRole) {
			return fmt.Errorf("knowledge: %s evidence role mismatch", item.kind)
		}
		if r.Digest != EvidenceDigest(r) {
			return fmt.Errorf("knowledge: %s evidence digest mismatch", item.kind)
		}
		if err := validateSources(r.Sources); err != nil {
			return fmt.Errorf("knowledge: %s evidence sources: %w", item.kind, err)
		}
		if err := validateDependencies(r.Dependencies); err != nil {
			return fmt.Errorf("knowledge: %s evidence dependencies: %w", item.kind, err)
		}
	}
	return nil
}

// EvidenceDigest is the canonical digest a workflow resolver must stamp on a
// receipt before returning it. It binds scope, point, tree, model and all
// source/dependency observations.
func EvidenceDigest(receipt EvidenceReceipt) string {
	wire := struct {
		ID, Kind, PointID                                                                 string
		Scope                                                                             Scope
		TreeDigest, Model, RequestedModel, RequestedEffort, ObservedEffort, Backend, Role string
		Complete, Approved                                                                bool
		Sources                                                                           []Source
		Dependencies                                                                      []Dependency
		KnowledgeDigest                                                                   string
	}{receipt.ID, string(receipt.Kind), receipt.PointID, receipt.Scope, receipt.TreeDigest, receipt.Model, receipt.RequestedModel, receipt.RequestedEffort, receipt.ObservedEffort, receipt.Backend, receipt.Role, receipt.Complete, receipt.Approved, receipt.Sources, receipt.Dependencies, receipt.KnowledgeDigest}
	raw, _ := json.Marshal(wire)
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func insertEvidenceSnapshot(ctx context.Context, tx *sql.Tx, receipt EvidenceReceipt) error {
	sources, err := json.Marshal(receipt.Sources)
	if err != nil {
		return err
	}
	dependencies, err := json.Marshal(receipt.Dependencies)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO knowledge_evidence(project_id,repository_id,point_id,kind,receipt_id,digest,tree_digest,model,requested_model,requested_effort,observed_effort,backend,role,knowledge_digest,complete,approved,sources_json,dependencies_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, receipt.Scope.ProjectID, receipt.Scope.RepositoryID, receipt.PointID, receipt.Kind, receipt.ID, receipt.Digest, receipt.TreeDigest, receipt.Model, receipt.RequestedModel, receipt.RequestedEffort, receipt.ObservedEffort, receipt.Backend, receipt.Role, receipt.KnowledgeDigest, boolInt(receipt.Complete), boolInt(receipt.Approved), sources, dependencies, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// Query is part of ATENEA's public orchestration contract.
func (s *Store) Query(ctx context.Context, query Query) ([]Entry, error) {
	if err := validateScope(query.Scope); err != nil {
		return nil, err
	}
	for _, kind := range query.Kinds {
		if !validKind(kind) {
			return nil, fmt.Errorf("knowledge: invalid query kind %q", kind)
		}
	}
	for _, status := range query.Statuses {
		if status != Candidate && status != Verified && status != Stale && status != Superseded {
			return nil, fmt.Errorf("knowledge: invalid query status %q", status)
		}
	}
	if err := authorizeScope(query.Scope, query.Permission, query.Permission.Read); err != nil {
		return nil, err
	}
	args := []any{query.Scope.ProjectID, query.Scope.RepositoryID}
	sqlText := selectEntrySQL + " WHERE project_id=? AND repository_id=?"
	if len(query.Kinds) > 0 {
		sqlText += " AND kind IN (" + placeholders(len(query.Kinds)) + ")"
		for _, kind := range query.Kinds {
			args = append(args, kind)
		}
	}
	if len(query.Statuses) > 0 {
		sqlText += " AND status IN (" + placeholders(len(query.Statuses)) + ")"
		for _, status := range query.Statuses {
			args = append(args, status)
		}
	}
	sqlText += " ORDER BY id"
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []Entry{}
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		if err := authorizeEntry(entry, query.Permission, true); err == nil {
			result = append(result, entry)
		}
	}
	return result, rows.Err()
}

// MarkStale is part of ATENEA's public orchestration contract.
func (s *Store) MarkStale(ctx context.Context, id string, permission Permission) error {
	return s.transition(ctx, id, permission, "stale", "marked_stale")
}

// Supersede is part of ATENEA's public orchestration contract.
func (s *Store) Supersede(ctx context.Context, scope Scope, id, replacementID string, permission Permission) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if strings.TrimSpace(replacementID) == "" {
		return errors.New("knowledge: replacement id is required")
	}
	return s.withBusyRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		old, err := scanEntry(tx.QueryRowContext(ctx, selectEntrySQL+" WHERE project_id=? AND repository_id=? AND id=?", scope.ProjectID, scope.RepositoryID, id))
		if err != nil {
			return err
		}
		newEntry, err := scanEntry(tx.QueryRowContext(ctx, selectEntrySQL+" WHERE project_id=? AND repository_id=? AND id=?", scope.ProjectID, scope.RepositoryID, replacementID))
		if err != nil {
			return err
		}
		if err := authorizeEntry(old, permission, permission.Write); err != nil {
			return err
		}
		if old.Scope != scope || old.Scope != newEntry.Scope {
			return errors.New("knowledge: superseded entries must share scope")
		}
		if old.ID == newEntry.ID {
			return errors.New("knowledge: replacement must be a different entry")
		}
		if old.Status != Verified && old.Status != Stale {
			return fmt.Errorf("knowledge: only verified or stale entries can be superseded, got %s", old.Status)
		}
		if newEntry.Status != Verified {
			return fmt.Errorf("knowledge: replacement must be verified, got %s", newEntry.Status)
		}
		if old.Kind != newEntry.Kind || old.Visibility != newEntry.Visibility {
			return errors.New("knowledge: replacement kind and visibility must match")
		}
		if err := authorizeEntry(newEntry, permission, permission.Write); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE knowledge_entries SET status='superseded',updated_at=? WHERE project_id=? AND repository_id=? AND id=? AND status IN ('verified','stale')`, time.Now().UTC().Format(time.RFC3339Nano), old.Scope.ProjectID, old.Scope.RepositoryID, id)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return errors.New("knowledge: supersede compare-and-swap failed")
		}
		if err := insertEvent(ctx, tx, old.Scope, id, "superseded", map[string]string{"replacement_id": replacementID}); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// Revalidate checks scope/permission before invoking probe. A changed
// dependency marks the entry stale; a failed or partial probe changes nothing.
func (s *Store) Revalidate(ctx context.Context, id string, permission Permission, probe func(context.Context, Entry) (ProbeResult, error)) error {
	if probe == nil {
		return errors.New("knowledge: revalidation probe is required")
	}
	entry, err := s.get(ctx, id, permission)
	if err != nil {
		return err
	}
	if err := authorizeEntry(entry, permission, permission.Read); err != nil {
		return err
	}
	result, err := probe(ctx, entry)
	if err != nil {
		return err
	}
	if !result.Complete || !result.Success {
		return errors.New("knowledge: partial or failed revalidation does not change knowledge")
	}
	if err := validateSources(result.Sources); err != nil {
		return err
	}
	if err := validateDependencies(result.Dependencies); err != nil {
		return err
	}
	if !freshObservation(result.Dependencies, time.Now().UTC()) {
		return errors.New("knowledge: revalidation observation is expired or not fresh")
	}
	if dependenciesEqual(entry.Dependencies, result.Dependencies) && (len(result.Sources) == 0 || sourcesEqual(entry.Sources, result.Sources)) {
		return nil
	}
	// The read permission authorized the probe. The resulting stale marker is
	// part of that same guarded revalidation transaction and must not require a
	// second, unrelated caller grant.
	return s.transitionExpected(ctx, id, Permission{SubjectID: permission.SubjectID, ProjectID: entry.Scope.ProjectID, RepositoryID: entry.Scope.RepositoryID, Write: true, ProjectMember: permission.ProjectMember}, "stale", "marked_stale", entry.UpdatedAt)
}

// refreshVerified is the read-path variant of revalidation. A context probe
// that completed successfully may refresh an expired observation in place;
// partial or failed observations never reach this method. The status remains
// verified and the update is guarded by the version read before the probe.
func (s *Store) refreshVerified(ctx context.Context, entry Entry, result ProbeResult, permission Permission) (Entry, error) {
	if err := authorizeEntry(entry, permission, permission.Read); err != nil {
		return Entry{}, err
	}
	if err := validateSources(result.Sources); err != nil {
		return Entry{}, err
	}
	if err := validateDependencies(result.Dependencies); err != nil {
		return Entry{}, err
	}
	returnEntry := entry
	returnEntry.Sources, returnEntry.Dependencies = result.Sources, result.Dependencies
	returnEntry.UpdatedAt = time.Now().UTC()
	// Only observation time, freshness and TTL may be renewed in place. A
	// changed source, provider, generation or snapshot changes the knowledge
	// digest and therefore requires a new candidate and acceptance chain.
	if KnowledgeDigest(returnEntry) != entry.KnowledgeDigest {
		if err := s.transitionExpected(ctx, entry.ID, Permission{SubjectID: permission.SubjectID, ProjectID: entry.Scope.ProjectID, RepositoryID: entry.Scope.RepositoryID, Write: true, ProjectMember: permission.ProjectMember}, "stale", "marked_stale", entry.UpdatedAt); err != nil {
			return Entry{}, err
		}
		return Entry{}, errors.New("knowledge: semantic dependency changed; entry marked stale")
	}
	return s.refreshVerifiedTx(ctx, entry, returnEntry, permission)
}

func (s *Store) refreshVerifiedTx(ctx context.Context, before, after Entry, permission Permission) (Entry, error) {
	sources, _ := json.Marshal(after.Sources)
	dependencies, _ := json.Marshal(after.Dependencies)
	err := s.withBusyRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := authorizeEntry(before, permission, true); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE knowledge_entries SET sources_json=?,dependencies_json=?,updated_at=? WHERE project_id=? AND repository_id=? AND id=? AND status='verified' AND updated_at=?`, sources, dependencies, after.UpdatedAt.Format(time.RFC3339Nano), before.Scope.ProjectID, before.Scope.RepositoryID, before.ID, before.UpdatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return errors.New("knowledge: verified refresh compare-and-swap failed")
		}
		if err := insertEvent(ctx, tx, before.Scope, before.ID, "revalidated", map[string]any{"sources": after.Sources, "dependencies": after.Dependencies}); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return Entry{}, err
	}
	after.CreatedAt = before.CreatedAt
	return after, nil
}

// Events is part of ATENEA's public orchestration contract.
func (s *Store) Events(ctx context.Context, scope Scope, permission Permission, limit int) ([]Event, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	if err := authorizeScope(scope, permission, permission.Read); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("knowledge: event limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.sequence,e.entry_id,e.action,e.payload_json,e.created_at,k.owner_id,k.visibility FROM knowledge_events e JOIN knowledge_entries k ON k.project_id=e.project_id AND k.repository_id=e.repository_id AND k.id=e.entry_id WHERE e.project_id=? AND e.repository_id=? ORDER BY e.sequence LIMIT ?`, scope.ProjectID, scope.RepositoryID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []Event{}
	for rows.Next() {
		var event Event
		var created, owner string
		var visibility Visibility
		if err := rows.Scan(&event.Sequence, &event.EntryID, &event.Action, &event.Payload, &created, &owner, &visibility); err != nil {
			return nil, err
		}
		if err := authorizeEntry(Entry{Scope: scope, OwnerID: owner, Visibility: visibility}, permission, true); err != nil {
			continue
		}
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

// Backup is part of ATENEA's public orchestration contract.
func (s *Store) Backup(ctx context.Context, destination string) error {
	if strings.TrimSpace(destination) == "" {
		return errors.New("knowledge: backup destination is required")
	}
	destination, err := secureDestinationPath(destination)
	if err != nil {
		return err
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("knowledge: backup destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	parent := filepath.Dir(destination)
	return s.withBusyRetry(ctx, func() error {
		tmp, err := os.CreateTemp(parent, ".knowledge-backup-*")
		if err != nil {
			return err
		}
		tmpPath := tmp.Name()
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpPath)
			return err
		}
		_ = os.Remove(tmpPath)
		defer func() { _ = os.Remove(tmpPath) }()
		if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", tmpPath); err != nil {
			return err
		}
		if err := verifySQLiteIntegrity(tmpPath); err != nil {
			return err
		}
		if err := secureDatabaseFiles(tmpPath); err != nil {
			return err
		}
		if err := os.Rename(tmpPath, destination); err != nil {
			return err
		}
		if err := secureDatabaseFiles(destination); err != nil {
			return err
		}
		return syncDirectory(parent)
	})
}

// Restore is part of ATENEA's public orchestration contract.
func Restore(ctx context.Context, source, destination string) error {
	source, err := secureExistingDatabasePath(source)
	if err != nil {
		return err
	}
	dsn, err := sqliteDSN(source, true)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("knowledge: open read-only backup source: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := verifySQLiteIntegrityDB(ctx, db); err != nil {
		return err
	}
	destination, err = secureDestinationPath(destination)
	if err != nil {
		return err
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("knowledge: restore destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".knowledge-restore-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath)
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", tmpPath); err != nil {
		return err
	}
	if err := verifySQLiteIntegrity(tmpPath); err != nil {
		return err
	}
	if err := secureDatabaseFiles(tmpPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return err
	}
	if err := secureDatabaseFiles(destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

const selectEntrySQL = `SELECT project_id,repository_id,id,owner_id,kind,status,title,body,visibility,sources_json,dependencies_json,knowledge_digest,created_at,updated_at FROM knowledge_entries`

type rowScanner interface{ Scan(...any) error }

func scanEntry(scanner rowScanner) (Entry, error) {
	var entry Entry
	var scopeProject, scopeRepo, kind, status, visibility, sources, dependencies, created, updated string
	if err := scanner.Scan(&scopeProject, &scopeRepo, &entry.ID, &entry.OwnerID, &kind, &status, &entry.Title, &entry.Body, &visibility, &sources, &dependencies, &entry.KnowledgeDigest, &created, &updated); err != nil {
		return Entry{}, err
	}
	entry.Scope = Scope{ProjectID: scopeProject, RepositoryID: scopeRepo}
	entry.Kind = Kind(kind)
	entry.Status = Status(status)
	entry.Visibility = Visibility(visibility)
	if err := json.Unmarshal([]byte(sources), &entry.Sources); err != nil {
		return Entry{}, err
	}
	if err := json.Unmarshal([]byte(dependencies), &entry.Dependencies); err != nil {
		return Entry{}, err
	}
	var err error
	entry.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Entry{}, err
	}
	entry.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return entry, err
}

func insertEntry(ctx context.Context, tx *sql.Tx, entry Entry) error {
	sources, _ := json.Marshal(entry.Sources)
	dependencies, _ := json.Marshal(entry.Dependencies)
	_, err := tx.ExecContext(ctx, `INSERT INTO knowledge_entries(project_id,repository_id,id,owner_id,kind,status,title,body,visibility,sources_json,dependencies_json,knowledge_digest,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, entry.Scope.ProjectID, entry.Scope.RepositoryID, entry.ID, entry.OwnerID, entry.Kind, entry.Status, entry.Title, entry.Body, entry.Visibility, sources, dependencies, entry.KnowledgeDigest, entry.CreatedAt.Format(time.RFC3339Nano), entry.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func insertEvent(ctx context.Context, tx *sql.Tx, scope Scope, id, action string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO knowledge_events(project_id,repository_id,entry_id,action,payload_json,created_at) VALUES(?,?,?,?,?,?)`, scope.ProjectID, scope.RepositoryID, id, action, data, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) get(ctx context.Context, id string, permission Permission) (Entry, error) {
	return scanEntry(s.db.QueryRowContext(ctx, selectEntrySQL+" WHERE project_id=? AND repository_id=? AND id=?", permission.ProjectID, permission.RepositoryID, id))
}

func (s *Store) transition(ctx context.Context, id string, permission Permission, status, action string) error {
	return s.transitionExpected(ctx, id, permission, status, action, time.Time{})
}

func (s *Store) transitionExpected(ctx context.Context, id string, permission Permission, status, action string, expected time.Time) error {
	return s.withBusyRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		entry, err := scanEntry(tx.QueryRowContext(ctx, selectEntrySQL+" WHERE project_id=? AND repository_id=? AND id=?", permission.ProjectID, permission.RepositoryID, id))
		if err != nil {
			return err
		}
		if err := authorizeEntry(entry, permission, permission.Write); err != nil {
			return err
		}
		if entry.Status == Superseded {
			return errors.New("knowledge: superseded entries are terminal")
		}
		if string(entry.Status) == status {
			return tx.Commit()
		}
		if status != string(Stale) {
			return fmt.Errorf("knowledge: unsupported transition to %s", status)
		}
		query := `UPDATE knowledge_entries SET status=?,updated_at=? WHERE project_id=? AND repository_id=? AND id=? AND status!='superseded'`
		args := []any{status, time.Now().UTC().Format(time.RFC3339Nano), entry.Scope.ProjectID, entry.Scope.RepositoryID, id}
		if !expected.IsZero() {
			query += " AND updated_at=?"
			args = append(args, expected.Format(time.RFC3339Nano))
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return errors.New("knowledge: state compare-and-swap failed")
		}
		if err := insertEvent(ctx, tx, entry.Scope, id, action, map[string]string{"status": status}); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (s *Store) withBusyRetry(ctx context.Context, operation func() error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		if err = operation(); err == nil {
			return nil
		}
		if !isSQLiteBusy(err) {
			return err
		}
		delay := min(10*time.Millisecond<<attempt, 250*time.Millisecond)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return err
}

func secureDatabasePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("knowledge: database path must not be a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := validateParentChain(filepath.Dir(abs)); err != nil {
		return "", err
	}
	parent, err := canonicalParent(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

func canonicalParent(parent string) (string, error) {
	parent = filepath.Clean(parent)
	if physical, err := filepath.EvalSymlinks(parent); err == nil {
		physical = filepath.Clean(physical)
		return physical, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	base := filepath.Base(parent)
	ancestor, err := canonicalParent(filepath.Dir(parent))
	if err != nil {
		return "", err
	}
	return filepath.Join(ancestor, base), nil
}

func validateParentChain(parent string) error {
	parent = filepath.Clean(parent)
	for current := string(filepath.Separator); ; {
		for _, part := range strings.Split(strings.TrimPrefix(parent, current), string(filepath.Separator)) {
			if part == "" {
				continue
			}
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 && current != "/var" && current != "/tmp" {
				return fmt.Errorf("knowledge: database parent symlink is not allowed: %s", current)
			}
		}
		return nil
	}
}

func secureExistingDatabasePath(path string) (string, error) {
	abs, err := secureDatabasePath(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func secureDestinationPath(path string) (string, error) {
	abs, err := secureDatabasePath(path)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("knowledge: destination must not be a symlink")
	}
	return abs, nil
}

func secureDatabaseFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			if err := os.Chmod(candidate, 0o600); err != nil {
				return err
			}
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func verifySQLiteIntegrity(path string) error {
	dsn, err := sqliteDSN(path, true)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return verifySQLiteIntegrityDB(context.Background(), db)
}

// sqliteDSN is the single boundary between filesystem paths and SQLite URI
// syntax. URL.Path escapes '#', '?', spaces and other URI delimiters while
// preserving path separators, so every caller addresses the same database.
func sqliteDSN(path string, readOnly bool) (string, error) {
	if path == ":memory:" {
		return path, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	u := &url.URL{Scheme: "file", Path: abs}
	if readOnly {
		u.RawQuery = "mode=ro"
	}
	return u.String(), nil
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code()
		if code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED || code == sqlite3.SQLITE_LOCKED|(1<<8) {
			return true
		}
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "busy") || strings.Contains(text, "locked")
}

func verifySQLiteIntegrityDB(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("knowledge: integrity check: %w", err)
	}
	if !strings.EqualFold(result, "ok") {
		return fmt.Errorf("knowledge: integrity check failed: %s", result)
	}
	var version string
	if err := db.QueryRowContext(ctx, `SELECT value FROM knowledge_meta WHERE key='schema_version'`).Scan(&version); err != nil {
		return fmt.Errorf("knowledge: schema version check: %w", err)
	}
	if version != fmt.Sprint(schemaVersion) {
		return fmt.Errorf("knowledge: incompatible schema version %s", version)
	}
	var triggers int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name IN ('knowledge_events_no_update','knowledge_events_no_delete','knowledge_evidence_no_update','knowledge_evidence_no_delete')`).Scan(&triggers); err != nil {
		return err
	}
	if triggers != 4 {
		return errors.New("knowledge: required append-only triggers are missing")
	}
	return nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

func validateEntry(entry Entry) error {
	if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Title) == "" {
		return errors.New("knowledge: entry id and title are required")
	}
	if err := validateScope(entry.Scope); err != nil {
		return err
	}
	if strings.TrimSpace(entry.OwnerID) == "" {
		return errors.New("knowledge: owner is required")
	}
	if !validKind(entry.Kind) {
		return fmt.Errorf("knowledge: invalid kind %q", entry.Kind)
	}
	if entry.Status != "" && entry.Status != Candidate {
		return errors.New("knowledge: new entries must be candidates")
	}
	if entry.Visibility != Private && entry.Visibility != Project {
		return fmt.Errorf("knowledge: invalid visibility %q", entry.Visibility)
	}
	if len(entry.Sources) == 0 {
		return errors.New("knowledge: at least one source is required")
	}
	if err := validateSources(entry.Sources); err != nil {
		return err
	}
	return validateDependencies(entry.Dependencies)
}

// KnowledgeDigest is the canonical identity of candidate content. Status and
// timestamps are deliberately excluded: promotion changes status, while the
// workflow evidence must remain bound to the same knowledge payload.
func KnowledgeDigest(entry Entry) string {
	sources := append([]Source(nil), entry.Sources...)
	type digestDependency struct {
		Source     Source
		Provider   ProviderIdentity
		Generation int64
		Snapshot   string
	}
	deps := make([]digestDependency, 0, len(entry.Dependencies))
	for _, dependency := range entry.Dependencies {
		// Freshness, CheckedAt and TTL are observations of the same semantic
		// dependency, not part of the knowledge payload. Revalidation may
		// refresh them without changing what the candidate says.
		deps = append(deps, digestDependency{
			Source: dependency.Source, Provider: dependency.Provider,
			Generation: dependency.Generation, Snapshot: dependency.Snapshot,
		})
	}
	slices.SortFunc(sources, func(a, b Source) int {
		if c := strings.Compare(a.ID, b.ID); c != 0 {
			return c
		}
		return strings.Compare(a.Digest, b.Digest)
	})
	slices.SortFunc(deps, func(a, b digestDependency) int {
		if c := strings.Compare(a.Source.ID, b.Source.ID); c != 0 {
			return c
		}
		if c := strings.Compare(a.Source.Digest, b.Source.Digest); c != 0 {
			return c
		}
		if c := strings.Compare(a.Snapshot, b.Snapshot); c != 0 {
			return c
		}
		return strings.Compare(a.Provider.Instance, b.Provider.Instance)
	})
	wire := struct {
		Scope        Scope
		ID, OwnerID  string
		Kind         Kind
		Title, Body  string
		Visibility   Visibility
		Sources      []Source
		Dependencies []digestDependency
	}{entry.Scope, entry.ID, entry.OwnerID, entry.Kind, entry.Title, entry.Body, entry.Visibility, sources, deps}
	raw, _ := json.Marshal(wire)
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validateSources(sources []Source) error {
	if len(sources) == 0 {
		return errors.New("knowledge: at least one source is required")
	}
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if strings.TrimSpace(source.ID) == "" || strings.TrimSpace(source.Kind) == "" || strings.TrimSpace(source.Locator) == "" || strings.TrimSpace(source.Digest) == "" {
			return errors.New("knowledge: source kind, locator, identity and digest are required")
		}
		if _, ok := seen[source.ID]; ok {
			return fmt.Errorf("knowledge: duplicate source %q", source.ID)
		}
		seen[source.ID] = struct{}{}
	}
	return nil
}

func validateDependencies(dependencies []Dependency) error {
	seen := make(map[string]struct{}, len(dependencies))
	for _, dependency := range dependencies {
		if err := validateSources([]Source{dependency.Source}); err != nil {
			return fmt.Errorf("knowledge: dependency: %w", err)
		}
		if _, ok := seen[dependency.Source.ID]; ok {
			return fmt.Errorf("knowledge: duplicate dependency %q", dependency.Source.ID)
		}
		seen[dependency.Source.ID] = struct{}{}
		providerEmpty := dependency.Provider == (ProviderIdentity{})
		if providerEmpty && (dependency.Generation != 0 || dependency.Snapshot != "" || dependency.Freshness != "") {
			return errors.New("knowledge: dependency provider identity is required")
		}
		if !providerEmpty {
			if strings.TrimSpace(dependency.Provider.Name) == "" || strings.TrimSpace(dependency.Provider.Version) == "" || strings.TrimSpace(dependency.Provider.Instance) == "" || strings.TrimSpace(dependency.Provider.ConfigDigest) == "" {
				return errors.New("knowledge: complete provider identity is required")
			}
			if dependency.Generation < 1 || strings.TrimSpace(dependency.Snapshot) == "" || dependency.CheckedAt.IsZero() || dependency.TTLSeconds <= 0 {
				return errors.New("knowledge: provider dependency requires generation and snapshot")
			}
		}
		if dependency.Freshness != "" && dependency.Freshness != "fresh" && dependency.Freshness != "stale" {
			return errors.New("knowledge: invalid dependency freshness")
		}
	}
	return nil
}

func validateScope(scope Scope) error {
	if strings.TrimSpace(scope.ProjectID) == "" || strings.TrimSpace(scope.RepositoryID) == "" {
		return errors.New("knowledge: project and repository scope are required")
	}
	return nil
}
func authorizeScope(scope Scope, permission Permission, needed bool) error {
	if !needed {
		return errors.New("knowledge: permission denied")
	}
	if permission.ProjectID != scope.ProjectID || (permission.RepositoryID != scope.RepositoryID && permission.RepositoryID != "*") {
		return errors.New("knowledge: scope permission denied")
	}
	return nil
}

func authorizeEntry(entry Entry, permission Permission, needed bool) error {
	if err := authorizeScope(entry.Scope, permission, needed); err != nil {
		return err
	}
	if !needed {
		return errors.New("knowledge: permission denied")
	}
	if entry.Visibility == Private && (permission.SubjectID == "" || permission.SubjectID != entry.OwnerID) {
		return errors.New("knowledge: private entry permission denied")
	}
	if entry.Visibility == Project && !permission.ProjectMember && permission.SubjectID != entry.OwnerID {
		return errors.New("knowledge: project membership required")
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func validKind(kind Kind) bool {
	return kind == Decision || kind == Convention || kind == Solution || kind == Hypothesis || kind == Fact
}
func placeholders(count int) string {
	values := make([]string, count)
	for i := range values {
		values[i] = "?"
	}
	return strings.Join(values, ",")
}
func entriesEqual(a, b Entry) bool {
	return a.ID == b.ID && a.Scope == b.Scope && a.OwnerID == b.OwnerID && a.Kind == b.Kind && a.Status == Candidate && b.Status == Candidate && a.Title == b.Title && a.Body == b.Body && a.Visibility == b.Visibility && a.KnowledgeDigest == b.KnowledgeDigest && dependenciesEqual(a.Dependencies, b.Dependencies) && sourcesEqual(a.Sources, b.Sources)
}
func sourcesEqual(a, b []Source) bool {
	aa := append([]Source(nil), a...)
	bb := append([]Source(nil), b...)
	slices.SortFunc(aa, func(x, y Source) int { return strings.Compare(x.ID, y.ID) })
	slices.SortFunc(bb, func(x, y Source) int { return strings.Compare(x.ID, y.ID) })
	return string(mustJSON(aa)) == string(mustJSON(bb))
}
func dependenciesEqual(a, b []Dependency) bool {
	aa := append([]Dependency(nil), a...)
	bb := append([]Dependency(nil), b...)
	slices.SortFunc(aa, func(x, y Dependency) int { return strings.Compare(x.Source.ID, y.Source.ID) })
	slices.SortFunc(bb, func(x, y Dependency) int { return strings.Compare(x.Source.ID, y.Source.ID) })
	return string(mustJSON(aa)) == string(mustJSON(bb))
}
func mustJSON(value any) []byte { data, _ := json.Marshal(value); return data }
