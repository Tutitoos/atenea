//nolint:revive // The closure API is documented at the package boundary.
package remotedevice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// maxClosureFence is the largest integer that can be represented exactly by
// the JSON number format used by atenea.remote.v1.  The store never accepts a
// closure acknowledgement outside that wire-safe range.
const maxClosureFence int64 = 9007199254740991

var (
	// ErrClosureIntentNotFound means that the acknowledged event_id does not
	// identify a durable closure intent.
	ErrClosureIntentNotFound = errors.New("remote device: closure intent not found")
	// ErrClosureIntentConflict means that a durable intent, revocation, device,
	// session, actor, or fence is not the exact state named by the request.
	// Conflicts are returned before any state or audit row is changed.
	ErrClosureIntentConflict = errors.New("remote device: incompatible closure intent")
)

// ApplyClosureIntentRequest is the coordinator-side projection of a validated
// revoked event acknowledgement.  EventID is the durable closure_intents.id;
// RequestID identifies this API invocation and may be fresh on a terminal
// replay. Actor is the original administrative authority persisted by the
// durable revocation, not a peer identity or socket owner. The wire event_ack
// intentionally carries no Actor; transport authentication and socket-owner
// binding remain coordinator responsibilities.
type ApplyClosureIntentRequest struct {
	EventID   string
	DeviceID  string
	SessionID string
	Fence     int64
	Actor     Actor
	RequestID string
}

// ApplyClosureIntentResponse returns the stable terminal intent and session
// views.  Already is true only when the requested intent was already applied;
// terminal replay never creates another audit event or changes either view.
type ApplyClosureIntentResponse struct {
	Intent  ClosureIntent `json:"intent"`
	Session Session       `json:"session"`
	Already bool          `json:"already,omitempty"`
}

