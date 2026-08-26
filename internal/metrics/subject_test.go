package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// blockedOn is one failed fetch of one host, the shape web.fetch writes when a
// site answers with an anti-bot challenge instead of a page.
func blockedOn(at time.Time, subject string) Measurement {
	m := attempt(at, "web.fetch", "scrapling.request")
	m.Subject = subject
	m.OK = false
	m.FailureKind = contract.FailureUnavailable.String()
	m.Failure = "anti-bot challenge rather than the page"
	return m
}

func recordAll(t *testing.T, s *Store, ms ...Measurement) {
	t.Helper()
	for _, m := range ms {
		s.Record(m)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

// The defect this column exists for, reproduced and then closed.
//
// Health was recorded per (capability, repository), and web.fetch ignores the
// repository -- so a run of failures against one site WAS the health of that
// implementation everywhere. Measured through the real funnel before the fix:
// after example.com challenged scrapling.request, a fetch of iana.org went
// straight to the stealth level, and the drop reason still quoted example.com.
func TestFailuresOnOneSubjectDoNotCondemnAnother(t *testing.T) {
	s := store(t, Options{})
	now := time.Now()
	var failures []Measurement
	for i := range 5 {
		failures = append(failures, blockedOn(now.Add(-time.Duration(i)*time.Minute), "blocked.example"))
	}
	recordAll(t, s, failures...)

	blocked := costsForSubject(t, s, "web.fetch", "current", "blocked.example")
	health, said := blocked["scrapling.request"].Health(now)
	if !said {
		t.Fatal("five straight failures on this subject produced no health verdict at all")
	}
	if health.Usable() {
		t.Errorf("health on the blocked subject = %v, want it out of the funnel", health.State)
	}

	// The whole point: another host has its own record, and that record is
	// empty rather than inherited.
	other := costsForSubject(t, s, "web.fetch", "current", "innocent.example")
	if _, said := other["scrapling.request"].Health(now); said {
		t.Error("failures on one host produced a health verdict for another")
	}
}

// A capability that declares no subject writes the empty string, and its
// baseline has to come back exactly as it did before the column existed --
// otherwise every code capability quietly changed how it ranks.
func TestNoSubjectIsItsOwnBucketAndStillWorks(t *testing.T) {
	s := store(t, Options{})
	now := time.Now()
	var failures []Measurement
	for i := range 3 {
		m := attempt(now.Add(-time.Duration(i)*time.Minute), "code.search", "ripgrep")
		m.OK = false
		m.FailureKind = contract.FailureUnavailable.String()
		m.Failure = "ripgrep is not on PATH"
		failures = append(failures, m)
	}
	recordAll(t, s, failures...)

	base := costsForSubject(t, s, "code.search", "current", "")
	if _, said := base["ripgrep"].Health(now); !said {
		t.Error("a capability with no subject lost its own health record")
	}
}

func costsForSubject(t *testing.T, s *Store, capability, repository, subject string) map[string]Baseline {
	t.Helper()
	out, err := s.Baselines(context.Background(), capability, repository, subject)
	if err != nil {
		t.Fatalf("Baselines: %v", err)
	}
	return out
}
