package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// process is one server's whole lifecycle: at most one activation running at
// a time, driven by a single goroutine so nothing else has to reason about
// two spawns racing each other.
type process struct {
	spec Spec
	// endpoint is fixed once, in newProcess, from the port Supervisor.New
	// already chose. It never changes for the life of this process, even
	// across an OnDemand server's idle stop and later restart: the adapter
	// built with this URL must never find it stale.
	endpoint string

	mu sync.Mutex
	// state is read far more often than it changes -- every EnsureReady
	// call on an already-ready server is one lock and a return -- so it is
	// a plain field under mu rather than anything fancier.
	state   State
	pid     int
	started time.Time
	// attempts counts failures since the last time this activation reached
	// ready, and drives the restart budget. It resets to zero on ready, so
	// a server that flakes and recovers repeatedly over a long uptime is
	// judged on its most recent run of bad luck, not its whole life.
	attempts int
	// totalRestarts never resets. It is what the status screen shows,
	// because "restarts: 0" on a server that has crashed and recovered five
	// times today would hide exactly the fact somebody reading that screen
	// wants to see.
	totalRestarts int
	lastReason    string
	inflight      int
	lastUsed      time.Time
	// stopCh is recreated at the start of every activation and closed to
	// ask that activation's goroutine to stop. Reading it is always done
	// under mu; run is handed the channel directly instead of re-reading
	// the field, which sidesteps having to reason about it being replaced
	// out from under a goroutine that is, by construction, always gone by
	// the time a new one could replace it.
	stopCh chan struct{}
	// session is the live mcpstdio session for a stdio activation's child,
	// replaced on every restart under mu so no caller can be handed one
	// whose process has already exited. Always nil for an http spec.
	session *mcpstdio.Session
}

func newProcess(spec Spec) *process {
	p := &process{
		spec:     spec,
		state:    StateStopped,
		lastUsed: time.Now(),
	}
	if spec.Transport == TransportHTTP {
		p.endpoint = fmt.Sprintf("http://%s:%d%s", spec.Host, spec.Port, spec.EndpointPath)
	}
	return p
}

// ensureReady starts the process if it is idle and blocks until it answers
// ready, goes down, or ctx ends. Concurrent callers on the same process share
// one attempt: the second caller in just polls the state the first caused.
func (p *process) ensureReady(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.state == StateStopped {
		p.beginLocked()
	}
	p.mu.Unlock()

	ticker := time.NewTicker(probeEvery)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		state, endpoint, reason := p.state, p.endpoint, p.lastReason
		p.mu.Unlock()
		switch state {
		case StateReady:
			return endpoint, nil
		case StateDown:
			return "", fmt.Errorf("%s is down: %s", p.spec.ID, reason)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// beginLocked starts a fresh activation. Called with mu held, only when
// state is StateStopped: that precondition is what guarantees no run
// goroutine from a previous activation is still alive to race this one.
func (p *process) beginLocked() {
	p.state = StateStarting
	p.attempts = 0
	p.lastReason = ""
	p.stopCh = make(chan struct{})
	go p.run(p.stopCh)
}

func (p *process) acquire() {
	p.mu.Lock()
	p.inflight++
	p.mu.Unlock()
}

func (p *process) release() {
	p.mu.Lock()
	if p.inflight > 0 {
		p.inflight--
	}
	p.lastUsed = time.Now()
	p.mu.Unlock()
}

// stopIfIdle asks a ready, unused OnDemand server to stop. It does not wait:
// the reaper has other servers to check on the same tick, and run's own
// goroutine carries the stop through on its own time.
func (p *process) stopIfIdle() {
	p.mu.Lock()
	idle := p.state == StateReady && p.inflight == 0 && time.Since(p.lastUsed) >= p.spec.IdleTimeout
	p.mu.Unlock()
	if idle {
		p.requestStop()
	}
}

// requestStop asks the current activation, if any, to stop. Safe to call on
// a process that is already Stopped or Down: there is nothing running to
// ask, and asking nobody twice is not an error.
func (p *process) requestStop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.state {
	case StateStopped, StateDown:
		return
	}
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
}

// waitStopped blocks until the process reaches Stopped or Down -- the two
// states nothing is running in. It polls because those are reached from two
// different places in run, and a channel closes exactly once: it cannot mark
// both without reinventing what state already means.
func (p *process) waitStopped() {
	for {
		p.mu.Lock()
		state := p.state
		p.mu.Unlock()
		if state == StateStopped || state == StateDown {
			return
		}
		time.Sleep(probeEvery)
	}
}

// stop is the deliberate, synchronous stop Supervisor.Stop uses: ask, then
// wait for the answer.
func (p *process) stop() {
	p.requestStop()
	p.waitStopped()
}

func (p *process) status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Status{
		ID:         p.spec.ID,
		State:      p.state,
		Endpoint:   p.endpoint,
		PID:        p.pid,
		Port:       p.spec.Port,
		Started:    p.started,
		Restarts:   p.totalRestarts,
		LastReason: p.lastReason,
	}
}

