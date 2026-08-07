package core

import (
	"context"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// commissioned refuses a step whose capability causes an effect the
// commission does not cover. It is the permission gate, and there is one of
// it.
//
// It lives here rather than in the adapters, and that placement is the whole
// point. The same three lines used to be copy-pasted into five of them --
// claudecode, codebasememory, omp, serena and the local stand-in -- with
// nothing at all on the core's own dispatch path. The architecture says
// adapters are dumb translators and the brain lives in the core; this is the
// most security-relevant decision in the system, and it was living in five
// translators. Worse, pkg/contract explicitly anticipates adapters supplied
// from outside this repository, and such an adapter enforced nothing
// whatsoever unless its author happened to copy those lines.
//
// Everything but Run is the wrapped Runner's own, the same as guardedRunner:
// an adapter still decides what it serves and what it answers for. Only the
// question of whether it is allowed to answer at all moved.
type commissioned struct{ contract.Runner }

func (c commissioned) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	if missing, ok := req.Allowed(); !ok {
		return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied,
			"%s causes %s, which the commission does not cover", req.Capability.ID, missing)
	}
	return c.Runner.Run(ctx, req)
}
