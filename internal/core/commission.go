package core

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// commissioned refuses a step whose capability causes an effect the
// commission does not cover. It is the permission gate, and there is one of
// it.
//
// It lives here rather than in the adapters, and that placement is the whole
// point. The same three lines used to be copy-pasted into five of them --
// claudecode, omp, serena and the local stand-in -- with
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

// grounded refuses a step whose repository is not on this machine's disk.
//
// It sits beside the permission gate for the same reason that one does: every
// adapter turns Repository.Path into the child process's working directory,
// and Go reports a missing cmd.Dir by naming the *binary* -- "fork/exec
// /home/me/.local/bin/omp: no such file or directory" for a tool that is
// installed and fine. Measured on a real three-repository commission: one
// wrong path in the settings file sent the operator to reinstall omp.
//
// The bin is the other half. That failure arrived as unavailable, which is the
// bin that condemns a provider: it takes health down for every repository on
// the machine, so the two steps that had nothing wrong with them failed too,
// with "every implementation of code.search is down". invalid_input is what
// this is -- the settings file is wrong, the provider is not -- and the health
// record filters that bin out, so nothing is condemned for it.
//
// A path is required of every repository (contract.Repository.Validate), so
// the empty case is not this gate's business: requests that carry no
// repository at all pass straight through.
type grounded struct{ contract.Runner }

func (g grounded) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	path := strings.TrimSpace(req.Repository.Path)
	if path == "" {
		return g.Runner.Run(ctx, req)
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return contract.Outcome{}, contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %s does not exist", req.Repository.ID, path)
	case err != nil:
		return contract.Outcome{}, contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %s cannot be read", req.Repository.ID, path).
			WithRaw(err.Error())
	case !info.IsDir():
		return contract.Outcome{}, contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %s is not a directory", req.Repository.ID, path)
	}
	return g.Runner.Run(ctx, req)
}