// liveSession returns the current activation's mcpstdio session -- the
// stdio counterpart of the endpoint field an http spec already carries.
// Every reason a caller cannot use it collapses to the one bin a caller
// backing off on error needs, rather than three different ones to
// distinguish: never a stdio spec, not ready yet, and a session whose child
// has already died all mean the same thing to whoever asked.
func (p *process) liveSession() (*mcpstdio.Session, error) {
	if p.spec.Transport != TransportStdio {
		return nil, contract.Fail(contract.FailureUnavailable, "%s is not a stdio server", p.spec.ID)
	}
	p.mu.Lock()
	state, session := p.state, p.session
	p.mu.Unlock()
	if state != StateReady || session == nil {
		return nil, contract.Fail(contract.FailureUnavailable, "%s is not ready", p.spec.ID)
	}
	if err := session.Err(); err != nil {
		return nil, contract.Fail(contract.FailureUnavailable, "%s session died: %v", p.spec.ID, err)
	}
	return session, nil
}

// run drives one activation from its first spawn to the moment nothing is
// left running: a deliberate stop, or the restart budget running out. It is
// the only goroutine that ever moves this process between Starting,
// Restarting, Ready and the two ends, Stopped and Down.
func (p *process) run(stopCh chan struct{}) {
	for {
		result := p.spawnOnce(stopCh)

		p.mu.Lock()
		p.pid = 0
		if result.deliberate {
			p.state = StateStopped
			p.mu.Unlock()
			return
		}
		if result.stableFor >= p.spec.StableAfter {
			// Stayed up long enough to call it recovered: this failure
			// starts a fresh budget rather than spending down the last
			// one, the same forgiveness a long-uptime server deserves
			// after any single bad moment.
			p.attempts = 0
		}
		p.attempts++
		p.totalRestarts++
		p.lastReason = result.err.Error()
		if p.attempts > p.spec.RestartLimit {
			p.state = StateDown
			p.mu.Unlock()
			return
		}
		p.state = StateRestarting
		p.mu.Unlock()

		select {
		case <-time.After(p.spec.RestartDelay):
		case <-stopCh:
			p.mu.Lock()
			p.state = StateStopped
			p.mu.Unlock()
			return
		}

		p.mu.Lock()
		p.state = StateStarting
		p.mu.Unlock()
	}
}

// spawnResult is what one spawn attempt ended with. deliberate means stopCh
// fired, whether before or after the server answered ready, and err is
// always nil in that case: a deliberate stop is not a failure to explain.
type spawnResult struct {
	deliberate bool
	err        error
	// stableFor is how long this attempt stayed ready before ending, zero
	// if it never became ready at all. run compares it against
	// StableAfter to decide whether this failure continues the current
	// restart budget or earns a fresh one.
	stableFor time.Duration
}

// spawnOnce launches one child and carries it from Start through either a
// deliberate stop or its own exit, whichever comes first. stderr is
// captured throughout for both transports, so a failure that happens
// before ready and one that happens hours into a long run both get to
// leave a reason behind. stdout joins it only for an http child; for a
// stdio child stdout is the protocol itself, read instead by the session
// this function stores on p for Supervisor.Session to hand out.
func (p *process) spawnOnce(stopCh chan struct{}) spawnResult {
	args := withPort(p.spec.Args, p.spec.Port)
	cmd := exec.Command(p.spec.Command, args...)
	if len(p.spec.Env) > 0 {
		cmd.Env = append(os.Environ(), p.spec.Env...)
	}
	// Isolate puts the child in its own process group so gracefulStop and
	// the deadline path below can signal the whole tree, not just the one
	// process. Contain would be the wrong call here: this Cmd is built with
	// plain exec.Command, and Contain's Cancel only works on one built with
	// CommandContext -- Start refuses any other Cmd that carries a Cancel
	// func at all. Every stop this package makes goes through gracefulStop
	// or the deadline below instead, on this package's own timers.
	procgroup.Isolate(cmd)
	out := newRing(outputLimit)
	cmd.Stderr = out

	var session *mcpstdio.Session
	if p.spec.Transport == TransportStdio {
		// A stdio child's stdout is the protocol, not diagnostics -- it
		// must never join stderr in out the way an http child's does, or
		// mcpstdio and this ring would each read half of every frame.
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return spawnResult{err: fmt.Errorf("starting: %w", err)}
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return spawnResult{err: fmt.Errorf("starting: %w", err)}
		}
		session = mcpstdio.New(stdin, stdout, mcpstdio.Options{})
	} else {
		cmd.Stdout = out
	}

	if err := cmd.Start(); err != nil {
		return spawnResult{err: fmt.Errorf("starting: %w", err)}
	}
	p.mu.Lock()
	p.pid = cmd.Process.Pid
	p.started = time.Now()
	// The previous activation's child is provably gone by the time a new
	// one starts -- run only calls spawnOnce again after the last call
	// already returned -- so its session is retired here rather than left
	// for some caller to discover the hard way mid-call.
	if p.session != nil {
		_ = p.session.Close()
	}
	p.session = session
	p.mu.Unlock()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	outcome, err := p.waitForReady(cmd, exited, stopCh, session)
	switch outcome {
	case readyStopRequested:
		return spawnResult{deliberate: true}
	case readyNeverCame:
		return spawnResult{err: annotate(err, out)}
	}

	p.mu.Lock()
	p.state = StateReady
	p.lastReason = ""
	p.mu.Unlock()
	readyAt := time.Now()

	select {
	case exitErr := <-exited:
		return spawnResult{err: annotate(fmt.Errorf("exited: %w", exitErr), out), stableFor: time.Since(readyAt)}
	case <-stopCh:
		p.gracefulStop(cmd, exited)
		return spawnResult{deliberate: true, stableFor: time.Since(readyAt)}
	}
}

