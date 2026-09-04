package kivgraph

import (
	"context"
	"encoding/json"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// A collector belongs to one Run, never to the shared Runner. Only validated
// scalar metadata crosses into the model-readable receipt.
type evidenceSession struct {
	Session
	evidence []contract.QueryEvidence
}

func (s *evidenceSession) Call(ctx context.Context, tool string, args map[string]any) (string, error) {
	text, err := s.Session.Call(ctx, tool, args)
	if err != nil || tool == "get_source" {
		return text, err
	}
	s.collect(tool, text)
	return text, nil
}

// Invalid optional evidence is omitted; the capability validates its response.
func (s *evidenceSession) collect(tool, text string) {
	var document struct {
		queryEnvelopeMetadata
		SnapshotID int `json:"snapshot_id"`
	}
	if err := json.Unmarshal([]byte(text), &document); err != nil {
		return
	}
	if _, err := queryMetadata(tool, text, false); err != nil {
		return
	}
	e := contract.QueryEvidence{Tool: tool, SnapshotID: document.SnapshotID, Truncated: document.Truncated, NextCursor: document.NextCursor,
		Exact: document.Coverage.Exact, Candidate: document.Coverage.Candidate, UnresolvedRelated: document.Coverage.UnresolvedRelated, PackageLevel: document.Coverage.PackageLevel}
	if tool == toolStatus {
		var status statusAnswer
		if json.Unmarshal([]byte(text), &status) == nil && status.Results != nil {
			e.SnapshotID = status.Results.SnapshotID
			if f := status.Results.ContentFreshness; f != nil {
				if f.Generation > 0 {
					e.ContentGeneration = f.Generation
				}
				switch f.State {
				case "fresh", "stale", "unverified", "unavailable":
					e.Freshness = f.State
				}
			}
		}
	}
	if c := document.Completeness; c != nil {
		e.Completeness = c.Verdict
		e.BlindSpots = len(c.BlindSpots) + c.MoreBlindSpots
		e.InvisibleScopes = len(c.InvisibleScopes) + c.MoreInvisibleScopes
	}
	s.evidence = append(s.evidence, e)
}
