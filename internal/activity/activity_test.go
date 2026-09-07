package activity

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublishWaitsForCallbackAndReturnsItsFailure(t *testing.T) {
	seen := make(chan Notice, 1)
	server, err := Start(func(n []Notice) error {
		seen <- n[0]
		return errors.New("chat closed")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	if err := Publish(server.Path(), "code.search"); err == nil {
		t.Fatal("publish succeeded after callback failed")
	}
	if got := <-seen; got.Tool != "code.search" || got.ID == "" {
		t.Fatalf("notice = %#v", got)
	}
}

func TestCloseCancelsAcceptedConnectionsBeforeReturning(t *testing.T) {
	var called atomic.Bool
	server, err := Start(func([]Notice) error {
		called.Store(true)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", server.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(`{"id":"partial"`)); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte(`,"tool":"Bash"}` + "\n"))
	_ = conn.Close()
	time.Sleep(20 * time.Millisecond)
	if called.Load() {
		t.Fatal("callback ran after server close")
	}
}

func TestParallelPublicationsShareOneBatch(t *testing.T) {
	batches := make(chan []Notice, 1)
	server, err := Start(func(batch []Notice) error {
		batches <- batch
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, tool := range []string{"code.search", "symbol.search"} {
		go func(tool string) {
			<-start
			done <- Publish(server.Path(), tool)
		}(tool)
	}
	close(start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	batch := <-batches
	if len(batch) != 2 {
		t.Fatalf("batch = %#v, want two parallel notices", batch)
	}
}
