// Package toolstats records operator-facing activity independently of routing costs.
package toolstats

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Query selects the time interval and dimensions of an activity snapshot.
type Query struct {
	Since      time.Time `json:"since"`
	Until      time.Time `json:"until"`
	Repository string    `json:"repository,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	Tool       string    `json:"tool,omitempty"`
	Used       bool      `json:"used,omitempty"`
}

// Tool identifies a catalog entry at the request or attempt level.
type Tool struct {
	ProviderVersion   string     `json:"provider_version,omitempty"`
	SchemaHash        string     `json:"schema_hash,omitempty"`
	CatalogObservedAt *time.Time `json:"catalog_observed_at,omitempty"`
	Level             string     `json:"level"`
	Name              string     `json:"name"`
	Provider          string     `json:"provider"`
	Repository        string     `json:"repository,omitempty"`
	State             string     `json:"state"`
}

// Row contains outcome counters and duration statistics for a tool.
type Row struct {
	Tool
	Calls      int64      `json:"calls"`
	OK         int64      `json:"ok"`
	Refused    int64      `json:"refused"`
	Fail       int64      `json:"fail"`
	Cancel     int64      `json:"cancel"`
	Active     int64      `json:"active"`
	OKPercent  *float64   `json:"ok_percent"`
	MeanUS     *int64     `json:"mean_us"`
	P95US      *int64     `json:"p95_us"`
	MaxUS      *int64     `json:"max_us"`
	Last       *time.Time `json:"last"`
	SumUS      int64      `json:"-"`
	Samples    int64      `json:"-"`
	Summarized bool       `json:"-"`
}

// Diagnostic describes a recent failure or refusal without payload data.
type Diagnostic struct {
	At       time.Time `json:"at"`
	Tool     string    `json:"tool"`
	Provider string    `json:"provider"`
	Code     string    `json:"code"`
	Reason   string    `json:"reason"`
}

// Coverage describes retained history and known recording gaps.
type Coverage struct {
	Started        *time.Time `json:"started"`
	EffectiveSince *time.Time `json:"effective_since,omitempty"`
	EffectiveUntil *time.Time `json:"effective_until,omitempty"`
	DetailSince    time.Time  `json:"detail_since"`
	Dropped        int64      `json:"dropped"`
	Partial        bool       `json:"partial"`
	Notes          []string   `json:"notes"`
}

// Snapshot is the shared socket and CLI statistics response.
type Snapshot struct {
	Version     int          `json:"version"`
	At          time.Time    `json:"at"`
	Query       Query        `json:"query"`
	Source      string       `json:"source"`
	Service     string       `json:"service"`
	Catalog     []Tool       `json:"catalog"`
	Rows        []Row        `json:"rows"`
	Totals      []Row        `json:"totals"`
	Errors      []Diagnostic `json:"errors"`
	Coverage    Coverage     `json:"coverage"`
	Legacy      []LegacyRow  `json:"legacy"`
	LegacyError string       `json:"legacy_error,omitempty"`
}

// LegacyRow reports routing metrics that predate observability recording.
type LegacyRow struct {
	Tool         string `json:"tool"`
	Provider     string `json:"provider"`
	Repository   string `json:"repository"`
	Calls        int64  `json:"calls"`
	OK           int64  `json:"ok"`
	Unclassified int64  `json:"unclassified_failures"`
	MeanUS       *int64 `json:"mean_us"`
	MaxUS        *int64 `json:"max_us"`
}

// Event identifies a request or attempt and its parent request.
type Event struct {
	Metadata   Metadata
	ID         string
	Parent     string
	Level      string
	Tool       string
	Provider   string
	Repository string
	At         time.Time
}

type requestKey struct{}

// RequestID returns the original request identifier from the context.
func RequestID(ctx context.Context) string { id, _ := ctx.Value(requestKey{}).(string); return id }

// WithRequest attaches an original request identifier to the context.
func WithRequest(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestKey{}, id)
}

// Outcome classifies an error and returns its bounded diagnostic.
func Outcome(err error) (string, string, string) {
	if err == nil {
		return "ok", "", ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancel", "canceled", Clean(err.Error(), 240)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "fail", "timeout", Clean(err.Error(), 240)
	}
	code := contract.CodeOf(err)
	outcome := "fail"
	switch contract.KindOf(err) {
	case contract.FailurePermissionDenied, contract.FailureExternalDenied:
		outcome = "refused"
	case contract.FailureCanceled:
		outcome = "cancel"
	}
	return outcome, code, Clean(err.Error(), 240)
}

// Clean strips terminal control sequences, not just their ESC prefix.
func Clean(s string, limit int) string {
	var b strings.Builder
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		if r[i] == 27 {
			i++
			if i < len(r) && r[i] == '[' {
				for i++; i < len(r) && (r[i] < 0x40 || r[i] > 0x7e); i++ {
				}
			} else if i < len(r) && r[i] == ']' {
				for i++; i < len(r) && r[i] != 7; i++ {
					if r[i] == 27 && i+1 < len(r) && r[i+1] == '\\' {
						i++
						break
					}
				}
			}
			continue
		}
		if unicode.IsControl(r[i]) {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r[i])
	}
	out := []rune(strings.Join(strings.Fields(b.String()), " "))
	if len(out) > limit {
		return string(out[:limit]) + "..."
	}
	return string(out)
}
