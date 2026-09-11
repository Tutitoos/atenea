package remotedevice

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// SchemaVersion is incremented whenever the durable registry contract changes.
const SchemaVersion = 1

const schemaSQL = `
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL,
    CHECK (length(key) BETWEEN 1 AND 128),
    CHECK (length(value) BETWEEN 1 AND 256)
);
CREATE TABLE IF NOT EXISTS enrollments (
    id TEXT PRIMARY KEY NOT NULL,
    device_id TEXT NOT NULL,
    name TEXT NOT NULL,
    platform TEXT NOT NULL,
    architecture TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    token_digest TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    issue_key TEXT NOT NULL DEFAULT '',
    challenge_id TEXT UNIQUE,
    actor_id TEXT NOT NULL,
    policy_id TEXT NOT NULL,
    CHECK (state IN ('pending','challenged','expired','failed','active')),
    CHECK (length(id) BETWEEN 1 AND 128),
    CHECK (length(device_id) BETWEEN 1 AND 128),
    CHECK (length(name) BETWEEN 1 AND 256),
    CHECK (platform IN ('windows','macos','linux')),
    CHECK (architecture IN ('x86_64','arm64','armv7')),
    CHECK (length(token_digest)=64 AND token_digest=lower(token_digest) AND token_digest NOT GLOB '*[^0-9a-f]*'),
    CHECK (expires_at > created_at),
    CHECK (updated_at >= created_at),
    CHECK (length(issue_key) <= 128),
    CHECK (length(actor_id) BETWEEN 1 AND 128),
    CHECK (length(policy_id) BETWEEN 1 AND 128)
);
CREATE TABLE IF NOT EXISTS challenges (
    id TEXT PRIMARY KEY NOT NULL,
    enrollment_id TEXT NOT NULL UNIQUE REFERENCES enrollments(id) ON DELETE RESTRICT,
    device_id TEXT NOT NULL,
    name TEXT NOT NULL,
    platform TEXT NOT NULL,
    architecture TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    nonce_digest TEXT NOT NULL,
    context_digest TEXT NOT NULL,
    original_context_digest TEXT NOT NULL,
    public_key_spki BLOB NOT NULL,
    public_key_digest TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    policy_id TEXT NOT NULL,
    issued_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    CHECK (state IN ('pending','consumed','failed','expired')),
    CHECK (length(id) BETWEEN 1 AND 128),
    CHECK (length(device_id) BETWEEN 1 AND 128),
    CHECK (length(name) BETWEEN 1 AND 256),
    CHECK (platform IN ('windows','macos','linux')),
    CHECK (architecture IN ('x86_64','arm64','armv7')),
    CHECK (length(nonce_digest)=64 AND nonce_digest=lower(nonce_digest) AND nonce_digest NOT GLOB '*[^0-9a-f]*'),
    CHECK (length(context_digest)=64 AND context_digest=lower(context_digest) AND context_digest NOT GLOB '*[^0-9a-f]*'),
    CHECK (length(original_context_digest)=64 AND original_context_digest=lower(original_context_digest) AND original_context_digest NOT GLOB '*[^0-9a-f]*'),
    CHECK (length(public_key_digest)=64 AND public_key_digest=lower(public_key_digest) AND public_key_digest NOT GLOB '*[^0-9a-f]*'),
    CHECK (length(public_key_spki) BETWEEN 1 AND 1024),
    CHECK (length(actor_id) BETWEEN 1 AND 128),
    CHECK (length(policy_id) BETWEEN 1 AND 128),
    CHECK (expires_at > issued_at)
);
CREATE TABLE IF NOT EXISTS devices (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    platform TEXT NOT NULL,
    architecture TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'active',
    fence INTEGER NOT NULL DEFAULT 0,
    active_certificate_id TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK (state IN ('active','revoked')),
    CHECK (fence >= 0),
    CHECK (active_certificate_id IS NULL OR length(active_certificate_id) BETWEEN 1 AND 128),
    CHECK (length(id) BETWEEN 1 AND 128),
    CHECK (length(name) BETWEEN 1 AND 256),
    CHECK (platform IN ('windows','macos','linux')),
    CHECK (architecture IN ('x86_64','arm64','armv7'))
);
CREATE TABLE IF NOT EXISTS certificates (
    id TEXT PRIMARY KEY NOT NULL,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    issuer_id TEXT NOT NULL,
    serial TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    public_key_digest TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'active',
    not_before INTEGER NOT NULL,
    not_after INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    CHECK (state IN ('active','revoked','superseded')),
    CHECK (length(id) BETWEEN 1 AND 128),
    CHECK (length(issuer_id) BETWEEN 1 AND 256),
    CHECK (length(serial) BETWEEN 1 AND 256),
    CHECK (length(fingerprint) BETWEEN 1 AND 256),
    CHECK (length(public_key_digest)=64 AND public_key_digest=lower(public_key_digest) AND public_key_digest NOT GLOB '*[^0-9a-f]*'),
    CHECK (not_after > not_before)
);
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY NOT NULL,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    state TEXT NOT NULL DEFAULT 'active',
    fence INTEGER NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    closed_at INTEGER,
    CHECK (state IN ('active','closed')),
    CHECK (fence >= 0),
    CHECK (length(reason) <= 64),
    CHECK ((state='active' AND reason='') OR (state='closed' AND reason IN ('revoked','heartbeat_timeout','administrator','protocol_error'))),
    CHECK ((state='active' AND closed_at IS NULL) OR (state='closed' AND closed_at IS NOT NULL))
);
CREATE TABLE IF NOT EXISTS revocations (
    id TEXT PRIMARY KEY NOT NULL,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    actor_id TEXT NOT NULL,
    policy_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'applied',
    CHECK (length(actor_id) BETWEEN 1 AND 128),
    CHECK (length(policy_id) BETWEEN 1 AND 128),
    CHECK (length(reason) BETWEEN 1 AND 64),
    CHECK (reason IN ('administrator','certificate','policy','unknown_state')),
    CHECK (state='applied')
);
CREATE TABLE IF NOT EXISTS closure_intents (
    id TEXT PRIMARY KEY NOT NULL,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE RESTRICT,
    revocation_id TEXT NOT NULL REFERENCES revocations(id) ON DELETE RESTRICT,
    fence INTEGER NOT NULL,
    reason TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    created_at INTEGER NOT NULL,
    applied_at INTEGER,
    CHECK (state IN ('pending','applied')),
    CHECK (fence >= 0),
    CHECK (length(reason) BETWEEN 1 AND 64),
    CHECK (reason IN ('administrator','certificate','policy','unknown_state')),
    CHECK ((state='pending' AND applied_at IS NULL) OR (state='applied' AND applied_at IS NOT NULL)),
    UNIQUE(device_id, session_id, fence),
    UNIQUE(revocation_id, session_id)
);
CREATE TABLE IF NOT EXISTS audit_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT UNIQUE NOT NULL,
    event_kind TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    device_id TEXT,
    session_id TEXT,
    enrollment_id TEXT,
    request_id TEXT NOT NULL DEFAULT '',
    actor_id TEXT NOT NULL,
    policy_id TEXT NOT NULL,
    outcome TEXT NOT NULL,
    reason TEXT NOT NULL,
    digest_ref TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    CHECK (event_kind IN ('enrollment.created','enrollment.failed','enrollment.expired','enrollment.consumed','challenge.issued','challenge.failed','challenge.expired','challenge.consumed','certificate.issued','certificate.renewed','certificate.revoked','device.activated','device.revoked','session.registered','session.closed','closure.requested','closure.applied')),
    CHECK (aggregate_type IN ('enrollment','challenge','device','session','certificate','closure_intent')),
    CHECK (outcome IN ('allow','deny','error')),
    CHECK (length(id) BETWEEN 1 AND 128),
    CHECK (length(aggregate_id) BETWEEN 1 AND 128),
    CHECK (length(request_id) <= 128),
    CHECK (length(actor_id) BETWEEN 1 AND 128),
    CHECK (length(policy_id) BETWEEN 1 AND 128),
    CHECK (length(reason) BETWEEN 1 AND 64),
    CHECK (reason IN ('created','issued','expired','failed','binding_mismatch','proof_invalid','proof_verified','enrollment_completed','enrollment_consumed','certificate_issued','certificate_renewed','certificate_revoked','closure_requested','administrator','certificate','policy','unknown_state','heartbeat_timeout','protocol_error','registered','revoked')),
    CHECK (digest_ref='' OR (length(digest_ref)=64 AND digest_ref=lower(digest_ref) AND digest_ref NOT GLOB '*[^0-9a-f]*'))
);
CREATE UNIQUE INDEX IF NOT EXISTS certificates_one_active_per_device ON certificates(device_id) WHERE state='active';
CREATE UNIQUE INDEX IF NOT EXISTS enrollments_one_per_device ON enrollments(device_id);
CREATE INDEX IF NOT EXISTS enrollments_by_device ON enrollments(device_id);
CREATE INDEX IF NOT EXISTS challenges_by_state ON challenges(state);
CREATE INDEX IF NOT EXISTS sessions_by_device_state ON sessions(device_id, state);
CREATE INDEX IF NOT EXISTS closure_intents_pending ON closure_intents(state, created_at);
CREATE INDEX IF NOT EXISTS audit_by_aggregate ON audit_events(aggregate_type, aggregate_id, created_at);
CREATE TRIGGER IF NOT EXISTS audit_events_no_update BEFORE UPDATE ON audit_events BEGIN SELECT RAISE(ABORT, 'audit_events is append-only'); END;
CREATE TRIGGER IF NOT EXISTS audit_events_no_delete BEFORE DELETE ON audit_events BEGIN SELECT RAISE(ABORT, 'audit_events is append-only'); END;
CREATE TRIGGER IF NOT EXISTS revocations_no_update BEFORE UPDATE ON revocations BEGIN SELECT RAISE(ABORT, 'revocations are immutable'); END;
CREATE TRIGGER IF NOT EXISTS revocations_no_delete BEFORE DELETE ON revocations BEGIN SELECT RAISE(ABORT, 'revocations are immutable'); END;
CREATE TRIGGER IF NOT EXISTS enrollments_valid_transition BEFORE UPDATE OF state ON enrollments
WHEN NOT ((OLD.state=NEW.state) OR (OLD.state='pending' AND NEW.state IN ('challenged','expired','failed')) OR (OLD.state='challenged' AND NEW.state IN ('active','expired','failed')))
BEGIN SELECT RAISE(ABORT, 'invalid enrollment state transition'); END;
CREATE TRIGGER IF NOT EXISTS challenges_valid_transition BEFORE UPDATE OF state ON challenges
WHEN NOT ((OLD.state=NEW.state) OR (OLD.state='pending' AND NEW.state IN ('consumed','expired','failed')))
BEGIN SELECT RAISE(ABORT, 'invalid challenge state transition'); END;
CREATE TRIGGER IF NOT EXISTS certificates_valid_transition BEFORE UPDATE OF state ON certificates
WHEN NOT ((OLD.state=NEW.state) OR (OLD.state='active' AND NEW.state IN ('revoked','superseded')))
BEGIN SELECT RAISE(ABORT, 'invalid certificate state transition'); END;
`

