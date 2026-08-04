// Package supervisor keeps an MCP server alive on Atenea's own behalf.
//
// Until now every MCP adapter has assumed something else already started its
// far side: Serena behind a hand-run proxy, reachable at a fixed endpoint.
// That is still the default and still works unchanged. This package is for
// the alternative -- Atenea launches the server itself, as a bare process,
// watches it, and revives it a couple of times if it falls over, exactly as
// decided for the break-in rotation elsewhere in this design: no infinite
// retry, no silent redefinition of "down".
//
// The scope is one Atenea process, not the machine. A server this package
// starts is stopped when this process stops; nothing here writes a PID file
// for a later invocation to adopt, and nothing here reaches for a process
// that some earlier invocation left running. That mirrors how every other
// piece of state in Atenea already works -- the run receipts, the
// measurement base, the crash notebook -- except that a live MCP server is
// computation, not a file on disk, and stretching its lifetime across
// independent processes would need a lease and an orphan sweep this design
// deliberately does not take on.
//
// Two lifecycles are supported. A persistent server is warmed up when the
// service starts and stays up until it stops; an on_demand server starts on
// first use and is stopped by the idle reaper once nothing has asked for it
// in a while. Both get the same crash handling: a bounded number of restart
// attempts, a pause between them, and a server that still will not stay up is
// marked down for the rest of this process's life -- there is no command here
// to revive it by hand, the same way there is none to force a provider's
// health back to green.
package supervisor

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Lifecycle decides who may stop a server once it is up.
type Lifecycle string

const (
	// Persistent servers are warmed up when the service starts and run
	// until this process stops. The idle reaper never touches them.
	Persistent Lifecycle = "persistent"
	// OnDemand servers start lazily on first use and are stopped by the
	// idle reaper once nothing has been in flight for IdleTimeout.
	OnDemand Lifecycle = "on_demand"
)

// The defaults every zero-valued Spec field falls back to. They live here
// rather than only in internal/config for the same reason
// internal/adapter/serena keeps its own DefaultTimeout: a caller building a
// Spec by hand -- a test, or some future caller that is not the settings
// file -- gets a working server without having to know these numbers too.
const (
	// DefaultReadyTimeout bounds one spawn attempt's wait for the readiness
	// probe. Fifteen seconds is generous for a language server indexing
	// nothing yet -- it has no repository activated at startup -- and short
	// enough that a server that will never come up does not stall the first
	// commission that needed it.
	DefaultReadyTimeout = 15 * time.Second
	// DefaultRestartLimit is how many times a crash is retried before the
	// server is marked down for good: "a couple of times", the design's own
	// words for the break-in rotation this mirrors.
	DefaultRestartLimit = 2
	// DefaultStableAfter is how long a server must stay ready before a
	// later crash is treated as a fresh problem with its own restart
	// budget, rather than a continuation of the last one.
	//
	// Without this, a server that flickers ready for a moment and dies
	// keeps resetting its own restart count on every brief success and
	// never runs out of retries -- a real crash loop, retried forever,
	// which is exactly the failure a restart budget exists to catch. Half
	// a minute is long enough that staying up for it is a meaningful sign
	// of health, not a lucky probe landing between two crashes.
	DefaultStableAfter = 30 * time.Second
	// DefaultRestartDelay is the pause between a crash and the next
	// attempt -- short, because by the time this runs the decision to retry
	// is already made and there is nothing to wait for except giving
	// whatever killed the server a moment to clear.
	DefaultRestartDelay = 2 * time.Second
	// DefaultIdleTimeout is how long an on_demand server may sit with
	// nothing in flight before the reaper stops it.
	DefaultIdleTimeout = 5 * time.Minute
	// DefaultStopGrace bounds how long a deliberate stop waits after
	// SIGTERM before escalating to SIGKILL.
	DefaultStopGrace = 5 * time.Second
	// defaultHost is the address a server listens on when Spec does not say
	// otherwise. Never wide open by default, the same posture the rest of
	// Atenea takes with anything that binds a socket.
	defaultHost = "127.0.0.1"
	// probeEvery is how often a spawn attempt polls for readiness.
	probeEvery = 150 * time.Millisecond
)

// idleCheckEvery is how often the reaper looks for an on_demand server that
// has earned a stop. Not configurable by users -- unlike the rhythms in the
// settings file, retuning it changes nothing a user would notice, only how
// promptly an idle server's memory is given back -- but a var rather than a
// const so tests can shrink it instead of a reaper test waiting out a real
// 30 seconds.
var idleCheckEvery = 30 * time.Second

