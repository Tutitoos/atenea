package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/knowledge"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// knowledgeCapture turns only an explicit structured implementation result
// into a candidate. Sources, scope, ownership and provider identity come from
// the host workflow; the model cannot widen them.
type knowledgeCapture struct {
	store      *knowledge.Store
	repository string
}

func (c knowledgeCapture) prepare(ctx context.Context, workflowID string, step Step, fingerprint string, report contract.Report) (string, string, error) {
	if c.store == nil || report.Result == nil || report.Verdict != contract.VerdictOK || report.Partial() {
		return "", "", nil
	}
	if step.PointID == "" || step.Route == nil || !strings.EqualFold(strings.TrimSpace(step.Route.Role), "implement") {
		return "", "", nil
	}
	raw, exists := report.Result["knowledge_candidate"]
	if !exists {
		return "", "", nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return "", "", errors.New("workflow: knowledge_candidate must be an object")
	}
	text := func(name string) string { value, _ := values[name].(string); return strings.TrimSpace(value) }
	title, body := text("title"), text("body")
	if title == "" || body == "" || len(title) > 160 || len(body) > 4000 {
		return "", "", errors.New("workflow: knowledge_candidate requires title and body within limits")
	}
	kind := knowledge.Kind(strings.ToLower(text("kind")))
	if kind == "" {
		kind = knowledge.Fact
	}
	visibility := knowledge.Visibility(strings.ToLower(text("visibility")))
	if visibility == "" {
		visibility = knowledge.Private
	}
	if strings.TrimSpace(fingerprint) == "" || fingerprint == "unavailable" {
		return "", "", errors.New("workflow: knowledge candidate requires a verified source fingerprint")
	}
	repository := strings.TrimSpace(c.repository)
	if repository == "" {
		return "", "", errors.New("workflow: knowledge candidate requires repository scope")
	}
	seed, _ := json.Marshal(struct{ Workflow, Point, Kind, Title, Body string }{workflowID, step.PointID, string(kind), title, body})
	sum := sha256.Sum256(seed)
	id := "kc-" + hex.EncodeToString(sum[:10])
	now := time.Now().UTC()
	source := knowledge.Source{ID: "workflow:" + workflowID + ":" + step.PointID, Kind: "workflow", Locator: "workflow/" + workflowID + "/" + step.PointID, Digest: fingerprint}
	dependency := knowledge.Dependency{Source: knowledge.Source{ID: "workflow-state:" + workflowID, Kind: "workflow", Locator: "workflow/status", Digest: fingerprint}, Provider: knowledge.ProviderIdentity{Name: "atenea-workflow", Version: "1", Instance: workflowID, ConfigDigest: fingerprint}, Generation: 1, Snapshot: fingerprint, Freshness: "fresh", CheckedAt: now, TTLSeconds: 600}
	entry := knowledge.Entry{ID: id, Scope: knowledge.Scope{ProjectID: repository, RepositoryID: repository}, OwnerID: "workflow:" + workflowID, Kind: kind, Title: title, Body: body, Visibility: visibility, Sources: []knowledge.Source{source}, Dependencies: []knowledge.Dependency{dependency}}
	entry.KnowledgeDigest = knowledge.KnowledgeDigest(entry)
	permission := knowledge.Permission{SubjectID: entry.OwnerID, ProjectID: repository, RepositoryID: repository, Write: true, ProjectMember: true}
	if err := c.store.PutCandidate(ctx, entry, permission); err != nil {
		return "", "", fmt.Errorf("workflow: creating knowledge candidate: %w", err)
	}
	return id, entry.KnowledgeDigest, nil
}

func (c knowledgeCapture) promote(ctx context.Context, run Run) error {
	if c.store == nil || strings.TrimSpace(run.Repository) == "" {
		return nil
	}
	scope := knowledge.Scope{ProjectID: run.Repository, RepositoryID: run.Repository}
	permission := knowledge.Permission{SubjectID: "workflow:" + run.ID, ProjectID: run.Repository, RepositoryID: run.Repository, Read: true, Write: true, Promote: true, ProjectMember: true}
	entries, err := c.store.Query(ctx, knowledge.Query{Scope: scope, Permission: permission, Statuses: []knowledge.Status{knowledge.Candidate, knowledge.Verified}})
	if err != nil {
		return err
	}
	byID := make(map[string]knowledge.Entry, len(entries))
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	for _, point := range run.Points {
		if point.Retired || point.State != PointAccepted {
			continue
		}
		for _, row := range run.Steps {
			if row.Step.PointID != point.ID || row.Result == nil {
				continue
			}
			candidateID, _ := row.Result["knowledge_candidate_id"].(string)
			digest, _ := row.Result["knowledge_digest"].(string)
			entry, exists := byID[candidateID]
			if !exists || entry.KnowledgeDigest != digest {
				continue
			}
			if entry.Status == knowledge.Verified {
				continue
			}
			if err := c.store.PromoteVerified(ctx, candidateID, knowledge.PromotionGate{WorkflowID: run.ID, Scope: scope, PointID: point.ID}, permission); err != nil {
				return fmt.Errorf("workflow: promoting knowledge candidate: %w", err)
			}
		}
	}
	return nil
}
