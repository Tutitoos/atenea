package desktop

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Opening cannot resolve through list_apps: the requested app may be closed.
func (r *Runner) open(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	started := time.Now()
	bundleID, _ := req.Payload["application"].(string)
	if err := r.checkAllowed(bundleID, r.mayActOn(bundleID), "action_applications"); err != nil {
		return contract.Outcome{}, err
	}
	text, err := r.call(ctx, "open", map[string]any{
		"bundle_id": bundleID, "visual_feedback": r.visualFeedback,
	})
	if err != nil {
		return contract.Outcome{}, err
	}
	var answer struct {
		ActionSent  bool   `json:"action_sent"`
		Application string `json:"application"`
		BundleID    string `json:"bundle_id"`
		PID         int    `json:"pid"`
		Frontmost   bool   `json:"frontmost"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"desktop: cannot verify open acknowledgement: %v; observe before another action", err)
	}
	if !answer.ActionSent || answer.BundleID != bundleID || answer.PID <= 0 || answer.Application == "" || !answer.Frontmost {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"desktop: open did not confirm the requested application in the foreground; observe before another action")
	}
	return contract.Outcome{
		Result: map[string]any{
			"did": CapabilityOpen, "action_sent": true,
			"application": answer.Application, "bundle_id": answer.BundleID,
			"pid": answer.PID, "frontmost": true,
		},
		Verdict:       contract.VerdictOK,
		Spent:         contract.Sample{Duration: time.Since(started)},
		SpentUSD:      0,
		SpentUSDKnown: true,
	}, nil
}
