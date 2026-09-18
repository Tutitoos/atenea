package workflow_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestPartialWriterCanBeReviewed(t *testing.T) {
	for _, verdict := range []string{"failed", "incomplete"} {
		t.Run(verdict, func(t *testing.T) {
			repo, agents := t.TempDir(), t.TempDir()
			source := filepath.Join(repo, "file.txt")
			if err := os.WriteFile(source, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			writer := stub(t, agents, "writer", "printf after > '"+source+"'\necho '{\"result\":{\"ok\":false},\"verdict\":\""+verdict+"\",\"reason\":{\"kind\":\"unavailable\",\"text\":\"partial change\"}}'")
			reviewer, saved := records(t, agents, "judge")
			h := newHarnessWith(t, workflow.Options{Lanes: noCeiling(), Repository: "repo", RepositoryRoot: repo}, "",
				declared("writer", writer, config.PoolAgent, contract.EffectRead, contract.EffectWrite), declared("judge", reviewer, config.PoolReview))
			run, err := h.engine.Start(t.Context(), graphOf(step("w", "writer", nil, contract.EffectRead, contract.EffectWrite), reviewing(step("r", "judge", nil), "w")))
			if err != nil {
				t.Fatalf("validated %s writer aborted workflow before its configured reviewer: %v", verdict, err)
			}
			if _, err := os.Stat(saved); err != nil {
				t.Fatalf("reviewer did not run: %v; states=%v", err, statuses(t, run))
			}
			if got := statuses(t, run)["w"]; got != verdict {
				t.Fatalf("writer status %s, want %s", got, verdict)
			}
			if got := statuses(t, run)["r"]; got != workflow.StatusOK.String() {
				t.Fatalf("reviewer status %s, want ok", got)
			}
			writerRow := stepOf(t, run, "w")
			if writerRow.SourceFingerprint != run.SourceFingerprint {
				t.Fatal("writer and workflow do not record the same observed sources")
			}
		})
	}
}

func TestObservedWriteFingerprintRejectsAcceptedStepWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("writer", answers(t, dir, "writer"), config.PoolAgent, contract.EffectRead, contract.EffectWrite))
	run, _, err := h.engine.Create(t.Context(), graphOf(step("w", "writer", nil, contract.EffectRead, contract.EffectWrite)))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.state.Claim(t.Context(), run.ID, "w", "trace-1", 1, time.Now(), 4242); err != nil {
		t.Fatal(err)
	}
	if err := h.state.Finish(t.Context(), run.ID, "w", workflow.StatusOK,
		contract.Report{Result: map[string]any{"ok": true}, Verdict: contract.VerdictOK}, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.state.SetObservedWriteFingerprint(t.Context(), run.ID, "w", "wrong"); err == nil {
		t.Fatal("accepted step was recorded as a failed observed write")
	}
	after, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SourceFingerprint != before.SourceFingerprint ||
		stepOf(t, after, "w").SourceFingerprint != stepOf(t, before, "w").SourceFingerprint {
		t.Fatal("invalid observed-write call mutated durable source fingerprints")
	}
}
