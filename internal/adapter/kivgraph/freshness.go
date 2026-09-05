package kivgraph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type contentFreshness struct {
	InputDigest string `json:"input_digest,omitempty"`
	Generation  int    `json:"generation"`
	State       string `json:"state"`
	Detail      string `json:"detail"`
}

func contentFresh(status *statusResult) bool {
	return status != nil && status.Status == "ready" && status.ContentFreshness != nil && status.ContentFreshness.Generation == status.SnapshotID && status.ContentFreshness.State == "fresh"
}

// ensureFresh is the single full-rebuild lane across Atenea processes.
// A failed automatic attempt leaves a generation marker: another ordinary read
// cannot start a blind retry loop. Explicit maintenance may retry.
func (r *Runner) ensureFresh(ctx context.Context, sess Session, status *statusResult, req contract.RunRequest) (*statusResult, bool, error) {
	explicit := req.Capability.ID == CapabilityEnsureFresh
	if contentFresh(status) && !explicit {
		return status, false, nil
	}
	if !explicit && !r.autoReindexRegistered {
		return nil, false, maintenanceFailure("freshness_unverified", "Graph freshness unverified; request graph.ensure_fresh")
	}
	if r.index == nil {
		return nil, false, contract.Fail(contract.FailureUnavailable, "no full-index command configured")
	}
	if status != nil && status.ContentFreshness != nil && status.ContentFreshness.State == "unavailable" {
		return nil, false, maintenanceFailure("freshness_unverified", "Graph inventory unavailable: "+status.ContentFreshness.Detail)
	}
	dir := r.maintenanceDirectory
	if dir == "" {
		return nil, false, contract.Fail(contract.FailureUnavailable, "graph maintenance directory is not configured")
	}
	lock := filepath.Join(dir, "index.lock")
	var release func()
	for {
		var err error
		release, err = pidlock.Claim(lock)
		if err == nil {
			break
		}
		if !errors.Is(err, pidlock.ErrHeld) {
			return nil, false, contract.Fail(contract.FailureUnavailable, "graph rebuild lock: %v", err)
		}
		slog.Info("waiting for shared Kivgraph rebuild", "pid", pidlock.Holder(lock))
		select {
		case <-ctx.Done():
			return nil, false, contract.Fail(contract.FailureTimeout, "shared graph rebuild still pending; no duplicate started")
		case <-time.After(time.Second):
		}
	}
	defer release()
	marker := filepath.Join(dir, "last-attempt")
	// A previous holder may have completed while we waited.
	current, err := r.fetchStatus(ctx, sess)
	if err != nil {
		return nil, false, r.failureFor(err, ctx)
	}
	if contentFresh(current) {
		if explicit {
			// An explicit repair may find that a transient inventory change
			// has already settled. Record that recovery under the same lock,
			// so a later real edit can trigger another automatic full pass.
			if err := os.WriteFile(marker, []byte("verified:"+strconv.Itoa(current.SnapshotID)), 0600); err != nil {
				return nil, false, contract.Fail(contract.FailureUnavailable, "record verified freshness: %v", err)
			}
		}
		return current, false, nil
	}
	if current == nil {
		return nil, false, contract.Fail(contract.FailureUnavailable, "graph status missing")
	}
	if current.ContentFreshness != nil && current.ContentFreshness.State == "unavailable" {
		return nil, false, maintenanceFailure("freshness_unverified", "Graph inventory unavailable: "+current.ContentFreshness.Detail)
	}
	attempted, _ := os.ReadFile(marker)
	generationIdentity := strconv.Itoa(current.SnapshotID)
	identity := generationIdentity
	if current.ContentFreshness != nil && current.ContentFreshness.InputDigest != "" {
		identity += ":" + current.ContentFreshness.InputDigest
	}
	if (string(attempted) == identity || string(attempted) == generationIdentity) && !explicit {
		return nil, false, maintenanceFailure("rebuild_blocked", "automatic graph rebuild already attempted for this generation; inspect the failure and retry explicit graph.ensure_fresh")
	}
	if err := os.WriteFile(marker, []byte(identity), 0600); err != nil {
		return nil, false, contract.Fail(contract.FailureUnavailable, "record graph rebuild attempt: %v", err)
	}
	// No registry writes or index_project registration: only the configured
	// official full-index command can cross this boundary.
	slog.Info("Kivgraph full rebuild started", "repository", req.Repository.ID)
	if err := noteMaintenancePhase(ctx, "indexing"); err != nil {
		return nil, false, err
	}
	report, err := r.index(ctx, req.Repository.Path, "full")
	if err != nil {
		return nil, false, indexFailure(err, ctx)
	}
	if err := noteMaintenancePhase(ctx, "verifying"); err != nil {
		return nil, false, err
	}
	generation, err := strconv.Atoi(report.Generation)
	if err != nil || generation <= current.SnapshotID {
		return nil, false, maintenanceFailure("freshness_unverified", "Full rebuild did not advance the published generation")
	}
	for {
		verified, err := r.fetchStatus(ctx, sess)
		if err != nil {
			return nil, false, r.failureFor(err, ctx)
		}
		if verified != nil && verified.SnapshotID >= generation {
			if verified.SnapshotID != generation || !contentFresh(verified) {
				// Mark the newly published generation too, preventing a loop after a
				// rebuild that could not attest stability.
				if err := os.WriteFile(marker, []byte(strconv.Itoa(verified.SnapshotID)), 0600); err != nil {
					return nil, false, contract.Fail(contract.FailureUnavailable, "record failed freshness verification: %v", err)
				}
				return nil, false, maintenanceFailure("freshness_unverified", fmt.Sprintf("published graph could not verify source freshness (expected generation %d, served %d); results withheld", generation, verified.SnapshotID))
			}
			if err := checkGraphReady(verified, req.Repository.Path); err != nil {
				return nil, false, err
			}
			slog.Info("Kivgraph full rebuild verified", "generation", generation)
			return verified, true, nil
		}
		select {
		case <-ctx.Done():
			return nil, false, contract.Fail(contract.FailureTimeout, "waiting for published graph generation %s: %s", report.Generation, fmt.Sprint(ctx.Err()))
		case <-time.After(time.Second):
		}
	}
}
