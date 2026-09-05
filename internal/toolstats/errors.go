package toolstats

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// ErrorQuery pages request failures without loading all events into memory.
type ErrorQuery struct {
	Query
	Limit   int    `json:"limit,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
	Code    string `json:"error_code,omitempty"`
	Client  string `json:"client,omitempty"`
	Profile string `json:"profile,omitempty"`
	Origin  string `json:"origin,omitempty"`
}

// ErrorRow is one retained failure with its request and execution context.
type ErrorRow struct {
	Diagnostic
	ID         string `json:"request_id"`
	Repository string `json:"repository,omitempty"`
	Outcome    string `json:"outcome"`
	Metadata
}

// ErrorGroup retains cause counts after individual diagnostics expire.
type ErrorGroup struct {
	ClientVersion   string `json:"client_version,omitempty"`
	ProviderVersion string `json:"provider_version,omitempty"`
	SchemaHash      string `json:"schema_hash,omitempty"`
	Tool            string `json:"tool"`
	Provider        string `json:"provider"`
	Repository      string `json:"repository,omitempty"`
	Code            string `json:"error_code"`
	Client          string `json:"client,omitempty"`
	Profile         string `json:"profile,omitempty"`
	Origin          string `json:"origin"`
	Calls           int64  `json:"calls"`
}

// ErrorPage combines a bounded diagnostic page with retained cause counts.
type ErrorPage struct {
	GroupsTruncated bool         `json:"groups_truncated,omitempty"`
	Version         int          `json:"version"`
	Query           ErrorQuery   `json:"query"`
	Rows            []ErrorRow   `json:"errors"`
	Groups          []ErrorGroup `json:"groups"`
	NextCursor      string       `json:"next_cursor,omitempty"`
	DetailSince     time.Time    `json:"detail_since"`
	Notes           []string     `json:"notes"`
}

type errorCursor struct {
	At           int64
	ID           string
	Since, Until time.Time
	Filter       string
}

func (q ErrorQuery) fingerprint() string {
	raw, _ := json.Marshal([]string{q.Repository, q.Provider, q.Tool, q.Code, q.Client, q.Profile, q.Origin})
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum)
}

const contextFilter = ` AND (?='' OR code=?) AND (?='' OR client=?) AND (?='' OR profile=?) AND (?='' OR origin=?)`

func contextArgs(q ErrorQuery) []any {
	return []any{q.Code, q.Code, q.Client, q.Client, q.Profile, q.Profile, q.Origin, q.Origin}
}

func hasTable(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return n != 0, err
}

// Errors is read-only, bounded and cursor-stable across new writes. Cursors
// freeze the original time window; they cannot be reused with different filters.
func (s *Store) Errors(ctx context.Context, q ErrorQuery) (ErrorPage, error) {
	out := ErrorPage{Version: 1, Rows: []ErrorRow{}, Groups: []ErrorGroup{}, Notes: []string{}, DetailSince: time.Now().UTC().AddDate(0, 0, -7).Truncate(24 * time.Hour)}
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 500 {
		return out, fmt.Errorf("stats errors: limit must be 1..500")
	}
	if q.Origin != "" && q.Origin != "unknown" && q.Origin != "normal" && q.Origin != "synthetic" {
		return out, fmt.Errorf("stats errors: origin must be normal, synthetic or unknown")
	}
	cursor := errorCursor{}
	if q.Cursor != "" {
		if len(q.Cursor) > 4096 {
			return out, fmt.Errorf("stats errors: invalid cursor")
		}
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.ID == "" || cursor.Filter != q.fingerprint() {
			return out, fmt.Errorf("stats errors: invalid cursor or changed filters")
		}
		q.Since, q.Until = cursor.Since, cursor.Until
	}
	if q.Until.IsZero() {
		q.Until = time.Now()
	}
	if err := q.Validate(); err != nil {
		return out, err
	}
	out.Query = q
	if !q.Since.IsZero() && q.Since.Before(out.DetailSince) {
		out.Notes = append(out.Notes, "Individual diagnostics older than detail_since have expired; retained cause aggregates remain available.")
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
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	metadata, err := hasTable(ctx, tx, "event_context")
	if err != nil {
		return out, err
	}
	source := `SELECT events.*, '' client,'' client_version,'' profile,'' provider_version,'' schema_hash,'unknown' origin,'' receipt FROM events`
	if metadata {
		source = `SELECT events.*,coalesce(client,'') client,coalesce(client_version,'') client_version,coalesce(profile,'') profile,coalesce(provider_version,'') provider_version,coalesce(schema_hash,'') schema_hash,coalesce(origin,'unknown') origin,coalesce(receipt,'') receipt FROM events LEFT JOIN event_context ON events.id=event_context.event`
	}
	filter := eventFilter + contextFilter + ` AND level='request' AND ended IS NOT NULL AND ended<=? AND outcome IN ('fail','refused')`
	args := append(eventArgs(q.Query), contextArgs(q)...)
	args = append(args, q.Until.UnixMicro())
	pageFilter := ""
	pageArgs := append([]any{}, args...)
	if cursor.ID != "" {
		pageFilter = ` AND (at<? OR (at=? AND id<?))`
		pageArgs = append(pageArgs, cursor.At, cursor.At, cursor.ID)
	}
	pageArgs = append(pageArgs, q.Limit+1)
	rows, err := tx.QueryContext(ctx, `WITH enriched AS (`+source+`) SELECT at,tool,provider,code,reason,id,repository,outcome,client,client_version,profile,provider_version,schema_hash,origin,receipt FROM enriched WHERE `+filter+pageFilter+` ORDER BY at DESC,id DESC LIMIT ?`, pageArgs...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var row ErrorRow
		var at int64
		if err = rows.Scan(&at, &row.Tool, &row.Provider, &row.Code, &row.Reason, &row.ID, &row.Repository, &row.Outcome, &row.Client, &row.ClientVersion, &row.Profile, &row.ProviderVersion, &row.SchemaHash, &row.Origin, &row.ReceiptID); err != nil {
			return out, errors.Join(err, rows.Close())
		}
		row.At = time.UnixMicro(at)
		row.Reason = Clean(contract.RedactRaw(row.Reason), 240)
		out.Rows = append(out.Rows, row)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	if len(out.Rows) > q.Limit {
		out.Rows = out.Rows[:q.Limit]
		last := out.Rows[len(out.Rows)-1]
		raw, _ := json.Marshal(errorCursor{At: last.At.UnixMicro(), ID: last.ID, Since: q.Since, Until: q.Until, Filter: q.fingerprint()})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	aggregate := `SELECT tool,provider,repository,code,client,profile,origin,client_version,provider_version,schema_hash,count(*) calls FROM enriched WHERE ` + filter + ` GROUP BY 1,2,3,4,5,6,7,8,9,10`
	rollups, err := hasTable(ctx, tx, "context_rollups")
	if err != nil {
		return out, err
	}
	if rollups {
		aggregate += ` UNION ALL SELECT tool,provider,repository,code,client,profile,origin,client_version,provider_version,schema_hash,sum(fail+refused) calls FROM context_rollups WHERE bucket+86400000000>? AND bucket<=? AND (?='' OR repository=?) AND (?='' OR provider=?) AND (?='' OR instr(tool,?)>0)` + contextFilter + ` AND level='request' AND max_ended<=? GROUP BY 1,2,3,4,5,6,7,8,9,10 HAVING sum(fail+refused)>0`
		args = append(args, append(append(eventArgs(q.Query), contextArgs(q)...), q.Until.UnixMicro())...)
		out.Notes = append(out.Notes, "Cause aggregates use UTC day buckets; boundary days may include activity outside a partial-day interval. Older pre-upgrade aggregates have no recoverable cause or client.")
	} else {
		out.Notes = append(out.Notes, "This database predates contextual cause aggregates; only retained diagnostics are available.")
	}
	rows, err = tx.QueryContext(ctx, `WITH enriched AS (`+source+`), grouped AS (`+aggregate+`) SELECT tool,provider,repository,code,client,profile,origin,client_version,provider_version,schema_hash,sum(calls) FROM grouped GROUP BY 1,2,3,4,5,6,7,8,9,10 ORDER BY sum(calls) DESC,tool,provider,repository,code,client,profile,origin,client_version,provider_version,schema_hash LIMIT 1001`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var g ErrorGroup
		if err = rows.Scan(&g.Tool, &g.Provider, &g.Repository, &g.Code, &g.Client, &g.Profile, &g.Origin, &g.ClientVersion, &g.ProviderVersion, &g.SchemaHash, &g.Calls); err != nil {
			return out, errors.Join(err, rows.Close())
		}
		out.Groups = append(out.Groups, g)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if len(out.Groups) > 1000 {
		out.Groups = out.Groups[:1000]
		out.GroupsTruncated = true
		out.Notes = append(out.Notes, "More than 1000 cause groups match; narrow provider, client, profile or cause filters. Detailed error pagination remains available.")
	}
	return out, err
}
