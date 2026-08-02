// Package clock is the single lane every background maintenance task runs in.
//
// Plenty of things in Atenea happen on a rhythm: metrics are flushed in
// batches, the history is rolled up, and more will follow. Given a timer each,
// they eventually all come due on the same second, wake up together, and fight
// over the same disk -- a stall for whoever is waiting on real work at that
// moment. So there is one clock marking every rhythm, and one lane serving the
// tasks one at a time.
//
// The lane is a mutex rather than a worker goroutine on purpose. Atenea is a
// CLI at least as often as it is a service, and a short-lived command still has
// to flush before it exits: with a mutex, Do works in a process that never
// started the clock at all, and no goroutine exists until somebody asks for a
// rhythm.
package clock

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Job is one maintenance task and how often it should run.
type Job struct {
	// Name identifies the job to Do and on the status screen. Unique.
	Name string
	// Every is the rhythm. Ticks that arrive while the lane is busy are
	// dropped rather than queued: a job that cannot keep up with its own
	// period would otherwise build a backlog that never drains.
	Every time.Duration
	// Run does the work. It is never called concurrently with any other job.
	Run func(context.Context) error
}

// State is what a job has been doing, for whoever is watching.
type State struct {
	Name    string
	Runs    int
	LastRun time.Time
	LastErr error
}

// Clock ticks once for everybody and serves the jobs one at a time.
type Clock struct {
	jobs []Job

	// lane serializes execution. Held for exactly as long as a job runs.
	lane sync.Mutex

	mu      sync.Mutex
	state   map[string]*State
	due     map[string]time.Time
	started bool
	stop    chan struct{}
	done    chan struct{}
}

// New validates the jobs and returns a clock that is not ticking yet.
func New(jobs ...Job) (*Clock, error) {
	c := &Clock{
		jobs:  make([]Job, 0, len(jobs)),
		state: make(map[string]*State, len(jobs)),
		due:   make(map[string]time.Time, len(jobs)),
	}
	now := time.Now()
	for _, job := range jobs {
		switch {
		case job.Name == "":
			return nil, fmt.Errorf("clock: a job has no name")
		case job.Run == nil:
			return nil, fmt.Errorf("clock: job %q has nothing to run", job.Name)
		case job.Every <= 0:
			return nil, fmt.Errorf("clock: job %q has no rhythm: every %v", job.Name, job.Every)
		}
		if _, clash := c.state[job.Name]; clash {
			return nil, fmt.Errorf("clock: job %q is registered twice", job.Name)
		}
		c.jobs = append(c.jobs, job)
		c.state[job.Name] = &State{Name: job.Name}
		c.due[job.Name] = now.Add(job.Every)
	}
	return c, nil
}

// resolution is how often the clock looks at its list. One ticker for
// everybody, so the rhythms are compared against a single beat instead of each
// job carrying a timer of its own.
func (c *Clock) resolution() time.Duration {
	smallest := time.Duration(0)
	for _, job := range c.jobs {
		if smallest == 0 || job.Every < smallest {
			smallest = job.Every
		}
	}
	return smallest
}

// Start begins ticking until ctx is canceled or Stop is called. Calling it on
// a clock with no jobs, or twice, is a no-op: there is nothing to beat for.
func (c *Clock) Start(ctx context.Context) {
	c.mu.Lock()
	if c.started || len(c.jobs) == 0 {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.stop = make(chan struct{})
	c.done = make(chan struct{})
	stop, done := c.stop, c.done
	c.mu.Unlock()

	go c.tick(ctx, stop, done)
}

func (c *Clock) tick(ctx context.Context, stop, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(c.resolution())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case now := <-ticker.C:
			c.sweep(ctx, now)
		}
	}
}

// sweep runs whatever is due. It takes the lane without waiting: if real
// maintenance is already under way this beat is skipped, and the job stays due
// for the next one.
func (c *Clock) sweep(ctx context.Context, now time.Time) {
	for _, job := range c.jobs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		c.mu.Lock()
		due := c.due[job.Name]
		c.mu.Unlock()
		if now.Before(due) {
			continue
		}
		if !c.lane.TryLock() {
			return
		}
		// A tick has nobody to report to. The failure is already counted in
		// the job's state, which is what the status screen reads, and there
		// is always a next beat -- unlike Do, where the caller is waiting.
		_ = c.execute(ctx, job)
		c.lane.Unlock()
	}
}

// Do runs one job out of band and waits for it.
//
// This is the safety net the batching design asks for: the rhythm keeps the
// common case cheap, and the moments that must not lose data -- a phase
// closing, the process going down -- ask for the work directly. It waits for
// the lane rather than skipping it, because unlike a tick there is no next
// time.
func (c *Clock) Do(ctx context.Context, name string) error {
	var found *Job
	for i := range c.jobs {
		if c.jobs[i].Name == name {
			found = &c.jobs[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("clock: no job named %q", name)
	}
	c.lane.Lock()
	defer c.lane.Unlock()
	return c.execute(ctx, *found)
}

// execute runs a job and books the result. The lane is held by the caller.
func (c *Clock) execute(ctx context.Context, job Job) error {
	err := job.Run(ctx)
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()
	// The next turn is counted from the end of this one, so a job that takes
	// longer than its own period backs off instead of running back to back.
	c.due[job.Name] = now.Add(job.Every)
	st := c.state[job.Name]
	st.Runs++
	st.LastRun = now
	st.LastErr = err
	return err
}

// Stop ends the rhythm and returns once no job is running.
//
// It does not cut a job short: a maintenance task interrupted between two
// writes is the situation the single lane exists to avoid, so the wait is
// bounded by the job rather than by a deadline here.
func (c *Clock) Stop() {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return
	}
	c.started = false
	stop, done := c.stop, c.done
	c.stop, c.done = nil, nil
	c.mu.Unlock()

	close(stop)
	<-done

	c.lane.Lock()
	c.lane.Unlock() //nolint:staticcheck // taking the lane is the wait itself.
}

// States reports what each job has been doing, in registration order.
func (c *Clock) States() []State {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]State, 0, len(c.jobs))
	for _, job := range c.jobs {
		out = append(out, *c.state[job.Name])
	}
	return out
}