// Spec describes one MCP server Atenea may launch and keep alive.
type Spec struct {
	// ID names the server for the status screen and for EnsureReady. It
	// matches the runner name it backs, e.g. "serena".
	ID string
	// Command and Args launch the server. A bare Command is looked up on
	// PATH. An element of Args equal to "{{port}}" is replaced with the
	// chosen port before every spawn.
	Command string
	Args    []string
	// Env adds to, rather than replaces, the inherited environment: entries
	// are "KEY=VALUE" pairs appended after os.Environ(), so a server that
	// needs PATH or HOME still gets them.
	Env []string
	// Lifecycle decides who may stop the server. There is no default: a
	// Spec with Command set and Lifecycle empty is rejected by New rather
	// than silently guessing which of the two very different behaviors --
	// always warm, or stopped when idle -- was meant.
	Lifecycle Lifecycle
	// Host and Port are where the server listens. Port zero asks the OS for
	// a free one, chosen once in New and then fixed for the life of the
	// Supervisor: the endpoint an adapter is built with must never go stale
	// under it, including across an on_demand server's idle stop and later
	// restart.
	Host string
	Port int
	// EndpointPath is appended to host:port to build the URL Endpoint
	// returns and the probe calls.
	EndpointPath string
	// IdleTimeout is how long an OnDemand server may sit with nothing in
	// flight before the reaper stops it. Ignored for Persistent.
	IdleTimeout time.Duration
	// ReadyTimeout bounds one spawn attempt's wait for the readiness probe.
	ReadyTimeout time.Duration
	// RestartLimit is how many times a crash may be retried before the
	// server is marked down for good. Zero is a legitimate choice -- never
	// retry, go straight to down -- so it is distinguished from "not set"
	// before this Spec is built, not here.
	RestartLimit int
	// StableAfter is how long a server must stay ready before a later
	// crash earns a fresh restart budget instead of continuing the last
	// one. See DefaultStableAfter for why this exists at all.
	StableAfter time.Duration
	// RestartDelay is the pause between a crash and the next attempt.
	RestartDelay time.Duration
	// StopGrace bounds how long a deliberate stop waits after SIGTERM
	// before escalating to SIGKILL.
	StopGrace time.Duration
	// HTTP is the client the readiness probe uses. Nil uses a default one;
	// tests point this at whatever they need.
	HTTP *http.Client
}

// withDefaults returns a copy of s with every zero-valued optional field
// filled in, and reports the endpoint it will listen on once a port is
// chosen. It does not choose the port itself: New does that once, and every
// other reader of a *process gets it from there, never recomputed.
func (s Spec) withDefaults() (Spec, error) {
	if s.ID == "" {
		return Spec{}, fmt.Errorf("supervisor: a spec has no ID")
	}
	if s.Command == "" {
		return Spec{}, fmt.Errorf("supervisor: spec %q has no command", s.ID)
	}
	switch s.Lifecycle {
	case Persistent, OnDemand:
	default:
		return Spec{}, fmt.Errorf("supervisor: spec %q lifecycle must be %q or %q, got %q",
			s.ID, Persistent, OnDemand, s.Lifecycle)
	}
	if s.RestartLimit < 0 {
		return Spec{}, fmt.Errorf("supervisor: spec %q restart limit must not be negative, got %d",
			s.ID, s.RestartLimit)
	}
	if s.Host == "" {
		s.Host = defaultHost
	}
	if s.IdleTimeout <= 0 {
		s.IdleTimeout = DefaultIdleTimeout
	}
	if s.ReadyTimeout <= 0 {
		s.ReadyTimeout = DefaultReadyTimeout
	}
	if s.RestartDelay <= 0 {
		s.RestartDelay = DefaultRestartDelay
	}
	if s.StableAfter <= 0 {
		s.StableAfter = DefaultStableAfter
	}
	if s.StopGrace <= 0 {
		s.StopGrace = DefaultStopGrace
	}
	if s.HTTP == nil {
		s.HTTP = &http.Client{}
	}
	return s, nil
}

// State is where a process is in its lifecycle.
type State uint8

const (
	// StateStopped means no child is running: either never started, or
	// stopped on purpose. The next EnsureReady starts it fresh.
	StateStopped State = iota
	// StateStarting means a child was spawned and has not yet answered the
	// readiness probe.
	StateStarting
	// StateReady means the last probe succeeded and the server is serving.
	StateReady
	// StateRestarting means a spawn attempt ended and a retry is pending,
	// paused for RestartDelay.
	StateRestarting
	// StateDown means the restart budget ran out. Terminal until this
	// Atenea process exits.
	StateDown
)

func (s State) String() string {
	switch s {
	case StateStopped:
		return "stopped"
	case StateStarting:
		return "starting"
	case StateReady:
		return "ready"
	case StateRestarting:
		return "restarting"
	case StateDown:
		return "down"
	default:
		return "unknown"
	}
}

