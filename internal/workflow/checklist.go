package workflow

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// PointState is part of ATENEA's public orchestration contract.
type PointState string

const (
	// PointPending is part of ATENEA's public orchestration contract.
	PointPending PointState = "pending"
	// PointInProgress is part of ATENEA's public orchestration contract.
	PointInProgress PointState = "in_progress"
	// PointPendingReview is part of ATENEA's public orchestration contract.
	PointPendingReview PointState = "pending_review"
	// PointBlocked is part of ATENEA's public orchestration contract.
	PointBlocked PointState = "blocked"
	// PointAccepted is part of ATENEA's public orchestration contract.
	PointAccepted PointState = "accepted"
	// PointRetired is part of ATENEA's public orchestration contract.
	PointRetired PointState = "retired"
)

// PlanPoint is part of ATENEA's public orchestration contract.
type PlanPoint struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	State    PointState `json:"state"`
	Evidence []string   `json:"evidence"`
	Retired  bool       `json:"retired"`
	Updated  time.Time  `json:"updated_at"`
}

// PlanProgress is part of ATENEA's public orchestration contract.
type PlanProgress struct {
	Revision int         `json:"revision"`
	Points   []PlanPoint `json:"points"`
}

func activityField(value string) string {
	return cleanWords(value, 24)
}

// Accepted is part of ATENEA's public orchestration contract.
func (p PlanProgress) Accepted() int {
	n := 0
	for _, point := range p.Points {
		if !point.Retired && point.State == PointAccepted {
			n++
		}
	}
	return n
}

// Total is part of ATENEA's public orchestration contract.
func (p PlanProgress) Total() int {
	n := 0
	for _, point := range p.Points {
		if !point.Retired {
			n++
		}
	}
	return n
}

// Percent is part of ATENEA's public orchestration contract.
func (p PlanProgress) Percent() int {
	if p.Total() == 0 {
		return 0
	}
	return p.Accepted() * 100 / p.Total()
}

// Bar is part of ATENEA's public orchestration contract.
func (p PlanProgress) Bar() string {
	if p.Total() == 0 {
		return "sin puntos definidos"
	}
	filled := p.Accepted() * 20 / p.Total()
	return strings.Repeat("█", filled) + strings.Repeat("░", 20-filled)
}

// Markdown is part of ATENEA's public orchestration contract.
func (p PlanProgress) Markdown(event string) string {
	var out strings.Builder
	if strings.TrimSpace(event) != "" {
		fmt.Fprintf(&out, "> **ATENEA · %s** — actualizo el plan tras guardar estado y evidencia verificable.\n>\n", activityField(event))
	}
	if p.Total() == 0 {
		out.WriteString("> **Progreso:** `sin puntos definidos`\n")
		return out.String()
	}
	fmt.Fprintf(&out, "> **Progreso:** `%s` %d %% · %d/%d puntos completados\n>\n", p.Bar(), p.Percent(), p.Accepted(), p.Total())
	for _, point := range p.Points {
		box := " "
		if point.State == PointAccepted {
			box = "x"
		}
		suffix := map[PointState]string{
			PointInProgress: " (en curso)", PointPendingReview: " (pendiente de revisión)",
			PointBlocked: " (bloqueado)", PointRetired: " (retirado)",
		}[point.State]
		fmt.Fprintf(&out, "> - [%s] **%s.** %s%s\n", box, activityField(point.ID), activityField(point.Title), suffix)
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func derivePlanPoints(rows []StepRow, prior []PlanPoint, at time.Time) []PlanPoint {
	byID := make(map[string]*PlanPoint)
	order := make([]string, 0)
	retired := make(map[string]bool)
	for _, point := range prior {
		retired[point.ID] = point.Retired
	}
	for _, row := range rows {
		id := row.Step.PointID
		if id == "" {
			continue
		}
		if byID[id] == nil {
			byID[id] = &PlanPoint{ID: id, Title: row.Step.PointTitle, State: PointPending, Retired: retired[id], Updated: at}
			order = append(order, id)
		}
	}
	for _, id := range order {
		point := byID[id]
		if point.Retired {
			point.State = PointRetired
			continue
		}
		allOK, anyStarted, anyBlocked, reviewLeft := true, false, false, false
		var knowledgeDigest string
		for _, row := range rows {
			if row.Step.PointID != id {
				continue
			}
			if row.Status != StatusOK || row.TraceID == "" {
				allOK = false
			}
			if row.Completeness != nil && *row.Completeness < 1 {
				allOK = false
				anyBlocked = true
			}
			if row.Status != StatusPending {
				anyStarted = true
			}
			if row.Status == StatusFailed || row.Status == StatusIncomplete || row.Status == StatusInterrupted {
				anyBlocked = true
			}
			if row.Status == StatusPending && row.Pool.String() == "review" {
				reviewLeft = true
			}
			if row.Status == StatusOK && row.TraceID != "" {
				point.Evidence = append(point.Evidence, row.TraceID)
			}
			if row.Status == StatusOK && row.ResultDigest() != "" {
				if knowledgeDigest == "" {
					knowledgeDigest = row.ResultDigest()
				} else if knowledgeDigest != row.ResultDigest() {
					allOK = false
				}
			}
		}
		if allOK && knowledgeDigest != "" {
			point.Evidence = append(point.Evidence, "knowledge_digest:"+knowledgeDigest)
		}
		slices.Sort(point.Evidence)
		switch {
		case allOK:
			point.State = PointAccepted
		case anyBlocked:
			point.State = PointBlocked
		case reviewLeft && anyStarted:
			point.State = PointPendingReview
		case anyStarted:
			point.State = PointInProgress
		}
	}
	for _, point := range prior {
		if byID[point.ID] != nil {
			continue
		}
		point.Retired = true
		point.State = PointRetired
		point.Updated = at
		byID[point.ID] = &point
		order = append(order, point.ID)
	}
	out := make([]PlanPoint, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}
