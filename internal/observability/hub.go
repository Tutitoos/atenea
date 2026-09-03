// Package observability carries the small, safe event stream used by the
// Atenea dashboard. Events are deliberately metadata-only: the stream is a
// live view, not a second receipt store.
package observability

import (
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// DefaultCapacity is the number of recent metadata-only events retained for replay.
const DefaultCapacity = 4096

// Event is the public, metadata-only event shape. Do not add payloads,
// results, discoveries or arbitrary maps here: this type is the privacy
// boundary between orchestration and a browser.
type Event struct {
	Seq            uint64    `json:"seq"`
	At             time.Time `json:"at"`
	Kind           string    `json:"kind"`
	SessionID      string    `json:"session_id,omitempty"`
	RunID          string    `json:"run_id,omitempty"`
	StepID         string    `json:"step_id,omitempty"`
	Capability     string    `json:"capability,omitempty"`
	Implementation string    `json:"implementation,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	Repository     string    `json:"repository,omitempty"`
	State          string    `json:"state,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	Light          string    `json:"light,omitempty"`
	Attempt        int       `json:"attempt,omitempty"`
	Count          int       `json:"count,omitempty"`
	DurationMS     int64     `json:"duration_ms,omitempty"`
	Tokens         int64     `json:"tokens,omitempty"`
}

// Safe returns a copy with untrusted reason text redacted and bounded.
func (e Event) Safe() Event {
	e.Kind = safeText(e.Kind, 80)
	e.SessionID = safeText(e.SessionID, 160)
	e.RunID = safeText(e.RunID, 160)
	e.StepID = safeText(e.StepID, 160)
	e.Capability = safeText(e.Capability, 120)
	e.Implementation = safeText(e.Implementation, 120)
	e.Provider = safeText(e.Provider, 120)
	e.Repository = safeRepository(e.Repository)
	e.State = safeText(e.State, 48)
	e.Light = safeText(e.Light, 48)
	e.Reason = safeText(e.Reason, 240)
	if len(e.Reason) > 240 {
		cut := 240 - len("…")
		for cut > 0 && !utf8.RuneStart(e.Reason[cut]) {
			cut--
		}
		e.Reason = e.Reason[:cut] + "…"
	}
	return e
}

func safeText(raw string, limit int) string {
	value := contract.RedactRaw(strings.TrimSpace(raw))
	if len(value) <= limit {
		return value
	}
	cut := limit - len("…")
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}

func safeRepository(raw string) string {
	value := safeText(raw, 80)
	if value == "" || strings.HasPrefix(value, "~") || strings.ContainsAny(value, `/\\`) || strings.Contains(value, ":") {
		return ""
	}
	return value
}

// Subscription is a replay followed by live events. Reset means the caller's
// cursor was older than the in-memory window and must fetch a new snapshot.
type Subscription struct {
	Replay []Event
	Events <-chan Event
	Reset  bool
	cancel func()
}

// Cancel disconnects the subscriber and releases its event channel.
func (s *Subscription) Cancel() {
	if s != nil && s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

type subscriber struct {
	ch     chan Event
	cancel chan struct{}
}

// Hub is a bounded, non-blocking event fan-out.
type Hub struct {
	capacity uint64
	mu       sync.Mutex
	next     uint64
	ring     []Event
	start    int
	subs     map[*subscriber]struct{}
	closed   atomic.Bool
}

// New creates a bounded event hub, using DefaultCapacity when capacity is not positive.
func New(capacity int) *Hub {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Hub{capacity: uint64(capacity), ring: make([]Event, 0, capacity), subs: make(map[*subscriber]struct{})}
}

// Publish never waits for a browser. A full subscriber is removed; its next
// connection receives a replay or reset from the sequence it last observed.
func (h *Hub) Publish(event Event) Event {
	if h == nil || h.closed.Load() {
		return event
	}
	event = event.Safe()
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	h.mu.Lock()
	h.next++
	event.Seq = h.next
	if uint64(len(h.ring)) < h.capacity {
		h.ring = append(h.ring, event)
	} else {
		h.ring[h.start] = event
		h.start = (h.start + 1) % len(h.ring)
	}
	for sub := range h.subs {
		select {
		case sub.ch <- event:
		default:
			delete(h.subs, sub)
			close(sub.cancel)
			close(sub.ch)
		}
	}
	h.mu.Unlock()
	return event
}

// Subscribe registers atomically with the replay snapshot, so an event cannot
// fall between the cursor check and registration.
func (h *Hub) Subscribe(after uint64) *Subscription {
	if h == nil {
		return &Subscription{Reset: true}
	}
	if h.closed.Load() {
		closed := make(chan Event)
		close(closed)
		return &Subscription{Reset: true, Events: closed}
	}
	h.mu.Lock()
	if h.closed.Load() {
		h.mu.Unlock()
		closed := make(chan Event)
		close(closed)
		return &Subscription{Reset: true, Events: closed}
	}
	replay := make([]Event, 0, len(h.ring))
	reset := false
	if len(h.ring) > 0 {
		ordered := make([]Event, 0, len(h.ring))
		ordered = append(ordered, h.ring[h.start:]...)
		ordered = append(ordered, h.ring[:h.start]...)
		oldest := ordered[0].Seq
		latest := ordered[len(ordered)-1].Seq
		// A cursor ahead of the current sequence is also stale. This happens
		// when the service restarts and the in-memory sequence starts over;
		// waiting silently would leave a reconnecting browser with no snapshot
		// and no live events. Treat both sides of the retained window as a
		// resynchronization request.
		if after != 0 && (after < oldest-1 || after > latest) {
			reset = true
		} else {
			for _, event := range ordered {
				if event.Seq > after {
					replay = append(replay, event)
				}
			}
		}
	} else if after != 0 {
		// No retained event can satisfy a non-zero cursor. The caller must
		// fetch a fresh snapshot before waiting for new events.
		reset = true
	}
	sub := &subscriber{ch: make(chan Event, 256), cancel: make(chan struct{})}
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	return &Subscription{Replay: replay, Events: sub.ch, Reset: reset, cancel: func() { h.remove(sub) }}
}

func (h *Hub) remove(sub *subscriber) {
	if h == nil || sub == nil {
		return
	}
	h.mu.Lock()
	if _, ok := h.subs[sub]; ok {
		delete(h.subs, sub)
		close(sub.cancel)
		close(sub.ch)
	}
	h.mu.Unlock()
}

// Latest returns the newest sequence number published by the hub.
func (h *Hub) Latest() uint64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.next
}

// Close disconnects all subscribers and rejects subsequent publications.
func (h *Hub) Close() {
	if h == nil || h.closed.Swap(true) {
		return
	}
	h.mu.Lock()
	for sub := range h.subs {
		close(sub.cancel)
		close(sub.ch)
		delete(h.subs, sub)
	}
	h.mu.Unlock()
}

// Events returns a copy useful for tests and diagnostics.
func (h *Hub) Events(after uint64) []Event {
	sub := h.Subscribe(after)
	sub.Cancel()
	return slices.Clone(sub.Replay)
}
