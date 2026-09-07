package kivgraph

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// indexFailure is scoped to the index process, never to the MCP transport.
// configuredTimeout is supplied by Runner when available. Keeping it outside
// the context avoids losing the configured value after a deadline expires.
func indexFailure(err error, ctx context.Context, configuredTimeout ...time.Duration) *contract.Failure {
	stop := ctx.Err()
	if stop == nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		stop = err
	}
	if stop != nil {
		timeout := effectiveIndexTimeout(ctx)
		if len(configuredTimeout) > 0 && configuredTimeout[0] > 0 {
			timeout = configuredTimeout[0]
		}
		failure := contract.Stopped(stop, "kivgraph index", timeout)
		failure.HealthNeutral = true
		return failure
	}
	code := "index_worker_failed"
	reason := contract.RedactRaw(err.Error())
	if strings.Contains(strings.ToLower(reason), "source inventory changed") {
		code = "inventory_changed"
		reason = "Kivgraph source files changed during indexing; no fresh generation published; pause edits before retrying"
	}
	return maintenanceFailure(code, reason)
}

func effectiveIndexTimeout(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < DefaultIndexTimeout {
			return remaining
		}
	}
	return DefaultIndexTimeout
}