// ApplyClosureIntent atomically applies one acknowledged device-revocation
// closure intent.  It is deliberately a store-only operation: transport
// owner binding, event delivery, acknowledgement freshness, and socket close
// remain coordinator responsibilities.
//
// The SQLite DSN is configured with _txlock=immediate.  All rows are read and
// revalidated after that lock is acquired.  For a pending intent, an active
// session is closed with the revocation fence and the session.closed audit is
// inserted before closure.applied.  A session already closed before this call
// retains its original reason, timestamp, and admission fence; only the intent
// transition and closure.applied audit are added.  An applied intent is a
// read-only terminal replay when every durable binding, including actor,
// still matches.
func (s *Store) ApplyClosureIntent(ctx context.Context, req ApplyClosureIntentRequest) (ApplyClosureIntentResponse, error) {
	var result ApplyClosureIntentResponse
	if err := s.ensureOpen(); err != nil {
		return result, err
	}
	if ctx == nil {
		return result, fmt.Errorf("%w: nil context", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := validateID(req.EventID, "event id"); err != nil {
		return result, err
	}
	if err := validateID(req.DeviceID, "device id"); err != nil {
		return result, err
	}
	if err := validateID(req.SessionID, "session id"); err != nil {
		return result, err
	}
	if req.Fence < 1 || req.Fence > maxClosureFence {
		return result, fmt.Errorf("%w: closure fence must be between 1 and %d", ErrInvalidInput, maxClosureFence)
	}
	if err := validateActor(req.Actor); err != nil {
		return result, err
	}
	requestID, err := validateRequestID(req.RequestID)
	if err != nil {
		return result, err
	}
	if requestID == "" {
		return result, fmt.Errorf("%w: request id is required", ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	// currentTime must be sampled after BEGIN IMMEDIATE.  This keeps the
	// session.closed and closure.applied timestamps at the transaction's
	// serialization point, even when another handle was holding the lock.
	now := s.currentTime()
	if err := ctx.Err(); err != nil {
		return result, err
	}

	intent, err := readClosureIntentForApply(ctx, tx, req.EventID)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrClosureIntentNotFound
	}
	if err != nil {
		return result, err
	}
	if intent.ID != req.EventID || intent.DeviceID != req.DeviceID || intent.SessionID != req.SessionID || intent.Fence != req.Fence {
		return result, closureIntentConflict("request does not exactly match intent")
	}
	if intent.State != "pending" && intent.State != "applied" {
		return result, closureIntentConflict("intent has unsupported state %q", intent.State)
	}
	if intent.Fence < 1 || intent.Fence > maxClosureFence {
		return result, closureIntentConflict("intent has invalid fence")
	}

	var revocation Revocation
	var revocationActorID, revocationPolicyID, revocationReason, revocationState string
	var revocationCreated int64
	err = tx.QueryRowContext(ctx, `SELECT id,device_id,actor_id,policy_id,reason,state,created_at FROM revocations WHERE id=?`, intent.RevocationID).Scan(
		&revocation.ID, &revocation.DeviceID, &revocationActorID, &revocationPolicyID, &revocationReason, &revocationState, &revocationCreated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return result, closureIntentConflict("referenced revocation is missing")
	}
	if err != nil {
		return result, err
	}
	revocation.Actor = Actor{ID: revocationActorID, PolicyID: revocationPolicyID}
	revocation.Reason = RevocationReason(revocationReason)
	revocation.State = revocationState
	revocation.CreatedAt = time.Unix(0, revocationCreated).UTC()
	if revocation.ID != intent.RevocationID || revocation.DeviceID != intent.DeviceID || revocation.State != "applied" || revocation.Reason != intent.Reason || revocation.Actor != req.Actor {
		return result, closureIntentConflict("revocation binding is not current")
	}

	var deviceState string
	var deviceFence int64
	if err := tx.QueryRowContext(ctx, `SELECT state,fence FROM devices WHERE id=?`, req.DeviceID).Scan(&deviceState, &deviceFence); errors.Is(err, sql.ErrNoRows) {
		return result, ErrDeviceNotFound
	} else if err != nil {
		return result, err
	}
	if deviceState != "revoked" || deviceFence != req.Fence {
		return result, closureIntentConflict("device is not revoked at the acknowledged fence")
	}

	session, err := readSessionForApply(ctx, tx, req.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrSessionNotFound
	}
	if err != nil {
		return result, err
	}
	if session.ID != req.SessionID || session.DeviceID != req.DeviceID || session.Fence < 0 || session.Fence == maxClosureFence || session.Fence+1 != req.Fence {
		return result, closureIntentConflict("session binding is not immediately prior to the acknowledged fence")
	}

	if intent.State == "applied" {
		if session.State != "closed" || !intent.AppliedAt.After(time.Time{}) {
			return result, closureIntentConflict("applied intent does not have a terminal session")
		}
		if err := tx.Commit(); err != nil {
			return result, err
		}
		result.Intent, result.Session, result.Already = intent, session, true
		return result, nil
	}

	if session.State == "active" {
		updated, updateErr := tx.ExecContext(ctx, `UPDATE sessions SET state='closed',reason='revoked',closed_at=? WHERE id=? AND device_id=? AND state='active' AND fence=?`, now.UnixNano(), req.SessionID, req.DeviceID, session.Fence)
		if updateErr != nil {
			return result, updateErr
		}
		if affected, affectedErr := updated.RowsAffected(); affectedErr != nil {
			return result, affectedErr
		} else if affected != 1 {
			return result, closureIntentConflict("active session changed before closure")
		}
		session.State = "closed"
		session.Reason = SessionCloseReasonRevoked
		session.ClosedAt = now
		if err := insertAudit(ctx, tx, auditEvent{Kind: EventSessionClosed, AggregateType: "session", AggregateID: req.SessionID, DeviceID: req.DeviceID, SessionID: req.SessionID, RequestID: requestID, Actor: req.Actor, Outcome: OutcomeAllow, Reason: ReasonRevoked, CreatedAt: now}); err != nil {
			return result, err
		}
	} else if session.State != "closed" {
		return result, closureIntentConflict("session has unsupported state %q", session.State)
	}

	updated, err := tx.ExecContext(ctx, `UPDATE closure_intents SET state='applied',applied_at=? WHERE id=? AND state='pending' AND device_id=? AND session_id=? AND fence=?`, now.UnixNano(), req.EventID, req.DeviceID, req.SessionID, req.Fence)
	if err != nil {
		return result, err
	}
	if affected, affectedErr := updated.RowsAffected(); affectedErr != nil {
		return result, affectedErr
	} else if affected != 1 {
		return result, closureIntentConflict("pending intent changed before application")
	}
	intent.State = "applied"
	intent.AppliedAt = now
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventClosureApplied, AggregateType: "closure_intent", AggregateID: intent.ID, DeviceID: req.DeviceID, SessionID: req.SessionID, RequestID: requestID, Actor: req.Actor, Outcome: OutcomeAllow, Reason: ReasonRevoked, CreatedAt: now}); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	result.Intent, result.Session = intent, session
	return result, nil
}

func closureIntentConflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrClosureIntentConflict, fmt.Sprintf(format, args...))
}

func readClosureIntentForApply(ctx context.Context, tx *sql.Tx, eventID string) (ClosureIntent, error) {
	var intent ClosureIntent
	var reason, state string
	var created, applied sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id,device_id,session_id,revocation_id,fence,reason,state,created_at,applied_at FROM closure_intents WHERE id=?`, eventID).Scan(
		&intent.ID, &intent.DeviceID, &intent.SessionID, &intent.RevocationID, &intent.Fence, &reason, &state, &created, &applied,
	)
	if err != nil {
		return ClosureIntent{}, err
	}
	intent.Reason = RevocationReason(reason)
	intent.State = state
	intent.CreatedAt = time.Unix(0, created.Int64).UTC()
	if applied.Valid {
		intent.AppliedAt = time.Unix(0, applied.Int64).UTC()
	}
	return intent, nil
}

func readSessionForApply(ctx context.Context, tx *sql.Tx, sessionID string) (Session, error) {
	var session Session
	var reason string
	var created, closed sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id,device_id,state,fence,reason,created_at,closed_at FROM sessions WHERE id=?`, sessionID).Scan(
		&session.ID, &session.DeviceID, &session.State, &session.Fence, &reason, &created, &closed,
	)
	if err != nil {
		return Session{}, err
	}
	session.Reason = SessionCloseReason(reason)
	session.CreatedAt = time.Unix(0, created.Int64).UTC()
	if closed.Valid {
		session.ClosedAt = time.Unix(0, closed.Int64).UTC()
	}
	return session, nil
}