func (s *Store) ensureSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("remote device: begin schema: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var hasMeta int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='meta'`).Scan(&hasMeta); err != nil {
		return fmt.Errorf("remote device: inspect schema: %w", err)
	}
	if hasMeta == 0 {
		if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
			return fmt.Errorf("remote device: create schema: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES ('schema_version',?)`, strconv.Itoa(SchemaVersion)); err != nil {
			return fmt.Errorf("remote device: write schema version: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
			return fmt.Errorf("remote device: write SQLite schema version: %w", err)
		}
	} else {
		var value string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&value); err != nil {
			return fmt.Errorf("remote device: schema version is missing: %w", err)
		}
		version, err := strconv.Atoi(value)
		if err != nil || version != SchemaVersion {
			return fmt.Errorf("remote device: unsupported schema version %q", value)
		}
	}
	var sqliteVersion int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&sqliteVersion); err != nil {
		return fmt.Errorf("remote device: read SQLite schema version: %w", err)
	}
	if sqliteVersion != SchemaVersion {
		return fmt.Errorf("remote device: unsupported SQLite schema version %d", sqliteVersion)
	}
	var foreignKeys int
	if err := tx.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("remote device: read foreign-key enforcement: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("remote device: foreign-key enforcement is disabled")
	}
	actual, err := readSchemaDefinitionsTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("remote device: read schema definitions: %w", err)
	}
	expected, err := referenceSchemaDefinitions(ctx)
	if err != nil {
		return fmt.Errorf("remote device: build reference schema: %w", err)
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("remote device: schema object set mismatch: got %d objects, want %d", len(actual), len(expected))
	}
	for key, definition := range expected {
		if actual[key] != definition {
			return fmt.Errorf("remote device: schema definition mismatch for %s", strings.ReplaceAll(key, "\x00", "."))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("remote device: commit schema: %w", err)
	}
	return nil
}

func readSchemaDefinitionsTx(ctx context.Context, tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' AND sql IS NOT NULL ORDER BY type,name`)
	if err != nil {
		return nil, err
	}
	return scanSchemaDefinitions(rows)
}

func readSchemaDefinitionsDB(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' AND sql IS NOT NULL ORDER BY type,name`)
	if err != nil {
		return nil, err
	}
	return scanSchemaDefinitions(rows)
}

func scanSchemaDefinitions(rows *sql.Rows) (map[string]string, error) {
	defer func() { _ = rows.Close() }()
	definitions := make(map[string]string)
	for rows.Next() {
		var objectType, name, definition string
		if err := rows.Scan(&objectType, &name, &definition); err != nil {
			return nil, err
		}
		definitions[objectType+"\x00"+name] = canonicalSchemaDefinition(definition)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return definitions, nil
}

func referenceSchemaDefinitions(ctx context.Context) (map[string]string, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return nil, err
	}
	return readSchemaDefinitionsDB(ctx, db)
}

func canonicalSchemaDefinition(definition string) string {
	return strings.Join(strings.Fields(definition), " ")
}
