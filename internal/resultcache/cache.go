// Package resultcache is a small bounded, read-only result cache.  It owns
// neither provider policy nor permissions; callers decide which operations
// are safe to cache and provide a complete canonical key.
package resultcache

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Version is part of ATENEA's public orchestration contract.
const Version = "result-cache-v1"

// Config bounds memory and freshness. Zero values are invalid; callers should
// explicitly choose a conservative limit rather than accidentally enabling an
// unbounded cache.
type Config struct {
	MaxEntries int
	MaxBytes   int64
	TTL        time.Duration
}

// Validate is part of ATENEA's public orchestration contract.
func (c Config) Validate() error {
	if c.MaxEntries <= 0 {
		return errors.New("result cache max entries must be positive")
	}
	if c.MaxBytes <= 0 {
		return errors.New("result cache max bytes must be positive")
	}
	if c.TTL <= 0 {
		return errors.New("result cache ttl must be positive")
	}
	return nil
}

// DefaultConfig is deliberately small and short lived.
func DefaultConfig() Config { return Config{MaxEntries: 128, MaxBytes: 4 << 20, TTL: 5 * time.Minute} }

type entry struct {
	key     string
	value   []byte
	bytes   int64
	expires time.Time
}
type flight struct {
	done    chan struct{}
	value   []byte
	err     error
	waiters atomic.Int32
}

// ResultSource distinguishes a physical leader call, a caller that joined an
// in-flight call, and a value already retained in the cache. Keeping these
// states separate prevents a waiter from being reported as a cache hit.
type ResultSource uint8

const (
	// SourceLeader is part of ATENEA's public orchestration contract.
	SourceLeader ResultSource = iota
	// SourceWaiter is part of ATENEA's public orchestration contract.
	SourceWaiter
	// SourceStored is part of ATENEA's public orchestration contract.
	SourceStored
)

// Cache is safe for concurrent use and coalesces concurrent misses by key.
type Cache struct {
	mu      sync.Mutex
	cfg     Config
	now     func() time.Time
	bytes   int64
	items   map[string]*list.Element
	lru     *list.List
	flights map[string]*flight
}

// New is part of ATENEA's public orchestration contract.
func New(cfg Config) (*Cache, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Cache{cfg: cfg, now: time.Now, items: make(map[string]*list.Element), lru: list.New(), flights: make(map[string]*flight)}, nil
}

// Get is part of ATENEA's public orchestration contract.
func (c *Cache) Get(key string) ([]byte, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getLocked(key)
}

// getLocked is the cache lookup shared by the public read and the flight
// admission path. Callers must hold c.mu. Keeping the expiry/LRU mutation in
// one place makes the second lookup below atomic with flight creation.
func (c *Cache) getLocked(key string) ([]byte, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*entry)
	if !c.now().Before(e.expires) {
		c.removeLocked(el)
		return nil, false
	}
	c.lru.MoveToFront(el)
	return append([]byte(nil), e.value...), true
}

// Put is part of ATENEA's public orchestration contract.
func (c *Cache) Put(key string, value []byte) bool {
	if c == nil || key == "" || int64(len(value)) > c.cfg.MaxBytes {
		return false
	}
	copyValue := append([]byte(nil), value...)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.removeLocked(el)
	}
	el := c.lru.PushFront(&entry{key: key, value: copyValue, bytes: int64(len(copyValue)), expires: c.now().Add(c.cfg.TTL)})
	c.items[key] = el
	c.bytes += int64(len(copyValue))
	for len(c.items) > c.cfg.MaxEntries || c.bytes > c.cfg.MaxBytes {
		c.removeLocked(c.lru.Back())
	}
	return true
}

func (c *Cache) removeLocked(el *list.Element) {
	if el == nil {
		return
	}
	e := el.Value.(*entry)
	delete(c.items, e.key)
	c.bytes -= e.bytes
	c.lru.Remove(el)
}

// Do returns (value, shared, error). shared is true for a caller that joined
// an in-flight provider call or received its completed cached result. A
// successful value is cached automatically.
func (c *Cache) Do(ctx context.Context, key string, load func(context.Context) ([]byte, error)) ([]byte, bool, error) {
	return c.DoIf(ctx, key, load, func([]byte) bool { return true })
}

