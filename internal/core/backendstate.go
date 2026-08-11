package core

import (
	"sync"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// BackendState is what the Core remembers about one declared MCP server.
//
// A named string rather than an iota, deliberately. This value answers "is it
// working", it reaches a screen and it crosses to JSON, and an iota pins its
// meaning to declaration order: inserting a state between two existing ones
// silently reinterprets every reading a consumer already learned to map. That
// is the same bug class this whole record exists to end -- a wrong answer here
// is indistinguishable from a right one, because all three words are plausible
// on a status line. Light next door had to grow a name table after the fact
// (status.go:29-41); this one is born with the names as the values.
type BackendState string

const (
	// BackendUnknown means nobody has exercised this backend yet.
	//
	// It is not a synonym for healthy and must never be rendered as one. Until
	// today the two were the same blank space: a server whose tools silently
	// vanished from tools/list looked exactly like a server nobody had asked
	// for. Keeping them apart is the entire point of remembering anything.
	BackendUnknown BackendState = "unknown"

	// BackendOK means the backend answered the last thing Atenea asked it.
	BackendOK BackendState = "ok"

	// BackendFailed means the last thing Atenea asked it did not come back,
	// and Reason carries the cause in the words the process itself used.
	BackendFailed BackendState = "failed"
)

// backendReading is the last thing that happened to one backend: what state it
// left behind, when, and why when the answer is "failed".
//
// One reading per backend rather than a history. A log of every failure is the
// notebook's job and it already exists; what was missing is the current answer
// to "what is the state of this server", which is one row that gets
// overwritten.
type backendReading struct {
	State  BackendState
	At     time.Time
	Reason string
}

// backendMemory remembers the last reading per backend id.
//
// It carries its own RWMutex rather than reusing the Core's mu for two
// reasons, one about contention and one about coupling. tools/list runs from
// every open chat at once and the status screen reads all of these rows at
// once, so the common shape is many readers and a write only when a backend's
// answer changes -- exactly what RWMutex is for, and the Core's plain Mutex
// would serialize a screen against every chat's tool listing. And the Core's
// mu guards the session table and the stopping flag; taking it here would tie
// a backend's health to the lifecycle lock, so a slow read of this map could
// hold up a clean stop.
type backendMemory struct {
	mu       sync.RWMutex
	readings map[string]backendReading
}

func newBackendMemory() *backendMemory {
	return &backendMemory{readings: make(map[string]backendReading)}
}

func (m *backendMemory) record(id string, reading backendReading) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readings[id] = reading
}

// recordBackendListing remembers what happened when a backend was asked for
// its tools.
//
// Any error is failed here, whatever its bin. This seam is defined by its
// consequence rather than by the kind of fault: whatever went wrong, the
// backend's tools did not reach the client, and the client was told nothing.
// A backend that is alive and refuses tools/list has still gone silent from
// where the operator sits, so it belongs on the screen exactly like one that
// never started.
func (c *Core) recordBackendListing(id string, err error) {
	if err == nil {
		c.readings.record(id, backendReading{State: BackendOK, At: time.Now()})
		return
	}
	c.readings.record(id, backendReading{State: BackendFailed, At: time.Now(), Reason: err.Error()})
}

// recordBackendCall remembers what a tools/call proved about the backend, and
// deliberately stays quiet about calls that prove nothing.
//
// Only unavailable and timeout mark a backend failed. The other bins are
// facts about the request, not about the server: FailurePermissionDenied is a
// tool outside this backend's allow list, and FailureInvalidInput is what a
// live backend's own error answer is sorted into -- a server that answers is a
// server that is up. Marking either as failed would put a red light on a
// working process because a model asked it something it does not do, and a
// false red teaches an operator to stop reading the lights.
//
// A call that proves nothing leaves the previous reading alone rather than
// resetting it to unknown: forgetting a real failure because somebody then
// asked for a forbidden tool would lose the one fact worth keeping.
func (c *Core) recordBackendCall(id string, err error) {
	switch {
	case err == nil:
		c.readings.record(id, backendReading{State: BackendOK, At: time.Now()})
	case contract.KindOf(err) == contract.FailureUnavailable,
		contract.KindOf(err) == contract.FailureTimeout:
		c.readings.record(id, backendReading{State: BackendFailed, At: time.Now(), Reason: err.Error()})
	}
}
