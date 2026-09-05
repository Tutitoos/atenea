package kivgraph

import (
	"context"
	"errors"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// indexFailure is scoped to the index process, never to the MCP transport.
func indexFailure(err error, ctx context.Context) *contract.Failure {
	stop := ctx.Err()
	if stop == nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		stop = err
	}
	if stop != nil {
		failure := contract.Stopped(stop, "kivgraph index", DefaultIndexTimeout)
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