// Status is one server's condition, for the status screen.
type Status struct {
	ID       string
	State    State
	Endpoint string
	PID      int
	Port     int
	Started  time.Time
	Restarts int
	// LastReason is what the most recent failed attempt said, empty when
	// the server has never failed to start or stay up.
	LastReason string
}

// Supervisor owns every server Atenea was told to launch. It is safe for
// concurrent use.
type Supervisor struct {
	procs    map[string]*process
	order    []string // registration order, so Status is stable to read
	reaper   sync.Once
	stopOnce sync.Once
	stopped  chan struct{}
}

// New validates specs and returns a Supervisor that has not spawned
// anything yet. Every port a Spec left at zero is chosen here, once, so
// Endpoint is stable from this call onward.
func New(specs ...Spec) (*Supervisor, error) {
	s := &Supervisor{
		procs:   make(map[string]*process, len(specs)),
		stopped: make(chan struct{}),
	}
	for _, spec := range specs {
		built, err := spec.withDefaults()
		if err != nil {
			return nil, err
		}
		if _, dup := s.procs[built.ID]; dup {
			return nil, fmt.Errorf("supervisor: spec %q is registered twice", built.ID)
		}
		port, err := choosePort(built.Host, built.Port)
		if err != nil {
			return nil, fmt.Errorf("supervisor: spec %q: %w", built.ID, err)
		}
		built.Port = port
		s.procs[built.ID] = newProcess(built)
		s.order = append(s.order, built.ID)
	}
	return s, nil
}

// find looks a process up by id, reporting the same "not registered" fact
// every exported method needs.
func (s *Supervisor) find(id string) (*process, error) {
	p, ok := s.procs[id]
	if !ok {
		return nil, fmt.Errorf("supervisor: no server registered as %q", id)
	}
	return p, nil
}

// Endpoint returns the URL id will listen on, without starting it. The value
// never changes after New: the port was chosen there.
func (s *Supervisor) Endpoint(id string) (string, error) {
	p, err := s.find(id)
	if err != nil {
		return "", err
	}
	return p.endpoint, nil
}

// EnsureReady starts id if it is not already running and blocks until it is
// ready, it is down, or ctx is done. It is safe to call concurrently: every
// caller waiting on the same server observes the same attempt.
func (s *Supervisor) EnsureReady(ctx context.Context, id string) (string, error) {
	p, err := s.find(id)
	if err != nil {
		return "", err
	}
	return p.ensureReady(ctx)
}

// Acquire and Release bracket one call against id, so the idle reaper never
// stops a server while something is using it.
func (s *Supervisor) Acquire(id string) {
	if p, err := s.find(id); err == nil {
		p.acquire()
	}
}

func (s *Supervisor) Release(id string) {
	if p, err := s.find(id); err == nil {
		p.release()
	}
}

// WarmUp starts every Persistent server without waiting for any of them,
// so a slow one does not hold up the others. Failures are not returned:
// they land on that server's own status, exactly like a crash discovered any
// other way.
func (s *Supervisor) WarmUp(ctx context.Context) {
	for _, id := range s.order {
		p := s.procs[id]
		if p.spec.Lifecycle != Persistent {
			continue
		}
		go func(p *process) { _, _ = p.ensureReady(ctx) }(p)
	}
}

// Start begins the idle reaper for OnDemand servers, until ctx is done.
// Calling it more than once is a no-op: there is one reaper for the life of
// this Supervisor, the same shape as clock.Clock.Start.
func (s *Supervisor) Start(ctx context.Context) {
	hasOnDemand := false
	for _, id := range s.order {
		if s.procs[id].spec.Lifecycle == OnDemand {
			hasOnDemand = true
			break
		}
	}
	if !hasOnDemand {
		return
	}
	s.reaper.Do(func() { go s.reap(ctx) })
}

func (s *Supervisor) reap(ctx context.Context) {
	ticker := time.NewTicker(idleCheckEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopped:
			return
		case <-ticker.C:
			for _, id := range s.order {
				p := s.procs[id]
				if p.spec.Lifecycle == OnDemand {
					p.stopIfIdle()
				}
			}
		}
	}
}

// Stop asks every server to stop, waiting for each -- up to its own
// StopGrace plus a margin for the kill to land -- and returns once all of
// them have. Calling it more than once is safe; the second call finds
// everything already stopped or down and returns at once.
func (s *Supervisor) Stop() {
	s.stopOnce.Do(func() { close(s.stopped) })
	var wg sync.WaitGroup
	for _, id := range s.order {
		p := s.procs[id]
		wg.Add(1)
		go func(p *process) {
			defer wg.Done()
			p.stop()
		}(p)
	}
	wg.Wait()
}

// Status reports every server's condition, in registration order.
func (s *Supervisor) Status() []Status {
	out := make([]Status, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.procs[id].status())
	}
	return out
}