type readyOutcome uint8

const (
	readyReached readyOutcome = iota
	readyNeverCame
	readyStopRequested
)

// waitForReady blocks until the server confirms readiness, the process
// exits on its own, ready_timeout elapses, or stop fires. What "confirms
// readiness" means depends on session: nil means an http spec, and this
// probes the endpoint exactly as before stdio existed; non-nil means a
// stdio spec, and this drives the MCP handshake on it instead, since a
// stdio child owns no address for a probe to point at.
//
// Every outcome except readyReached leaves exited already drained: on the
// two paths that end without the server ever answering, this function is the
// one that either received the exit itself or killed the child and then
// waited for it. spawnOnce only reads exited again after a readyReached,
// where this function deliberately left it alone.
func (p *process) waitForReady(cmd *exec.Cmd, exited chan error, stopCh chan struct{}, session *mcpstdio.Session) (readyOutcome, error) {
	deadline := time.Now().Add(p.spec.ReadyTimeout)
	ticker := time.NewTicker(probeEvery)
	defer ticker.Stop()
	for {
		select {
		case err := <-exited:
			return readyNeverCame, fmt.Errorf("exited before answering ready: %w", err)
		case <-stopCh:
			// Never reached ready: there is nothing negotiated yet worth
			// ending politely, so this goes straight to the hard stop
			// rather than through gracefulStop's SIGTERM-then-wait. A
			// stdio session dies on its own once its child is killed: its
			// stdout closes, which is what the read loop is waiting on.
			_ = procgroup.Kill(cmd)
			<-exited
			return readyStopRequested, nil
		case <-ticker.C:
			if p.readyNow(session) {
				return readyReached, nil
			}
			if time.Now().After(deadline) {
				_ = procgroup.Kill(cmd)
				<-exited
				return readyNeverCame, fmt.Errorf("did not answer ready within %s", p.spec.ReadyTimeout)
			}
		}
	}
}

// readyNow asks, once, whether this attempt is ready. A nil session is an
// http spec and this is byte-for-byte the probe waitForReady always made;
// a non-nil session is a stdio spec, and initialize succeeding is the only
// thing "ready" can mean when there is no address to ask instead. Bounded
// to one tick rather than the whole ready_timeout so a child that accepts
// the handshake but never answers it does not itself starve waitForReady's
// own ability to notice stopCh or ready_timeout in the meantime.
func (p *process) readyNow(session *mcpstdio.Session) bool {
	if session != nil {
		ctx, cancel := context.WithTimeout(context.Background(), probeEvery)
		defer cancel()
		return session.Initialize(ctx) == nil
	}
	if p.spec.Readiness == ReadinessHTTP {
		return probeHTTP(context.Background(), p.spec.HTTP, p.endpoint) == nil
	}
	return probeReady(context.Background(), p.spec.HTTP, p.endpoint) == nil
}

// gracefulStop asks a ready server to leave the polite way: SIGTERM to the
// group, a bounded wait, then SIGKILL if it is still standing. Called only
// once the child is known to be running, with exited not yet consumed.
func (p *process) gracefulStop(cmd *exec.Cmd, exited chan error) {
	_ = procgroup.Terminate(cmd)
	select {
	case <-exited:
		return
	case <-time.After(p.spec.StopGrace):
	}
	_ = procgroup.Kill(cmd)
	<-exited
}

// annotate folds a spawn attempt's captured output into its error, so a
// crash reason is more than "exit status 1".
func annotate(err error, out *ring) error {
	if tail := out.String(); tail != "" {
		return fmt.Errorf("%w (output: %s)", err, tail)
	}
	return err
}
