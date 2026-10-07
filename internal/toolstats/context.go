package toolstats

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"
)

// ContextBreakdown describes low-level recorded activity, never people or journeys.
// Version values are reduced to a numeric major version; arbitrary metadata is
// never returned to callers.
type ContextBreakdown struct {
	Version               int           `json:"version"`
	Since                 time.Time     `json:"since"`
	Until                 time.Time     `json:"until"`
	HistoryAvailable      bool          `json:"history_available"`
	Partial               bool          `json:"partial"`
	RecordingStarted      *time.Time    `json:"recording_started,omitempty"`
	LastRecorded          *time.Time    `json:"last_recorded,omitempty"`
	Requests              int64         `json:"requests"`
	Attempts              int64         `json:"attempts"`
	ActiveRequests        int64         `json:"active_requests"`
	MissingContext        int64         `json:"missing_context"`
	MissingAttemptContext int64         `json:"missing_attempt_context"`
	UnknownOriginRequests int64         `json:"unknown_origin_requests"`
	UnknownOriginAttempts int64         `json:"unknown_origin_attempts"`
	OmittedRollupRequests int64         `json:"omitted_rollup_requests"`
	OmittedRollupAttempts int64         `json:"omitted_rollup_attempts"`
	DroppedRecordings     int64         `json:"dropped_recordings"`
	RecoveredRequests     int64         `json:"recovered_requests"`
	RecoveredAttempts     int64         `json:"recovered_attempts"`
	Rows                  []ContextRow  `json:"rows"`
	Overflow              ContextCounts `json:"overflow"`
	Notes                 []string      `json:"notes"`
}

// ContextCounts keeps request and attempt totals separate.
type ContextCounts struct {
	Requests int64 `json:"requests"`
	Attempts int64 `json:"attempts"`
}

// ContextRow is one normalized context bucket.
type ContextRow struct {
	Client  string `json:"client"`
	Profile string `json:"profile"`
	Origin  string `json:"origin"`
	Version string `json:"version"`
	ContextCounts
}

const contextMaxRows = 20

