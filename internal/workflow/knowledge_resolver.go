package workflow

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/knowledge"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// KnowledgeEvidenceResolver adapts persisted workflow acceptance state to the
// knowledge promotion gate. It never accepts caller-supplied receipts: the
// run, point, review and audit rows are read from the workflow store.
type KnowledgeEvidenceResolver struct {
	Store *Store
	TTL   time.Duration
}

// Resolve is part of ATENEA's public orchestration contract.
func (r KnowledgeEvidenceResolver) Resolve(ctx context.Context, workflowID string, scope knowledge.Scope, pointID, knowledgeDigest string) (knowledge.EvidenceBundle, error) {
	if r.Store == nil {
		return knowledge.EvidenceBundle{}, errors.New("workflow: knowledge resolver has no store")
	}
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(knowledgeDigest) == "" {
		return knowledge.EvidenceBundle{}, errors.New("workflow: workflow id and knowledge digest are required")
	}
	run, err := r.Store.Load(ctx, workflowID)
	if err != nil {
		return knowledge.EvidenceBundle{}, err
	}
	if run.Repository != scope.RepositoryID {
		return knowledge.EvidenceBundle{}, errors.New("workflow: knowledge scope repository mismatch")
	}
	var point *PlanPoint
	for i := range run.Points {
		if run.Points[i].ID == pointID {
			point = &run.Points[i]
			break
		}
	}
	if point == nil || point.Retired || point.State != PointAccepted || len(point.Evidence) == 0 {
		return knowledge.EvidenceBundle{}, errors.New("workflow: point is not accepted with evidence")
	}
	if !containsExact(point.Evidence, knowledgeDigest) && !containsExact(point.Evidence, "knowledge_digest:"+knowledgeDigest) {
		return knowledge.EvidenceBundle{}, errors.New("workflow: accepted point is bound to another knowledge digest")
	}
	var implementation, review, audit *StepRow
	for i := range run.Steps {
		row := &run.Steps[i]
		if row.Step.PointID != pointID {
			continue
		}
		if row.Status != StatusOK || row.TraceID == "" || !row.InvokedKnown || !row.Invoked {
			if row.Status == StatusOK && row.TraceID != "" && (row.InvokedKnown || row.Invoked) {
				return knowledge.EvidenceBundle{}, errors.New("workflow: accepted evidence contains failed invocation")
			}
			continue
		}
		report := row.Report()
		if report.Verdict != contract.VerdictOK || report.Partial() || report.Reason.Kind != contract.FailureUnspecified {
			return knowledge.EvidenceBundle{}, errors.New("workflow: accepted evidence contains failed or partial report")
		}
		route := row.Step.Route
		if route == nil || strings.TrimSpace(route.RequestedModel) == "" || strings.TrimSpace(route.ObservedModel) == "" || route.RequestedModel != route.ObservedModel || strings.TrimSpace(route.RequestedReasoningEffort) == "" || strings.TrimSpace(route.ObservedReasoningEffort) == "" || route.RequestedReasoningEffort != route.ObservedReasoningEffort || strings.TrimSpace(route.Backend) == "" || strings.TrimSpace(route.Role) == "" {
			return knowledge.EvidenceBundle{}, errors.New("workflow: accepted evidence lacks observed route identity")
		}
		role := strings.ToLower(strings.TrimSpace(route.Role))
		if (role == "review" || role == "audit") && len(report.Result) == 0 {
			return knowledge.EvidenceBundle{}, errors.New("workflow: accepted review or audit has an empty report")
		}
		switch {
		case row.Pool == config.PoolAgent && row.Step.Subject == "" && role == "implement":
			if implementation != nil {
				return knowledge.EvidenceBundle{}, errors.New("workflow: ambiguous implementation chain")
			}
			implementation = row
		case row.Pool == config.PoolReview && role == "review":
			if review != nil {
				return knowledge.EvidenceBundle{}, errors.New("workflow: ambiguous review chain")
			}
			review = row
		case row.Pool == config.PoolReview && role == "audit":
			if audit != nil {
				return knowledge.EvidenceBundle{}, errors.New("workflow: ambiguous audit chain")
			}
			audit = row
		}
	}
	if implementation == nil || review == nil || audit == nil || review.Step.Subject != implementation.Step.ID || audit.Step.Subject != review.Step.ID {
		return knowledge.EvidenceBundle{}, errors.New("workflow: accepted point lacks complete implementation/review/audit rows")
	}
	if !containsExact(point.Evidence, implementation.TraceID) || !containsExact(point.Evidence, review.TraceID) || !containsExact(point.Evidence, audit.TraceID) {
		return knowledge.EvidenceBundle{}, errors.New("workflow: accepted point evidence does not contain the complete trace chain")
	}
	if implementation.ResultDigest() != knowledgeDigest {
		return knowledge.EvidenceBundle{}, errors.New("workflow: implementation report knowledge digest mismatch")
	}
	tree := strings.TrimSpace(implementation.SourceFingerprint)
	if tree == "" {
		return knowledge.EvidenceBundle{}, errors.New("workflow: accepted implementation has no source fingerprint")
	}
	if review.SourceFingerprint != tree || audit.SourceFingerprint != tree {
		return knowledge.EvidenceBundle{}, errors.New("workflow: accepted evidence was produced from different source fingerprints")
	}
	now := run.Started
	if now.IsZero() {
		now = time.Now().UTC()
	}
	ttl := r.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	source := knowledge.Source{ID: "workflow:" + workflowID + ":" + pointID, Kind: "workflow", Locator: "workflow/" + workflowID + "/" + pointID, Digest: tree}
	dependency := knowledge.Dependency{Source: knowledge.Source{ID: "workflow-state:" + workflowID, Kind: "workflow", Locator: "workflow/status", Digest: tree}, Provider: knowledge.ProviderIdentity{Name: "atenea-workflow", Version: implementation.Step.Route.ObservedModel, Instance: workflowID, ConfigDigest: tree}, Generation: 1, Snapshot: tree, Freshness: "fresh", CheckedAt: now, TTLSeconds: int64(ttl / time.Second)}
	makeReceipt := func(kind knowledge.ReceiptKind, name string, row *StepRow) knowledge.EvidenceReceipt {
		route := row.Step.Route
		receipt := knowledge.EvidenceReceipt{ID: name + ":" + workflowID + ":" + pointID, Kind: kind, PointID: pointID, Scope: scope, TreeDigest: tree, Model: route.ObservedModel, RequestedModel: route.RequestedModel, RequestedEffort: route.RequestedReasoningEffort, ObservedEffort: route.ObservedReasoningEffort, Backend: route.Backend, Role: route.Role, Complete: true, Approved: true, KnowledgeDigest: knowledgeDigest, Sources: []knowledge.Source{source}, Dependencies: []knowledge.Dependency{dependency}}
		receipt.Digest = knowledge.EvidenceDigest(receipt)
		return receipt
	}
	return knowledge.EvidenceBundle{Scope: scope, PointID: pointID, TreeDigest: tree, KnowledgeDigest: knowledgeDigest, Acceptance: makeReceipt(knowledge.AcceptanceReceipt, "acceptance", implementation), SolReview: makeReceipt(knowledge.SolReviewReceipt, "sol-review", review), AstraAudit: makeReceipt(knowledge.AstraAuditReceipt, "astra-audit", audit)}, nil
}

func containsExact(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// ResultDigest is part of ATENEA's public orchestration contract.
func (s StepRow) ResultDigest() string {
	if s.Result == nil {
		return ""
	}
	value, ok := s.Result["knowledge_digest"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}
