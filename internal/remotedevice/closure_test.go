package remotedevice

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type closureFixture struct {
	store  *Store
	clock  *testClock
	device Device
	actor  Actor
	intent ClosureIntent
}

// beginCheckpointContext exposes the point at which database/sql starts
// BeginTx. ApplyClosureIntent performs all input validation before this
// callback, and the sqlite driver executes BEGIN IMMEDIATE after it. That
// gives the clock/lock test a deterministic hand-off without sleeping.
type beginCheckpointContext struct {
	context.Context
	begin chan<- struct{}
	once  sync.Once
}

func (c *beginCheckpointContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.begin) })
	return c.Context.Done()
}

func newClosureFixture(t *testing.T, now time.Time) closureFixture {
	t.Helper()
	clock := &testClock{now: now}
	actor := Actor{ID: "operator-1", PolicyID: "policy-1"}
	spki, private := testKeys(t)
	store := openTestStore(t, clock, &testIssuer{clock: clock}, bytes.Repeat([]byte{0x6a}, 64))
	enrollment := createEnrollment(t, store)
	challenge := issueChallenge(t, store, enrollment, spki)
	device, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signChallenge(challenge, enrollment, private)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterSession(context.Background(), RegisterSessionRequest{SessionID: "session-1", DeviceID: device.ID, Actor: actor, RequestID: "register-1"}); err != nil {
		t.Fatal(err)
	}
	revocation, err := store.RevokeDevice(context.Background(), RevokeDeviceRequest{DeviceID: device.ID, Actor: actor, Reason: RevocationReasonAdministrator, RequestID: "revoke-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(revocation.Intents) != 1 {
		t.Fatalf("revocation intents = %d, want one", len(revocation.Intents))
	}
	return closureFixture{store: store, clock: clock, device: device, actor: actor, intent: revocation.Intents[0]}
}

func (f closureFixture) request(requestID string) ApplyClosureIntentRequest {
	return ApplyClosureIntentRequest{EventID: f.intent.ID, DeviceID: f.device.ID, SessionID: f.intent.SessionID, Fence: f.intent.Fence, Actor: f.actor, RequestID: requestID}
}

func TestApplyClosureIntentClosesActiveSessionInAuditOrder(t *testing.T) {
	now := time.Date(2026, 9, 11, 21, 0, 0, 0, time.UTC)
	f := newClosureFixture(t, now)
	result, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Already || result.Intent.State != "applied" || !result.Intent.AppliedAt.Equal(now) {
		t.Fatalf("apply result = %+v", result)
	}
	if result.Session.State != "closed" || result.Session.Reason != SessionCloseReasonRevoked || !result.Session.ClosedAt.Equal(now) {
		t.Fatalf("closed session = %+v", result.Session)
	}
	// The admission fence remains the session's immutable ownership epoch;
	// req.Fence is the next device/revocation epoch and is checked as +1.
	if result.Session.Fence+1 != f.intent.Fence {
		t.Fatalf("session fence = %d, intent fence = %d", result.Session.Fence, f.intent.Fence)
	}
	audit, err := f.store.Audit(context.Background(), AuditFilter{DeviceID: f.device.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var sessionClosed, closureApplied []AuditEvent
	for _, event := range audit {
		switch event.EventKind {
		case EventSessionClosed:
			sessionClosed = append(sessionClosed, event)
		case EventClosureApplied:
			closureApplied = append(closureApplied, event)
		}
	}
	if len(sessionClosed) != 1 || len(closureApplied) != 1 {
		t.Fatalf("closure audits session=%d applied=%d", len(sessionClosed), len(closureApplied))
	}
	if sessionClosed[0].Sequence >= closureApplied[0].Sequence || sessionClosed[0].RequestID != "apply-1" || closureApplied[0].RequestID != "apply-1" {
		t.Fatalf("audit order/session=%+v applied=%+v", sessionClosed[0], closureApplied[0])
	}
	pending, err := f.store.PendingClosureIntents(context.Background(), ClosureIntentFilter{DeviceID: f.device.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending intents after apply = %+v", pending)
	}
}

func TestApplyClosureIntentPreservesAlreadyClosedSession(t *testing.T) {
	now := time.Date(2026, 9, 11, 21, 5, 0, 0, time.UTC)
	f := newClosureFixture(t, now)
	closedAt := now.Add(time.Minute)
	f.clock.Set(closedAt)
	original, err := f.store.CloseSession(context.Background(), CloseSessionRequest{SessionID: f.intent.SessionID, DeviceID: f.device.ID, Actor: f.actor, Reason: SessionCloseReasonAdministrator, RequestID: "close-1"})
	if err != nil {
		t.Fatal(err)
	}
	revokedAt := closedAt.Add(time.Minute)
	f.clock.Set(revokedAt)
	result, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-closed-1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Already || result.Session.State != "closed" || result.Session.Reason != original.Reason || !result.Session.ClosedAt.Equal(original.ClosedAt) {
		t.Fatalf("already-closed result = %+v, original = %+v", result.Session, original)
	}
	audit, err := f.store.Audit(context.Background(), AuditFilter{DeviceID: f.device.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var sessionClosed, closureApplied int
	for _, event := range audit {
		if event.SessionID != f.intent.SessionID {
			continue
		}
		if event.EventKind == EventSessionClosed {
			sessionClosed++
		}
		if event.EventKind == EventClosureApplied {
			closureApplied++
		}
	}
	if sessionClosed != 1 || closureApplied != 1 {
		t.Fatalf("already-closed audit counts session=%d applied=%d", sessionClosed, closureApplied)
	}
}

func TestApplyClosureIntentFreshRequestReplayIsStableAndSilent(t *testing.T) {
	now := time.Date(2026, 9, 11, 21, 10, 0, 0, time.UTC)
	f := newClosureFixture(t, now)
	first, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-first"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.store.Audit(context.Background(), AuditFilter{DeviceID: f.device.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Set(now.Add(time.Hour))
	second, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-fresh-replay"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := f.store.Audit(context.Background(), AuditFilter{DeviceID: f.device.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Already || len(after) != len(before) || !second.Intent.AppliedAt.Equal(first.Intent.AppliedAt) || !second.Session.ClosedAt.Equal(first.Session.ClosedAt) {
		t.Fatalf("replay first=%+v second=%+v audits=%d/%d", first, second, len(before), len(after))
	}
	for _, test := range []struct {
		name, suffix string
		actor        Actor
	}{
		{name: "actor id", suffix: "actor-id", actor: Actor{ID: "operator-other", PolicyID: f.actor.PolicyID}},
		{name: "policy id", suffix: "policy-id", actor: Actor{ID: f.actor.ID, PolicyID: "policy-other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			replay := f.request("apply-fresh-conflict-" + test.suffix)
			replay.Actor = test.actor
			if _, err := f.store.ApplyClosureIntent(context.Background(), replay); !errors.Is(err, ErrClosureIntentConflict) {
				t.Fatalf("error = %v, want ErrClosureIntentConflict", err)
			}
			current, err := f.store.Audit(context.Background(), AuditFilter{DeviceID: f.device.ID, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			if len(current) != len(after) {
				t.Fatalf("audit count after actor conflict = %d, want %d", len(current), len(after))
			}
		})
	}
}

func TestApplyClosureIntentReplaySurvivesRestart(t *testing.T) {
	now := time.Date(2026, 9, 11, 21, 12, 0, 0, time.UTC)
	f := newClosureFixture(t, now)
	first, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-before-restart"))
	if err != nil {
		t.Fatal(err)
	}
	path := f.store.path
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.clock.Set(now.Add(2 * time.Hour))
	reopened, err := Open(context.Background(), path, WithClock(f.clock.Now), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x6d}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second, err := reopened.ApplyClosureIntent(context.Background(), f.request("apply-after-restart"))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Already || !second.Intent.AppliedAt.Equal(first.Intent.AppliedAt) || !second.Session.ClosedAt.Equal(first.Session.ClosedAt) {
		t.Fatalf("restart replay first=%+v second=%+v", first, second)
	}
}

func TestApplyClosureIntentRejectsMalformedAndConflictingRequestsWithoutMutation(t *testing.T) {
	now := time.Date(2026, 9, 11, 21, 15, 0, 0, time.UTC)
	f := newClosureFixture(t, now)
	base := f.request("apply-valid")
	tests := []struct {
		name    string
		request ApplyClosureIntentRequest
		wantErr error
	}{
		{name: "unknown event", request: ApplyClosureIntentRequest{EventID: "missing-event", DeviceID: base.DeviceID, SessionID: base.SessionID, Fence: base.Fence, Actor: base.Actor, RequestID: "apply-unknown"}, wantErr: ErrClosureIntentNotFound},
		{name: "device binding", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.DeviceID = "device-other" }), wantErr: ErrClosureIntentConflict},
		{name: "session binding", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.SessionID = "session-other" }), wantErr: ErrClosureIntentConflict},
		{name: "fence binding", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.Fence++ }), wantErr: ErrClosureIntentConflict},
		{name: "actor binding", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.Actor.ID = "operator-other" }), wantErr: ErrClosureIntentConflict},
		{name: "zero fence", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.Fence = 0 }), wantErr: ErrInvalidInput},
		{name: "missing request id", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.RequestID = "" }), wantErr: ErrInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var sessionState, intentState string
			if err := f.store.db.QueryRow(`SELECT state FROM sessions WHERE id=?`, f.intent.SessionID).Scan(&sessionState); err != nil {
				t.Fatal(err)
			}
			if err := f.store.db.QueryRow(`SELECT state FROM closure_intents WHERE id=?`, f.intent.ID).Scan(&intentState); err != nil {
				t.Fatal(err)
			}
			auditBefore, err := f.store.Audit(context.Background(), AuditFilter{DeviceID: f.device.ID, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.store.ApplyClosureIntent(context.Background(), test.request)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			var sessionAfter, intentAfter string
			if err := f.store.db.QueryRow(`SELECT state FROM sessions WHERE id=?`, f.intent.SessionID).Scan(&sessionAfter); err != nil {
				t.Fatal(err)
			}
			if err := f.store.db.QueryRow(`SELECT state FROM closure_intents WHERE id=?`, f.intent.ID).Scan(&intentAfter); err != nil {
				t.Fatal(err)
			}
			auditAfter, err := f.store.Audit(context.Background(), AuditFilter{DeviceID: f.device.ID, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			if sessionAfter != sessionState || intentAfter != intentState || len(auditAfter) != len(auditBefore) {
				t.Fatalf("mutation after rejection session=%s/%s intent=%s/%s audits=%d/%d", sessionState, sessionAfter, intentState, intentAfter, len(auditBefore), len(auditAfter))
			}
		})
	}
}

