package resultcache

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheLRUTTLAndByteBounds(t *testing.T) {
	c, err := New(Config{MaxEntries: 2, MaxBytes: 5, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	c.Put("a", []byte("aa"))
	c.Put("b", []byte("bb"))
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a missing")
	}
	c.Put("c", []byte("cc"))
	if _, ok := c.Get("b"); ok {
		t.Fatal("least recently used b survived")
	}
	if c.Bytes() > 5 || c.Len() > 2 {
		t.Fatalf("bounds exceeded: %d/%d", c.Bytes(), c.Len())
	}
	clock := time.Now()
	c.now = func() time.Time { return clock }
	c.Put("ttl", []byte("x"))
	clock = clock.Add(2 * time.Minute)
	if _, ok := c.Get("ttl"); ok {
		t.Fatal("expired value survived")
	}
}

func TestCacheCoalescesAndSeparatesFailures(t *testing.T) {
	c, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, _, err := c.Do(context.Background(), "same", func(context.Context) ([]byte, error) {
				calls.Add(1)
				time.Sleep(10 * time.Millisecond)
				return []byte("answer"), nil
			})
			if err != nil || string(value) != "answer" {
				t.Errorf("Do = %q, %v", value, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", calls.Load())
	}
	if _, _, err := c.Do(context.Background(), "same", func(context.Context) ([]byte, error) {
		calls.Add(1)
		return []byte("unexpected"), nil
	}); err != nil || calls.Load() != 1 {
		t.Fatalf("completed result was not cached: calls=%d err=%v", calls.Load(), err)
	}
	_, _, err = c.Do(context.Background(), "bad", func(context.Context) ([]byte, error) { return nil, errors.New("no") })
	if err == nil {
		t.Fatal("failed load was accepted")
	}
	if _, ok := c.Get("bad"); ok {
		t.Fatal("failed load cached")
	}
}

func TestCacheStateDistinguishesLeaderWaiterAndStoredHit(t *testing.T) {
	c, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	load := func(context.Context) ([]byte, error) {
		once.Do(func() { close(started) })
		<-release
		return []byte("complete"), nil
	}
	type answer struct {
		value  []byte
		source ResultSource
		err    error
	}
	first := make(chan answer, 1)
	go func() {
		value, source, err := c.DoIfState(context.Background(), "complete", load, func([]byte) bool { return true })
		first <- answer{value, source, err}
	}()
	<-started
	second := make(chan answer, 1)
	go func() {
		value, source, err := c.DoIfState(context.Background(), "complete", load, func([]byte) bool { return true })
		second <- answer{value, source, err}
	}()
	deadline := time.Now().Add(time.Second)
	for c.InFlightWaiters() == 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if c.InFlightWaiters() == 0 {
		t.Fatal("waiter did not join the in-flight call")
	}
	close(release)
	a, b := <-first, <-second
	if a.err != nil || b.err != nil || a.source != SourceLeader || b.source != SourceWaiter {
		t.Fatalf("leader/waiter = %+v / %+v", a, b)
	}
	value, source, err := c.DoIfState(context.Background(), "complete", load, func([]byte) bool { return true })
	if err != nil || string(value) != "complete" || source != SourceStored {
		t.Fatalf("stored = %q/%v/%v", value, source, err)
	}
}

func TestCacheConcurrentAdmissionRechecksStoredValue(t *testing.T) {
	for round := 0; round < 20; round++ {
		c, err := New(DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		var calls atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 64; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				value, _, err := c.DoIfState(context.Background(), "race", func(context.Context) ([]byte, error) {
					calls.Add(1)
					time.Sleep(time.Microsecond)
					return []byte("answer"), nil
				}, func([]byte) bool { return true })
				if err != nil || string(value) != "answer" {
					t.Errorf("result = %q, %v", value, err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if got := calls.Load(); got != 1 {
			t.Fatalf("round %d physical loads = %d, want 1", round, got)
		}
	}
}

func TestCanonicalKeyIncludesAllDimensions(t *testing.T) {
	a := KeyParts{RepositoryID: "repo", RepositoryRoot: "/tmp/a", SourceFingerprint: "one", Capability: "code.context", Payload: map[string]any{"q": "x"}}
	b := a
	b.SourceFingerprint = "two"
	ka, err := CanonicalKey(a)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := CanonicalKey(b)
	if err != nil {
		t.Fatal(err)
	}
	if ka == kb {
		t.Fatal("source fingerprint was not part of key")
	}
	p, err := PermissionDigest(struct {
		Task    string
		Effects []string
	}{"x", []string{"read"}})
	if err != nil || p == "" {
		t.Fatalf("permission digest = %q, %v", p, err)
	}
}
