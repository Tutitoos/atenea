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
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
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

// Transport is how a server this package started is reached once it is up.
type Transport string

const (
	// TransportHTTP is streamable-HTTP: the server listens on Host:Port and
	// speaks the wire format internal/adapter/serena/mcp.go and this
	// package's own probe already speak. It is the zero value, so every
	// Spec written before stdio existed keeps behaving exactly as it did.
	TransportHTTP Transport = "http"
	// TransportStdio is JSON-RPC over the child's own stdin and stdout, the
	// way internal/mcpstdio and internal/passthrough already speak it. A
	// stdio server listens on nothing: Host, Port and EndpointPath are
	// meaningless for it, and withDefaults rejects a spec that sets any of
	// them rather than silently ignoring a likely config mistake.
	TransportStdio Transport = "stdio"
)

// Readiness selects the harmless request used to decide whether an HTTP
// process is ready. MCP is the default for existing managed servers; a web
// dashboard only needs to serve an HTTP page and must not be sent an MCP
// initialize request.
type Readiness string

const (
	// ReadinessMCP probes an MCP initialize exchange.
	ReadinessMCP Readiness = "mcp"
	// ReadinessHTTP probes a dashboard with a harmless HTTP GET.
	ReadinessHTTP Readiness = "http"
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
	// DefaultProbeTimeout is the transport-level ceiling on the client that
	// asks a child whether it is ready. Every probe already passes a context
	// with a deadline; this is what a caller who supplies their own client
	// gets by default, so a request cannot outlive the answer being useful.
	DefaultProbeTimeout = 5 * time.Second
	// defaultHost is the address a server listens on when Spec does not say
	// otherwise. Never wide open by default, the same posture the rest of
	// Atenea takes with anything that binds a socket.
	defaultHost = "127.0.0.1"
	// probeEvery is how often a spawn attempt polls for readiness.
	probeEvery = 150 * time.Millisecond
	// handshakeExitGrace gives a child that closed its MCP stream a bounded
	// moment for cmd.Wait to publish the real exit status. It is deliberately
	// independent from probeEvery: the latter is a polling cadence, while this
	// is only the handoff between a failed handshake and the process result.
	handshakeExitGrace = 500 * time.Millisecond
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
	// Transport is how id is reached once it is up. The zero value is
	// TransportHTTP, so a Spec written before stdio existed keeps its
	// current meaning untouched.
	Transport Transport
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
	// Readiness is the protocol used by the supervisor's startup probe.
	Readiness Readiness
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
	if s.Transport == "" {
		s.Transport = TransportHTTP
	}
	switch s.Transport {
	case TransportHTTP:
		if s.Host == "" {
			s.Host = defaultHost
		}
	case TransportStdio:
		if s.Host != "" || s.Port != 0 || s.EndpointPath != "" {
			return Spec{}, fmt.Errorf(
				"supervisor: spec %q is stdio and also sets host, port or endpoint_path: a stdio server listens on nothing, so this is almost certainly a config mistake and not something to honor silently",
				s.ID)
		}
	default:
		return Spec{}, fmt.Errorf("supervisor: spec %q transport must be %q or %q, got %q",
			s.ID, TransportHTTP, TransportStdio, s.Transport)
	}
	if s.Readiness == "" {
		s.Readiness = ReadinessMCP
	}
	if s.Readiness != ReadinessMCP && s.Readiness != ReadinessHTTP {
		return Spec{}, fmt.Errorf("supervisor: spec %q readiness must be %q or %q, got %q",
			s.ID, ReadinessMCP, ReadinessHTTP, s.Readiness)
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
		// A Timeout, not the zero value. Every probe passes a context with a
		// deadline now, so this is the second lock rather than the only one --
		// but a client with no timeout at all is a loaded gun for the next
		// caller who forgets, and the transport-level ceiling costs nothing.
		s.HTTP = &http.Client{Timeout: DefaultProbeTimeout}
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
	procs  map[string]*process
	order  []string // registration order, so Status is stable to read
	reaper sync.Once
	// warming counts the goroutines WarmUp left running. Stop waits on them
	// before it declares everything stopped: a warm-up that is still in
	// flight is the one caller that can still be inside ensureReady when the
	// shutdown starts, and Stop promising that nothing is running has to
	// cover it too.
	warming  sync.WaitGroup
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
		if built.Transport == TransportHTTP {
			port, err := choosePort(built.Host, built.Port)
			if err != nil {
				return nil, fmt.Errorf("supervisor: spec %q: %w", built.ID, err)
			}
			built.Port = port
		}
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
// never changes after New: the port was chosen there. It only means
// anything for an http server; a stdio server has no URL to give, and
// asking for one here fails loudly rather than handing back "" as if it
// were a real answer -- see Session for the stdio counterpart.
func (s *Supervisor) Endpoint(id string) (string, error) {
	p, err := s.find(id)
	if err != nil {
		return "", err
	}
	if p.spec.Transport != TransportHTTP {
		return "", fmt.Errorf("supervisor: %q is a %s server: it has no endpoint URL, use Session instead", id, p.spec.Transport)
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

// Session returns the live mcpstdio session for id, the stdio counterpart
// of Endpoint: usable once EnsureReady has returned successfully for a
// stdio spec, and never again once that activation's child is gone.
func (s *Supervisor) Session(id string) (*mcpstdio.Session, error) {
	p, err := s.find(id)
	if err != nil {
		return nil, err
	}
	return p.liveSession()
}

// Acquire and Release bracket one call against id, so the idle reaper never
// stops a server while something is using it.
func (s *Supervisor) Acquire(id string) {
	if p, err := s.find(id); err == nil {
		p.acquire()
	}
}

// Release matches a prior Acquire, once the caller's work with id is done.
func (s *Supervisor) Release(id string) {
	if p, err := s.find(id); err == nil {
		p.release()
	}
}

// WarmUp starts every Persistent server. Independent servers still start in
// parallel, but Serena's per-repository instances are started in declaration
// order. Serena chooses its dashboard port by scanning for the first free
// port; parallel starts can all observe the same free port before any Flask
// thread binds it, leaving some dashboards missing or pointing at another
// repository. Waiting only within that family makes the dashboard allocation
// deterministic without serializing unrelated MCPs.
func (s *Supervisor) WarmUp(ctx context.Context) {
	// Start independent persistent servers first. Serena is the exception:
	// its per-repository children must be warmed in declaration order because
	// each one claims the first free dashboard port.
	for _, id := range s.order {
		p := s.procs[id]
		if p.spec.Lifecycle != Persistent || strings.HasPrefix(id, "serena@") {
			continue
		}
		s.warming.Add(1)
		go func(p *process) {
			defer s.warming.Done()
			_, _ = p.ensureReady(ctx)
		}(p)
	}
	for _, id := range s.order {
		p := s.procs[id]
		if p.spec.Lifecycle == Persistent && strings.HasPrefix(id, "serena@") {
			_, _ = p.ensureReady(ctx)
		}
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

// Stop closes this supervisor for good: nothing it owns is running when it
// returns, and nothing can be started through it afterwards. EnsureReady --
// including the one a WarmUp goroutine is still on its way into -- fails from
// here on rather than spawning a child with nobody left to stop it.
//
// Every process is waited for, but none of them indefinitely: each gets its
// own StopGrace twice over plus the margin os/exec needs to stop waiting on a
// grandchild that escaped the process group, and one that is still standing
// after that is marked down rather than waited on. Calling Stop more than
// once is safe; the second call finds everything already stopped or down and
// returns at once.
func (s *Supervisor) Stop() {
	s.stopOnce.Do(func() { close(s.stopped) })
	// Latch first, on every process, before waiting on anything. A shutdown
	// that stopped each server as it walked the list left the ones it had not
	// reached yet startable, and a warm-up goroutine landing in that window
	// spawned a child after Stop had already passed it by.
	for _, id := range s.order {
		s.procs[id].shutdown()
	}
	s.warming.Wait()
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