type closureStateSnapshot struct {
	sessionState, sessionReason string
	sessionClosed               sql.NullInt64
	intentState, intentReason   string
	intentApplied               sql.NullInt64
	auditCount                  int
}

func snapshotClosureState(t *testing.T, f closureFixture) closureStateSnapshot {
	t.Helper()
	var snapshot closureStateSnapshot
	if err := f.store.db.QueryRow(`SELECT state,reason,closed_at FROM sessions WHERE id=?`, f.intent.SessionID).Scan(&snapshot.sessionState, &snapshot.sessionReason, &snapshot.sessionClosed); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRow(`SELECT state,reason,applied_at FROM closure_intents WHERE id=?`, f.intent.ID).Scan(&snapshot.intentState, &snapshot.intentReason, &snapshot.intentApplied); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRow(`SELECT count(*) FROM audit_events WHERE device_id=?`, f.device.ID).Scan(&snapshot.auditCount); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertClosureStateUnchanged(t *testing.T, before, after closureStateSnapshot) {
	t.Helper()
	if before.sessionState != after.sessionState || before.sessionReason != after.sessionReason || before.sessionClosed != after.sessionClosed || before.intentState != after.intentState || before.intentReason != after.intentReason || before.intentApplied != after.intentApplied || before.auditCount != after.auditCount {
		t.Fatalf("durable state changed before=%+v after=%+v", before, after)
	}
}

func assertSQLiteError(t *testing.T, err error, wantCode int, wantMessage string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected SQLite error code %d", wantCode)
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("error = %v, want modernc.org/sqlite.Error", err)
	}
	if sqliteErr.Code() != wantCode {
		t.Fatalf("SQLite error code = %d, want %d (%s): %v", sqliteErr.Code(), wantCode, sqlite.ErrorCodeString[wantCode], err)
	}
	if wantMessage != "" && !strings.Contains(err.Error(), wantMessage) {
		t.Fatalf("SQLite error = %q, want message containing %q", err.Error(), wantMessage)
	}
}