// DoIf is Do with an explicit cacheability predicate. It lets a caller share
// a partial provider result with concurrent waiters without retaining it.
func (c *Cache) DoIf(ctx context.Context, key string, load func(context.Context) ([]byte, error), cacheable func([]byte) bool) ([]byte, bool, error) {
	value, source, err := c.DoIfState(ctx, key, load, cacheable)
	return value, source != SourceLeader, err
}

// DoIfState is DoIf with the complete source classification. DoIf retains its
// historical boolean API for callers that only need to know whether the
// result was shared.
func (c *Cache) DoIfState(ctx context.Context, key string, load func(context.Context) ([]byte, error), cacheable func([]byte) bool) ([]byte, ResultSource, error) {
	if value, ok := c.Get(key); ok {
		return value, SourceStored, nil
	}
	c.mu.Lock()
	// The first Get and this lock acquisition are separate by design so a
	// caller can be canceled while waiting. A leader may complete in between,
	// however, so revalidate the entry under the same lock before installing a
	// new flight; otherwise a completed answer can be executed a second time.
	if value, ok := c.getLocked(key); ok {
		c.mu.Unlock()
		return value, SourceStored, nil
	}
	if f, ok := c.flights[key]; ok {
		f.waiters.Add(1)
		c.mu.Unlock()
		defer f.waiters.Add(-1)
		select {
		case <-f.done:
			return append([]byte(nil), f.value...), SourceWaiter, f.err
		case <-ctx.Done():
			return nil, SourceWaiter, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	c.flights[key] = f
	c.mu.Unlock()
	value, err := load(ctx)
	if err == nil && cacheable != nil && cacheable(value) {
		c.Put(key, value)
	}
	c.mu.Lock()
	f.value, f.err = append([]byte(nil), value...), err
	delete(c.flights, key)
	close(f.done)
	c.mu.Unlock()
	return value, SourceLeader, err
}

// InFlightWaiters returns the number of callers currently waiting on any
// provider flight. It is useful for activity diagnostics and deterministic
// tests of coalescing; it does not count retained cache hits.
func (c *Cache) InFlightWaiters() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var total int
	for _, f := range c.flights {
		total += int(f.waiters.Load())
	}
	return total
}

// Len is part of ATENEA's public orchestration contract.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Bytes is part of ATENEA's public orchestration contract.
func (c *Cache) Bytes() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// KeyParts names every dimension that can make a read-only answer different.
// Keep fields additive: an omitted dimension is intentionally observable in
// the digest rather than silently sharing with an older producer.
type KeyParts struct {
	PermissionDigest  string `json:"permission_digest"`
	RepositoryID      string `json:"repository_id"`
	RepositoryRoot    string `json:"repository_root"`
	SourceFingerprint string `json:"source_fingerprint"`
	Capability        string `json:"capability"`
	CapabilityVersion string `json:"capability_version"`
	Payload           any    `json:"payload"`
	Cursor            string `json:"cursor"`
	Implementation    string `json:"implementation"`
	Provider          string `json:"provider"`
	ToolVersion       string `json:"tool_version"`
	ToolInstance      string `json:"tool_instance"`
	ConfigDigest      string `json:"config_digest"`
	Generation        int    `json:"generation"`
	Snapshot          string `json:"snapshot"`
	Freshness         string `json:"freshness"`
}

// CanonicalKey is part of ATENEA's public orchestration contract.
func CanonicalKey(parts KeyParts) (string, error) {
	encoded, err := json.Marshal(parts)
	if err != nil {
		return "", fmt.Errorf("cache key: %w", err)
	}
	h := sha256.Sum256(encoded)
	return Version + ":" + hex.EncodeToString(h[:]), nil
}

// PermissionDigest is part of ATENEA's public orchestration contract.
func PermissionDigest(permission any) (string, error) {
	encoded, err := json.Marshal(permission)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(encoded)
	return hex.EncodeToString(h[:]), nil
}
