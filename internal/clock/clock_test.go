package clock

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func noop(context.Context) error { return nil }

// A job the clock cannot place in time, name or work is a rhythm that silently
// never runs. Better refused at the door.
func TestABrokenJobIsRefused(t *testing.T) {
	for name, job := range map[string]Job{
		"no name":   {Every: time.Second, Run: noop},
		"no rhythm": {Name: "a", Run: noop},
		"backwards": {Name: "a", Every: -time.Second, Run: noop},
		"no work":   {Name: "a", Every: time.Second},
	} {
		if _, err := New(job); err == nil {
			t.Errorf("a job with %s was accepted", name)
		}
	}
	_, err := New(
		Job{Name: "twice", Every: time.Second, Run: noop},
		Job{Name: "twice", Every: time.Minute, Run: noop},
	)
	if err == nil {
		t.Error("two jobs sharing a name were accepted")
	}
}

// The safety nets in the batching design -- a phase closing, the process going
// down -- happen in commands that never start a rhythm at all.
func TestDoWorksWithoutStarting(t *testing.T) {
	var ran atomic.Int64
	c, err := New(Job{Name: "flush", Every: time.Hour, Run: func(context.Context) error {
		ran.Add(1)
		return nil
	}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := c.Do(context.Background(), "flush"); err != nil {
		t.Fatalf("do: %v", err)
	}
	if ran.Load() != 1 {
		t.Fatalf("job ran %d times, want 1", ran.Load())
	}
}

func TestDoRefusesAnUnknownJob(t *testing.T) {
	c, _ := New(Job{Name: "flush", Every: time.Hour, Run: noop})
	err := c.Do(context.Background(), "compact")
	if err == nil || !strings.Contains(err.Error(), "compact") {
		t.Fatalf("err = %v, want a refusal naming the job", err)
	}
}

// Do carries the job's own error back rather than swallowing it: the caller
// asked for this one and is entitled to know it did not happen.
func TestDoReturnsWhatTheJobSaid(t *testing.T) {
	boom := errors.New("disk is full")
	c, _ := New(Job{Name: "flush", Every: time.Hour, Run: func(context.Context) error {
		return boom
	}})
	if err := c.Do(context.Background(), "flush"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the job's own error", err)
	}
	st := c.States()
	if len(st) != 1 || !errors.Is(st[0].LastErr, boom) || st[0].Runs != 1 {
		t.Fatalf("state = %+v, want one run carrying the error", st)
	}
}

// The whole reason there is one clock: three tasks coming due on the same
// second must not all wake up and fight over the disk.
func TestJobsNeverOverlap(t *testing.T) {
	var inside, peak atomic.Int64
	watch := func(context.Context) error {
		now := inside.Add(1)
		for {
			high := peak.Load()
			if now <= high || peak.CompareAndSwap(high, now) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inside.Add(-1)
		return nil
	}
	c, err := New(
		Job{Name: "a", Every: time.Millisecond, Run: watch},
		Job{Name: "b", Every: time.Millisecond, Run: watch},
		Job{Name: "c", Every: time.Millisecond, Run: watch},
	)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				_ = c.Do(ctx, "a")
			}
		}()
	}
	wg.Wait()
	c.Stop()

	if got := peak.Load(); got > 1 {
		t.Fatalf("%d jobs ran at once, want the lane to serve one at a time", got)
	}
}

// A job slower than its own period would otherwise build a backlog that never
// drains. The beat is skipped, not queued.
func TestATickIsSkippedRatherThanQueued(t *testing.T) {
	release := make(chan struct{})
	var runs atomic.Int64
	c, err := New(Job{Name: "slow", Every: time.Millisecond, Run: func(context.Context) error {
		runs.Add(1)
		<-release
		return nil
	}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)

	// Far more beats than the job can serve.
	time.Sleep(60 * time.Millisecond)
	started := runs.Load()
	if started != 1 {
		t.Fatalf("the job started %d times while still running the first, want 1", started)
	}
	close(release)
	c.Stop()
}

// Stop is the last safety net's guarantee: once it returns, nothing is halfway
// through a write.
func TestStopWaitsForTheRunningJob(t *testing.T) {
	var finished atomic.Bool
	started := make(chan struct{})
	var once sync.Once
	c, err := New(Job{Name: "slow", Every: 5 * time.Millisecond, Run: func(context.Context) error {
		once.Do(func() { close(started) })
		time.Sleep(30 * time.Millisecond)
		finished.Store(true)
		return nil
	}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	c.Start(context.Background())
	<-started
	c.Stop()
	if !finished.Load() {
		t.Fatal("Stop returned while a job was still writing")
	}
}

// Running out of band resets the rhythm: the work just happened, so the next
// beat is a full period away rather than immediately due.
func TestDoPushesTheNextBeatOut(t *testing.T) {
	var runs atomic.Int64
	c, err := New(Job{Name: "flush", Every: 40 * time.Millisecond, Run: func(context.Context) error {
		runs.Add(1)
		return nil
	}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	c.Start(context.Background())
	defer c.Stop()
	time.Sleep(20 * time.Millisecond)
	if err := c.Do(context.Background(), "flush"); err != nil {
		t.Fatalf("do: %v", err)
	}
	// The beat that would have fired at 40ms is now due at 60ms.
	time.Sleep(25 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("job ran %d times, want the out-of-band run to have reset the rhythm", got)
	}
}

// A clock nobody started and a clock with nothing to do are both fine; neither
// should leave a goroutine behind or panic on Stop.
func TestAnIdleClockIsHarmless(t *testing.T) {
	empty, err := New()
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	empty.Start(context.Background())
	empty.Stop()
	empty.Stop()

	c, _ := New(Job{Name: "a", Every: time.Hour, Run: noop})
	c.Stop()
	if len(c.States()) != 1 || c.States()[0].Runs != 0 {
		t.Fatalf("states = %+v, want one job that never ran", c.States())
	}
}
