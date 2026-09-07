package workflow

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/acceptancefixture"
	"github.com/Tutitoos/atenea/internal/config"
)

func TestPlanProgressUsesTwentySegmentsAndTruncatedPercent(t *testing.T) {
	fixture, active, fixtureErr := acceptancefixture.Load("S10")
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if active {
		text := string(fixture.Bytes)
		accepted, total := strings.Count(text, "- [x]"), strings.Count(text, "- [")
		if total == 0 {
			t.Fatal("sealed progress fixture contains no points")
		}
		fixturePoints := make([]PlanPoint, total)
		for i := range fixturePoints {
			fixturePoints[i] = PlanPoint{ID: fmt.Sprintf("P%02d", i), Title: "fixture", State: PointPending}
			if i < accepted {
				fixturePoints[i].State = PointAccepted
			}
		}
		fixtureProgress := PlanProgress{Points: fixturePoints}
		if fixtureProgress.Percent() != accepted*100/total || len([]rune(fixtureProgress.Bar())) != 20 {
			t.Fatalf("sealed progress rendered incorrectly: %s", fixtureProgress.Markdown("fixture"))
		}
	}
	points := make([]PlanPoint, 19)
	for i := range points {
		points[i] = PlanPoint{ID: fmt.Sprintf("P%02d", i), Title: "Point", State: PointPending}
		if i < 6 {
			points[i].State = PointAccepted
		}
	}
	progress := PlanProgress{Points: points}
	if got := progress.Percent(); got != 31 {
		t.Fatalf("percent = %d, want truncated 31", got)
	}
	if got := progress.Bar(); got != "██████░░░░░░░░░░░░░░" {
		t.Fatalf("bar = %q", got)
	}
	markdown := progress.Markdown("P05 completado")
	if !strings.Contains(markdown, "31 % · 6/19") || strings.Count(markdown, "> - [") != 19 {
		t.Fatalf("markdown did not contain the complete compact checklist:\n%s", markdown)
	}
	if got := (PlanProgress{}).Markdown(""); got != "> **Progreso:** `sin puntos definidos`\n" {
		t.Fatalf("empty progress = %q", got)
	}
}

func TestPointAcceptanceRequiresEveryStepFullAndTraced(t *testing.T) {
	now := time.Now()
	full := 1.0
	partial := 0.5
	rows := []StepRow{
		{Step: Step{PointID: "P06", PointTitle: "Progress"}, Status: StatusOK, TraceID: "impl", Completeness: &full},
		{Step: Step{PointID: "P06", PointTitle: "Progress"}, Pool: config.PoolReview, Status: StatusPending},
	}
	points := derivePlanPoints(rows, nil, now)
	if points[0].State != PointPendingReview {
		t.Fatalf("state = %s, want pending_review", points[0].State)
	}
	rows[1].Status, rows[1].TraceID, rows[1].Completeness = StatusOK, "review", &partial
	points = derivePlanPoints(rows, points, now)
	if points[0].State != PointBlocked {
		t.Fatalf("partial review state = %s, want blocked", points[0].State)
	}
	rows[1].Completeness = &full
	points = derivePlanPoints(rows, points, now)
	if points[0].State != PointAccepted || !strings.Contains(strings.Join(points[0].Evidence, ","), "review") {
		t.Fatalf("accepted point = %+v", points[0])
	}
	retired := derivePlanPoints(nil, points, now)
	if !retired[0].Retired || retired[0].State != PointRetired || (PlanProgress{Points: retired}).Total() != 0 {
		t.Fatalf("retired point = %+v", retired[0])
	}
}

func TestProposalDigestCoversVisiblePointIdentity(t *testing.T) {
	base := Proposal{Steps: []Step{{ID: "work", TypeName: "worker", PointID: "P06", PointTitle: "Progress"}}}
	changed := base.Clone()
	changed.Steps[0].PointTitle = "Different progress"
	if base.Digest() == changed.Digest() {
		t.Fatal("point title changed without invalidating the approval digest")
	}
}