func TestApplyClosureIntentRejectsInvalidInputBeforeTransaction(t *testing.T) {
	now := time.Date(2026, 9, 11, 21, 16, 0, 0, time.UTC)
	f := newClosureFixture(t, now)
	base := f.request("apply-invalid")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		request ApplyClosureIntentRequest
		ctx     context.Context
		wantErr error
	}{
		{name: "event id empty", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.EventID = "" }), wantErr: ErrInvalidInput},
		{name: "event id invalid character", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.EventID = "event/id" }), wantErr: ErrInvalidInput},
		{name: "device id empty", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.DeviceID = "" }), wantErr: ErrInvalidInput},
		{name: "device id invalid character", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.DeviceID = "device id" }), wantErr: ErrInvalidInput},
		{name: "session id empty", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.SessionID = "" }), wantErr: ErrInvalidInput},
		{name: "session id invalid character", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.SessionID = "session/id" }), wantErr: ErrInvalidInput},
		{name: "actor id empty", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.Actor.ID = "" }), wantErr: ErrInvalidInput},
		{name: "policy id empty", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.Actor.PolicyID = "" }), wantErr: ErrInvalidInput},
		{name: "actor id invalid character", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.Actor.ID = "operator/id" }), wantErr: ErrInvalidInput},
		{name: "policy id invalid character", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.Actor.PolicyID = "policy id" }), wantErr: ErrInvalidInput},
		{name: "fence above JSON safe maximum", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.Fence = maxClosureFence + 1 }), wantErr: ErrInvalidInput},
		{name: "fence negative", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.Fence = -1 }), wantErr: ErrInvalidInput},
		{name: "request id invalid character", request: baseWith(base, func(r *ApplyClosureIntentRequest) { r.RequestID = "request/id" }), wantErr: ErrInvalidInput},
		{name: "nil context", request: base, ctx: nil, wantErr: ErrInvalidInput},
		{name: "canceled context", request: base, ctx: canceled, wantErr: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := snapshotClosureState(t, f)
			ctx := test.ctx
			if ctx == nil && test.name != "nil context" {
				ctx = context.Background()
			}
			_, err := f.store.ApplyClosureIntent(ctx, test.request)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			after := snapshotClosureState(t, f)
			assertClosureStateUnchanged(t, before, after)
		})
	}
}

