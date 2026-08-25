package agent

import (
	"fmt"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// deathf builds the error every failed spawn returns: a Failure whose bin
// says which kind of death it was.
//
// There is one constructor because there is one rule. Every path out of
// execute that is not a validated answer ends here, and every one of them is
// closed as incomplete by the caller. Spreading the construction around is
// how a `failed` would eventually appear in one of them.
func deathf(kind contract.FailureKind, format string, args ...any) error {
	return contract.Fail(kind, format, args...)
}

// died turns a death into the reason recorded on the trace row.
//
// The bin travels; the verdict does not, because the verdict is always the
// same. Nobody saw this run finish, so nobody may say it failed.
//
// The text is redacted on its way to the row, because this is the point where
// it stops being an in-memory error and becomes a durable record. Every death
// message this package builds folds in whatever the child printed -- see note
// -- and a child is any binary an agent type declares, so an API key echoed
// into a failing tool's stderr would otherwise land verbatim in the
// agent_trace reason_text column and stay there. The other two durable stores
// already do this at the same boundary: internal/metrics and
// internal/checkpoint both call contract.RedactRaw before writing, and the
// trace base was the one that did not. RedactRaw also bounds the text at
// contract.MaxPersistedRaw, which is the ceiling the other two are held to.
func died(err error) contract.Reason {
	kind := contract.KindOf(err)
	if kind == contract.FailureUnspecified {
		// An error nothing sorted still has to be filed. Unavailable is the
		// honest reading: the thing that was going to answer did not.
		kind = contract.FailureUnavailable
	}
	return contract.Reason{Kind: kind, Text: contract.RedactRaw(contract.MessageOf(err))}
}

// intersectLevels keeps the levels a type declared that its parent may also
// see, in the type's order.
func intersectLevels(declared, parent []contract.ContextLevel) []contract.ContextLevel {
	out := make([]contract.ContextLevel, 0, len(declared))
	for _, level := range declared {
		if slices.Contains(parent, level) {
			out = append(out, level)
		}
	}
	return out
}

func sortStrings(s []string) { slices.Sort(s) }

// defaultIDs mints execution ids that sort by time and cannot collide inside
// one process.
//
// Unique per execution is the contract's word, and this is the cheap way to
// honor it without a uuid dependency: the second half is a counter, so two
// agents started in the same nanosecond still differ. Two Ateneas started in
// the same nanosecond could in principle collide, and the trace store's
// primary key is what would catch it -- a collision refuses the second Begin
// rather than overwriting the first row.
func defaultIDs() func() string {
	var counter atomic.Uint64
	return func() string {
		n := counter.Add(1)
		return fmt.Sprintf("a%s-%s",
			strconv.FormatInt(time.Now().UTC().UnixNano(), 36),
			strconv.FormatUint(n, 36))
	}
}