// Context reads at most 168 trailing hours from the existing database. Its
// output does not contain diagnostic reasons, IDs, paths, or raw metadata.
func (s *Store) Context(ctx context.Context, q Query) (ContextBreakdown, error) {
	if q.Until.IsZero() {
		q.Until = time.Now()
	}
	if q.Since.IsZero() {
		q.Since = q.Until.Add(-168 * time.Hour)
	}
	out := ContextBreakdown{Version: 1, Since: q.Since, Until: q.Until, Rows: []ContextRow{}, Notes: []string{"Origin is recorded caller metadata; normal does not verify a human request."}}
	if err := q.Validate(); err != nil {
		return out, err
	}
	if q.Until.Sub(q.Since) > 168*time.Hour || !q.Since.Before(q.Until) {
		return out, fmt.Errorf("stats context: window must be positive and at most 168 hours")
	}
	if q.Repository != "" || q.Provider != "" || q.Tool != "" || q.Used {
		return out, fmt.Errorf("stats context: filters are not supported")
	}
	if _, err := os.Stat(s.Path); os.IsNotExist(err) {
		out.Notes = append(out.Notes, "Activity recording has not started.")
		return out, nil
	} else if err != nil {
		return out, err
	}
	db, err := sql.Open("sqlite", dsn(s.Path, "ro"))
	if err != nil {
		return out, err
	}
	defer func() { _ = db.Close() }()
	dead, release, err := deadOwners(ctx, db, s.Path)
	defer release()
	if err != nil {
		return out, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	var started int64
	if err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='started'`).Scan(&started); err != nil {
		return out, err
	}
	out.HistoryAvailable = true
	startedAt := time.UnixMicro(started).UTC()
	out.RecordingStarted = &startedAt
	var lastEvent, lastRollup sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT max(at) FROM events`).Scan(&lastEvent); err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT max(last) FROM rollups`).Scan(&lastRollup); err != nil {
		return out, err
	}
	last := lastEvent.Int64
	if lastRollup.Valid && lastRollup.Int64 > last {
		last = lastRollup.Int64
	}
	if lastEvent.Valid || lastRollup.Valid {
		at := time.UnixMicro(last).UTC()
		out.LastRecorded = &at
	}
	if out.LastRecorded == nil || out.LastRecorded.Before(q.Since) {
		out.Notes = append(out.Notes, "No recent recorded activity; a readable historical database does not prove that the recorder is currently active.")
	}
	if q.Since.UnixMicro() < started {
		out.Partial = true
		out.Notes = append(out.Notes, "Window starts before activity recording; earlier requests are unavailable.")
	}
	if err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='dropped'`).Scan(&out.DroppedRecordings); err != nil && err != sql.ErrNoRows {
		return out, err
	}
	out.DroppedRecordings += s.dropped.Load()
	if out.DroppedRecordings > 0 {
		out.Partial = true
		out.Notes = append(out.Notes, "Recording operations were dropped; historical counts may be incomplete.")
	}
	var hasContext, hasRollups bool
	if hasContext, err = hasTable(ctx, tx, "event_context"); err != nil {
		return out, err
	}
	if hasRollups, err = hasTable(ctx, tx, "context_rollups"); err != nil {
		return out, err
	}
	hasOwners, err := hasTable(ctx, tx, "event_owners")
	if err != nil {
		return out, err
	}
	if !hasContext {
		out.Partial = true
		out.Notes = append(out.Notes, "This database predates request context metadata.")
	}
	// Read-only owner inspection classifies interrupted recordings without
	// changing storage or pretending that their execution failed.
	filter := `at>=? AND at<? AND level=?`
	args := []any{q.Since.UnixMicro(), q.Until.UnixMicro()}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE `+filter+` AND ended IS NULL`, append(args, "request")...).Scan(&out.ActiveRequests); err != nil {
		return out, err
	}
	for _, level := range []string{"request", "attempt"} {
		var readOnly, stored int64
		if dead != "[]" {
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE `+filter+` AND ended IS NULL AND id IN (SELECT event FROM event_owners WHERE owner IN (SELECT value FROM json_each(?)))`, append(args, level, dead)...).Scan(&readOnly); err != nil {
				return out, err
			}
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE `+filter+` AND code='recording_interrupted' AND ended IS NOT NULL AND ended<?`, append(args, level, q.Until.UnixMicro())...).Scan(&stored); err != nil {
			return out, err
		}
		if hasRollups {
			var compacted int64
			if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(calls),0) FROM context_rollups WHERE level=? AND code='recording_interrupted' AND bucket>=? AND bucket+86400000000<=? AND max_ended<?`, level, q.Since.UnixMicro(), q.Until.UnixMicro(), q.Until.UnixMicro()).Scan(&compacted); err != nil {
				return out, err
			}
			stored += compacted
		}
		if level == "request" {
			out.RecoveredRequests = readOnly + stored
			out.ActiveRequests -= readOnly
		} else {
			out.RecoveredAttempts = readOnly + stored
		}
	}
	if out.RecoveredRequests > 0 || out.RecoveredAttempts > 0 {
		out.Partial = true
		out.Notes = append(out.Notes, "Interrupted recordings have unknown execution outcomes and durations.")
	}
	// SQL normalizes dimensions before grouping; no arbitrary metadata leaves
	// SQLite. The fixed categories keep cardinality and rendering bounded.
	client := `CASE lower(coalesce(client,'')) WHEN 'codex' THEN 'codex' WHEN 'chatgpt' THEN 'chatgpt' WHEN 'claude' THEN 'claude' WHEN 'omp' THEN 'omp' WHEN 'opencode' THEN 'opencode' WHEN '' THEN 'unknown' ELSE 'other' END`
	profile := `CASE lower(coalesce(profile,'')) WHEN 'shared' THEN 'shared' WHEN 'chatgpt' THEN 'chatgpt' WHEN 'codex' THEN 'codex' WHEN 'claude' THEN 'claude' WHEN 'omp' THEN 'omp' WHEN 'opencode' THEN 'opencode' WHEN '' THEN 'unknown' ELSE 'other' END`
	origin := `CASE origin WHEN 'normal' THEN 'normal' WHEN 'synthetic' THEN 'synthetic' ELSE 'unknown' END`
	major := `CASE WHEN coalesce(client_version,'') GLOB '[0-9]' OR coalesce(client_version,'') GLOB '[0-9][0-9]' THEN 'v'||client_version WHEN coalesce(client_version,'') GLOB '[0-9].*' THEN 'v'||substr(client_version,1,1) WHEN coalesce(client_version,'') GLOB '[0-9][0-9].*' THEN 'v'||substr(client_version,1,2) WHEN coalesce(client_version,'')='' THEN 'unknown' ELSE 'other' END`
	complete := `(ended IS NOT NULL AND ended<?)`
	completionArgs := []any{q.Until.UnixMicro()}
	if hasOwners && dead != "[]" {
		complete = `(ended IS NOT NULL AND ended<? OR events.id IN (SELECT event FROM event_owners WHERE owner IN (SELECT value FROM json_each(?))))`
		completionArgs = append(completionArgs, dead)
	}
	contextSource := `SELECT level,` + client + ` client,` + profile + ` profile,` + origin + ` origin,` + major + ` version,count(*) calls FROM events LEFT JOIN event_context ON events.id=event_context.event WHERE at>=? AND at<? AND ` + complete + ` AND level IN ('request','attempt') GROUP BY 1,2,3,4,5`
	if !hasContext {
		contextSource = `SELECT level,'unknown' client,'unknown' profile,'unknown' origin,'unknown' version,count(*) calls FROM events WHERE at>=? AND at<? AND ` + complete + ` AND level IN ('request','attempt') GROUP BY level`
	}
	queryArgs := append([]any{q.Since.UnixMicro(), q.Until.UnixMicro()}, completionArgs...)
	if hasRollups {
		contextSource += ` UNION ALL SELECT level,` + client + `,` + profile + `,` + origin + `,` + major + `,sum(calls) FROM context_rollups WHERE bucket>=? AND bucket+86400000000<=? AND max_ended<? AND level IN ('request','attempt') GROUP BY 1,2,3,4,5`
		queryArgs = append(queryArgs, q.Since.UnixMicro(), q.Until.UnixMicro(), q.Until.UnixMicro())
	}
	rows, err := tx.QueryContext(ctx, `WITH raw AS (`+contextSource+`) SELECT client,profile,origin,version,sum(CASE WHEN level='request' THEN calls ELSE 0 END),sum(CASE WHEN level='attempt' THEN calls ELSE 0 END) FROM raw GROUP BY 1,2,3,4 ORDER BY 5 DESC,6 DESC,1,2,3,4`, queryArgs...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var r ContextRow
		if err = rows.Scan(&r.Client, &r.Profile, &r.Origin, &r.Version, &r.Requests, &r.Attempts); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Requests += r.Requests
		out.Attempts += r.Attempts
		if r.Origin == "unknown" {
			out.UnknownOriginRequests += r.Requests
			out.UnknownOriginAttempts += r.Attempts
		}
		if len(out.Rows) < contextMaxRows {
			out.Rows = append(out.Rows, r)
		} else {
			out.Overflow.Requests += r.Requests
			out.Overflow.Attempts += r.Attempts
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	if out.Overflow.Requests > 0 || out.Overflow.Attempts > 0 {
		out.Notes = append(out.Notes, "Additional dimension groups are included in overflow.")
	}
	if out.Requests == 0 && out.Attempts == 0 {
		out.Notes = append(out.Notes, "Zero recorded calls in this window is inconclusive about actual user activity.")
	}
	if hasContext {
		for _, level := range []string{"request", "attempt"} {
			var count int64
			queryArgs := append(append([]any{q.Since.UnixMicro(), q.Until.UnixMicro()}, completionArgs...), level)
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM events LEFT JOIN event_context ON events.id=event_context.event WHERE at>=? AND at<? AND `+complete+` AND level=? AND event_context.event IS NULL`, queryArgs...).Scan(&count); err != nil {
				return out, err
			}
			if level == "request" {
				out.MissingContext = count
			} else {
				out.MissingAttemptContext = count
			}
		}
	} else {
		out.MissingContext = out.Requests
		out.MissingAttemptContext = out.Attempts
	}
	if out.MissingContext > 0 || out.MissingAttemptContext > 0 {
		out.Partial = true
		out.Notes = append(out.Notes, "Some request or attempt records lack context metadata; their origin and dimensions are unknown.")
	}
	if out.UnknownOriginRequests > 0 || out.UnknownOriginAttempts > 0 {
		out.Partial = true
		out.Notes = append(out.Notes, "Unknown-origin activity cannot be counted as normal traffic.")
	}
	// Base rollups can predate context_rollups. Exclude straddling buckets;
	// report their counts rather than silently attributing them to this window.
	for _, level := range []string{"request", "attempt"} {
		var base, contextual, boundary int64
		if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(calls),0) FROM rollups WHERE level=? AND bucket>=? AND bucket+86400000000<=?`, level, q.Since.UnixMicro(), q.Until.UnixMicro()).Scan(&base); err != nil {
			return out, err
		}
		if hasRollups {
			if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(calls),0) FROM context_rollups WHERE level=? AND bucket>=? AND bucket+86400000000<=? AND max_ended<?`, level, q.Since.UnixMicro(), q.Until.UnixMicro(), q.Until.UnixMicro()).Scan(&contextual); err != nil {
				return out, err
			}
		}
		if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(calls),0) FROM rollups WHERE level=? AND bucket+86400000000>? AND bucket<? AND (bucket<? OR bucket+86400000000>?)`, level, q.Since.UnixMicro(), q.Until.UnixMicro(), q.Since.UnixMicro(), q.Until.UnixMicro()).Scan(&boundary); err != nil {
			return out, err
		}
		omitted := boundary
		if base > contextual {
			omitted += base - contextual
		}
		if level == "request" {
			out.OmittedRollupRequests = omitted
		} else {
			out.OmittedRollupAttempts = omitted
		}
		if omitted > 0 {
			out.Partial = true
		}
	}
	if out.OmittedRollupRequests > 0 || out.OmittedRollupAttempts > 0 {
		out.Notes = append(out.Notes, "Older, incomplete, or partial UTC-day rollups were omitted from contextual attribution.")
	}
	if out.ActiveRequests > 0 {
		out.Notes = append(out.Notes, "Active requests are separate from completed request counts.")
	}
	return out, nil
}