func TestApplyClosureIntentRejectsStaleDeviceAndSessionFenceWithoutMutation(t *testing.T) {
	now := time.Date(2026, 9, 11, 21, 18, 0, 0, time.UTC)
	f := newClosureFixture(t, now)
	for _, test := range []struct {
		name  string
		setup func(*testing.T)
		reset func(*testing.T)
	}{
		{
			name: "device not revoked",
			setup: func(t *testing.T) {
				if _, err := f.store.db.Exec(`UPDATE devices SET state='active' WHERE id=?`, f.device.ID); err != nil {
					t.Fatal(err)
				}
			},
			reset: func(t *testing.T) {
				if _, err := f.store.db.Exec(`UPDATE devices SET state='revoked' WHERE id=?`, f.device.ID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "device fence advanced",
			setup: func(t *testing.T) {
				if _, err := f.store.db.Exec(`UPDATE devices SET fence=fence+1 WHERE id=?`, f.device.ID); err != nil {
					t.Fatal(err)
				}
			},
			reset: func(t *testing.T) {
				if _, err := f.store.db.Exec(`UPDATE devices SET fence=fence-1 WHERE id=?`, f.device.ID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "session fence changed",
			setup: func(t *testing.T) {
				if _, err := f.store.db.Exec(`UPDATE sessions SET fence=? WHERE id=?`, f.intent.Fence, f.intent.SessionID); err != nil {
					t.Fatal(err)
				}
			},
			reset: func(t *testing.T) {
				if _, err := f.store.db.Exec(`UPDATE sessions SET fence=? WHERE id=?`, f.intent.Fence-1, f.intent.SessionID); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.setup(t)
			defer test.reset(t)
			before, err := f.store.Audit(context.Background(), AuditFilter{DeviceID: f.device.ID, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-state-conflict")); !errors.Is(err, ErrClosureIntentConflict) {
				t.Fatalf("error = %v, want ErrClosureIntentConflict", err)
			}
			after, err := f.store.Audit(context.Background(), AuditFilter{DeviceID: f.device.ID, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			var sessionState, intentState string
			if err := f.store.db.QueryRow(`SELECT state FROM sessions WHERE id=?`, f.intent.SessionID).Scan(&sessionState); err != nil {
				t.Fatal(err)
			}
			if err := f.store.db.QueryRow(`SELECT state FROM closure_intents WHERE id=?`, f.intent.ID).Scan(&intentState); err != nil {
				t.Fatal(err)
			}
			if sessionState != "active" || intentState != "pending" || len(after) != len(before) {
				t.Fatalf("state mutated session=%s intent=%s audits=%d/%d", sessionState, intentState, len(after), len(before))
			}
		})
	}
}

func TestApplyClosureIntentRejectsDurableSemanticConflictsWithoutDisablingIntegrity(t *testing.T) {
	now := time.Date(2026, 9, 11, 21, 19, 0, 0, time.UTC)
	f := newClosureFixture(t, now)
	before := snapshotClosureState(t, f)
	// Both values are valid vocabulary values, so this fixture exercises the
	// semantic relation between intent and revocation without weakening checks
	// or triggers. The API must reject the inconsistent durable relation.
	if _, err := f.store.db.Exec(`UPDATE closure_intents SET reason='certificate' WHERE id=?`, f.intent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-reason-conflict")); !errors.Is(err, ErrClosureIntentConflict) {
		t.Fatalf("reason conflict error = %v, want ErrClosureIntentConflict", err)
	}
	if after := snapshotClosureState(t, f); after.sessionState != before.sessionState || after.intentState != before.intentState || after.auditCount != before.auditCount {
		t.Fatalf("reason conflict mutated state before=%+v after=%+v", before, after)
	}
	if _, err := f.store.db.Exec(`UPDATE closure_intents SET reason=? WHERE id=?`, string(f.intent.Reason), f.intent.ID); err != nil {
		t.Fatal(err)
	}

	var revocationID string
	if err := f.store.db.QueryRow(`SELECT revocation_id FROM closure_intents WHERE id=?`, f.intent.ID).Scan(&revocationID); err != nil {
		t.Fatal(err)
	}
	// An orphaned intent cannot be a valid v1 fixture: foreign_keys=ON rejects
	// it before ApplyClosureIntent can observe it. Keep that boundary explicit
	// instead of disabling integrity and calling the result an API test.
	for _, test := range []struct {
		name         string
		intentID     string
		sessionID    string
		revocationID string
		wantCode     int
	}{
		{name: "missing revocation", intentID: "orphan-revocation", sessionID: f.intent.SessionID, revocationID: "missing-revocation", wantCode: sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY},
		{name: "missing session", intentID: "orphan-session", sessionID: "missing-session", revocationID: revocationID, wantCode: sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Use a fresh device/session/fence key so the UNIQUE constraint cannot
			// mask the intended missing-parent FOREIGN KEY failure.
			_, err := f.store.db.Exec(`INSERT INTO closure_intents(id,device_id,session_id,revocation_id,fence,reason,state,created_at) VALUES (?,?,?,?,?,'administrator','pending',?)`, test.intentID, f.device.ID, test.sessionID, test.revocationID, f.intent.Fence+1, now.UnixNano())
			assertSQLiteError(t, err, test.wantCode, "FOREIGN KEY")
			if after := snapshotClosureState(t, f); after.sessionState != before.sessionState || after.intentState != before.intentState || after.auditCount != before.auditCount {
				t.Fatalf("orphan fixture mutated state before=%+v after=%+v", before, after)
			}
		})
	}
	if _, err := f.store.db.Exec(`UPDATE closure_intents SET applied_at=? WHERE id=?`, now.UnixNano(), f.intent.ID); err == nil {
		t.Fatal("pending closure intent with applied_at unexpectedly admitted")
	} else {
		assertSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_CHECK, "CHECK")
	}
	if _, err := f.store.db.Exec(`UPDATE revocations SET state='pending' WHERE id=?`, revocationID); err == nil {
		t.Fatal("non-applied revocation unexpectedly admitted")
	} else {
		assertSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_TRIGGER, "revocations are immutable")
	}
	if after := snapshotClosureState(t, f); after.sessionState != before.sessionState || after.intentState != before.intentState || after.auditCount != before.auditCount {
		t.Fatalf("revocation state fixture mutated state before=%+v after=%+v", before, after)
	}

	if _, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-terminal-incoherent-setup")); err != nil {
		t.Fatal(err)
	}
	terminalBefore := snapshotClosureState(t, f)
	// This is schema-valid state but semantically incoherent with an applied
	// intent; it reaches the API's terminal-consistency guard without dropping
	// any integrity trigger.
	if _, err := f.store.db.Exec(`UPDATE sessions SET state='active',reason='',closed_at=NULL WHERE id=?`, f.intent.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-terminal-incoherent")); !errors.Is(err, ErrClosureIntentConflict) {
		t.Fatalf("terminal inconsistency error = %v, want ErrClosureIntentConflict", err)
	}
	terminalAfter := snapshotClosureState(t, f)
	if terminalAfter.intentState != terminalBefore.intentState || terminalAfter.auditCount != terminalBefore.auditCount || terminalAfter.sessionState != "active" {
		t.Fatalf("terminal inconsistency mutated state before=%+v after=%+v", terminalBefore, terminalAfter)
	}
}

func baseWith(base ApplyClosureIntentRequest, mutate func(*ApplyClosureIntentRequest)) ApplyClosureIntentRequest {
	request := base
	mutate(&request)
	return request
}

func TestApplyClosureIntentAuditFailureRollsBackAllState(t *testing.T) {
	now := time.Date(2026, 9, 11, 21, 20, 0, 0, time.UTC)
	f := newClosureFixture(t, now)
	baseline := snapshotClosureState(t, f)
	if _, err := f.store.db.Exec(`CREATE TRIGGER block_session_closed BEFORE INSERT ON audit_events WHEN NEW.event_kind='session.closed' BEGIN SELECT RAISE(ABORT, 'session closed failure injection'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-before-transition-rollback")); err == nil {
		t.Fatal("session.closed audit failure unexpectedly succeeded")
	} else {
		assertSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_TRIGGER, "session closed failure injection")
	}
	assertClosureStateUnchanged(t, baseline, snapshotClosureState(t, f))
	if _, err := f.store.db.Exec(`DROP TRIGGER block_session_closed`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`CREATE TRIGGER block_closure_applied BEFORE INSERT ON audit_events WHEN NEW.event_kind='closure.applied' BEGIN SELECT RAISE(ABORT, 'closure applied failure injection'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-rollback")); err == nil {
		t.Fatal("audit failure unexpectedly succeeded")
	} else {
		assertSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_TRIGGER, "closure applied failure injection")
	}
	assertClosureStateUnchanged(t, baseline, snapshotClosureState(t, f))
	if _, err := f.store.db.Exec(`DROP TRIGGER block_closure_applied`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-after-rollback")); err != nil {
		t.Fatal(err)
	}
}

func TestApplyClosureIntentAuditFailureRollsBackAlreadyClosedSession(t *testing.T) {
	now := time.Date(2026, 9, 11, 21, 21, 0, 0, time.UTC)
	f := newClosureFixture(t, now)
	original, err := f.store.CloseSession(context.Background(), CloseSessionRequest{SessionID: f.intent.SessionID, DeviceID: f.device.ID, Actor: f.actor, Reason: SessionCloseReasonAdministrator, RequestID: "close-before-rollback"})
	if err != nil {
		t.Fatal(err)
	}
	baseline := snapshotClosureState(t, f)
	if _, err := f.store.db.Exec(`CREATE TRIGGER block_closed_intent BEFORE INSERT ON audit_events WHEN NEW.event_kind='closure.applied' BEGIN SELECT RAISE(ABORT, 'closed intent failure injection'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-closed-rollback")); err == nil {
		t.Fatal("already-closed audit failure unexpectedly succeeded")
	} else {
		assertSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_TRIGGER, "closed intent failure injection")
	}
	after := snapshotClosureState(t, f)
	assertClosureStateUnchanged(t, baseline, after)
	if after.sessionState != "closed" || after.sessionReason != string(original.Reason) || !after.sessionClosed.Valid || after.sessionClosed.Int64 != original.ClosedAt.UnixNano() || after.intentState != "pending" {
		t.Fatalf("already-closed rollback state = %+v, original = %+v", after, original)
	}
	if _, err := f.store.db.Exec(`DROP TRIGGER block_closed_intent`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyClosureIntent(context.Background(), f.request("apply-closed-after-rollback")); err != nil {
		t.Fatal(err)
	}
}

func TestApplyClosureIntentReadsClockAfterImmediateLock(t *testing.T) {
	now := time.Date(2026, 9, 11, 21, 22, 0, 0, time.UTC)
	f := newClosureFixture(t, now)
	second, err := Open(context.Background(), f.store.path, WithClock(f.clock.Now), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x6e}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	lockTx, err := f.store.db.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback()
	late := now.Add(3 * time.Hour)
	beginAttempt := make(chan struct{})
	applyContext := &beginCheckpointContext{Context: context.Background(), begin: beginAttempt}
	clockRead := make(chan struct{})
	var clockReadOnce sync.Once
	f.clock.SetOnNow(func() { clockReadOnce.Do(func() { close(clockRead) }) })
	applyDone := make(chan struct{})
	var result ApplyClosureIntentResponse
	var applyError error
	go func() {
		result, applyError = second.ApplyClosureIntent(applyContext, f.request("apply-after-lock"))
		close(applyDone)
	}()
	select {
	case <-beginAttempt:
		// database/sql asks the context for Done at the start of BeginTx. The
		// immediate write lock is still held here, so the apply cannot finish
		// before BEGIN IMMEDIATE acquires that lock.
	case <-time.After(5 * time.Second):
		t.Fatal("apply did not reach BeginTx")
	}
	select {
	case <-clockRead:
		t.Fatal("clock was read before BEGIN IMMEDIATE acquired the lock")
	default:
	}
	select {
	case <-applyDone:
		t.Fatal("apply completed while immediate lock was held")
	default:
	}
	// This update happens after BeginTx has started but before the lock can be
	// acquired. A clock read before BeginTx would therefore retain `now`.
	f.clock.Set(late)
	if err := lockTx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-applyDone:
		if applyError != nil {
			t.Fatal(applyError)
		}
		if !result.Intent.AppliedAt.Equal(late) || !result.Session.ClosedAt.Equal(late) {
			t.Fatalf("timestamps = intent %v session %v, want %v", result.Intent.AppliedAt, result.Session.ClosedAt, late)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("apply did not finish after lock release")
	}
}

func TestApplyClosureIntentConcurrentHandlesHaveOneTerminalWinner(t *testing.T) {
	now := time.Date(2026, 9, 11, 21, 25, 0, 0, time.UTC)
	clock := &testClock{now: now}
	actor := Actor{ID: "operator-1", PolicyID: "policy-1"}
	spki, private := testKeys(t)
	path := filepath.Join(t.TempDir(), "registry", "devices.sqlite")
	store1, err := Open(context.Background(), path, WithClock(clock.Now), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x6b}, 64))), WithCertificateIssuer(&testIssuer{clock: clock}))
	if err != nil {
		t.Fatal(err)
	}
	defer store1.Close()
	enrollment := createEnrollment(t, store1)
	challenge := issueChallenge(t, store1, enrollment, spki)
	device, err := store1.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signChallenge(challenge, enrollment, private)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store1.RegisterSession(context.Background(), RegisterSessionRequest{SessionID: "session-1", DeviceID: device.ID, Actor: actor, RequestID: "register-1"}); err != nil {
		t.Fatal(err)
	}
	revocation, err := store1.RevokeDevice(context.Background(), RevokeDeviceRequest{DeviceID: device.ID, Actor: actor, Reason: RevocationReasonAdministrator, RequestID: "revoke-1"})
	if err != nil {
		t.Fatal(err)
	}
	store2, err := Open(context.Background(), path, WithClock(clock.Now), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x6c}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	req := ApplyClosureIntentRequest{EventID: revocation.Intents[0].ID, DeviceID: device.ID, SessionID: "session-1", Fence: revocation.Intents[0].Fence, Actor: actor}
	results := make(chan ApplyClosureIntentResponse, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, store := range []*Store{store1, store2} {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			request := req
			request.RequestID = "apply-" + requestIDForStore(store, store1)
			result, applyErr := store.ApplyClosureIntent(context.Background(), request)
			if applyErr != nil {
				errs <- applyErr
				return
			}
			results <- result
		}(store)
	}
	wg.Wait()
	close(results)
	close(errs)
	if len(errs) != 0 || len(results) != 2 {
		t.Fatalf("concurrent results=%d errors=%d", len(results), len(errs))
	}
	var applied, already int
	for result := range results {
		if result.Already {
			already++
		} else {
			applied++
		}
	}
	if applied != 1 || already != 1 {
		t.Fatalf("concurrent outcomes applied=%d already=%d", applied, already)
	}
}

func requestIDForStore(store, first *Store) string {
	if store == first {
		return "one"
	}
	return "two"
}
