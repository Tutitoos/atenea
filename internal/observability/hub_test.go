package observability

import (
	"strings"
	"testing"
	"time"
)

func TestHubReplaysInSequence(t *testing.T) {
	h := New(3)
	h.Publish(Event{Kind: "run.started"})
	h.Publish(Event{Kind: "step.started"})
	sub := h.Subscribe(1)
	defer sub.Cancel()
	if len(sub.Replay) != 1 || sub.Replay[0].Seq != 2 {
		t.Fatalf("replay = %#v", sub.Replay)
	}
	h.Publish(Event{Kind: "step.closed"})
	select {
	case got := <-sub.Events:
		if got.Seq != 3 || got.Kind != "step.closed" {
			t.Fatalf("event = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive event")
	}
}

func TestHubReportsResetWhenCursorFallsOutOfWindow(t *testing.T) {
	h := New(2)
	h.Publish(Event{Kind: "one"})
	h.Publish(Event{Kind: "two"})
	h.Publish(Event{Kind: "three"})
	h.Publish(Event{Kind: "four"})
	if sub := h.Subscribe(1); !sub.Reset {
		t.Fatal("old cursor was not reset")
	}
}

func TestHubReportsResetWhenCursorIsAheadAfterRestart(t *testing.T) {
	h := New(2)
	h.Publish(Event{Kind: "one"})
	sub := h.Subscribe(99)
	defer sub.Cancel()
	if !sub.Reset {
		t.Fatal("future cursor was not reset")
	}
	empty := New(2)
	sub = empty.Subscribe(1)
	defer sub.Cancel()
	if !sub.Reset {
		t.Fatal("cursor on an empty window was not reset")
	}
}

func TestHubDropsAFullSubscriberWithoutBlockingPublisher(t *testing.T) {
	h := New(4)
	sub := h.Subscribe(0)
	defer sub.Cancel()
	for i := 0; i < 300; i++ {
		h.Publish(Event{Kind: "tick"})
	}
	if sub2 := h.Subscribe(h.Latest()); sub2.Reset {
		t.Fatal("current cursor unexpectedly reset")
	} else {
		sub2.Cancel()
	}
}

func TestEventReasonIsRedactedAndBounded(t *testing.T) {
	e := Event{Kind: "incident", Reason: "Authorization: Bearer sk-live-abcdef123456 and " + strings.Repeat("x", 5000)}.Safe()
	if strings.Contains(e.Reason, "sk-live") || len(e.Reason) > 4096 {
		t.Fatalf("unsafe reason: %q", e.Reason)
	}
}

func TestEventSafeDropsRepositoryPaths(t *testing.T) {
	e := (Event{Kind: "step.started", Repository: "/Users/private/project"}).Safe()
	if e.Repository != "" {
		t.Fatalf("repository path survived event sanitization: %#v", e)
	}
}
