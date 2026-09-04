// Package dbaccess coordinates DuckDB connections and snapshots across Atenea processes.
package dbaccess

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Suffix identifies coordination files, which are not snapshot data.
const Suffix = ".atenea-access.lock"

type localAccess struct {
	gate chan struct{}
	refs int
}

var localAccesses = struct {
	sync.Mutex
	byPath map[string]*localAccess
}{byPath: make(map[string]*localAccess)}

func reserveLocal(path string) (*localAccess, func(bool)) {
	localAccesses.Lock()
	access := localAccesses.byPath[path]
	if access == nil {
		access = &localAccess{gate: make(chan struct{}, 1)}
		access.gate <- struct{}{}
		localAccesses.byPath[path] = access
	}
	access.refs++
	localAccesses.Unlock()
	return access, func(held bool) {
		if held {
			access.gate <- struct{}{}
		}
		localAccesses.Lock()
		access.refs--
		if access.refs == 0 {
			delete(localAccesses.byPath, path)
		}
		localAccesses.Unlock()
	}
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if os.IsNotExist(err) {
		parent, e := filepath.EvalSymlinks(filepath.Dir(absolute))
		if e != nil {
			return "", e
		}
		canonical = filepath.Join(parent, filepath.Base(absolute))
		err = nil
	}
	if err != nil {
		return "", err
	}
	return canonical, nil
}

// AcquireConnection queues one in-process database connection by canonical path.
// DuckDB cannot reliably attach the same file through concurrent handles, even
// when both handles belong to one process.
func AcquireConnection(ctx context.Context, path string) (func() error, error) {
	canonical, err := canonicalPath(path)
	if err != nil {
		return nil, err
	}
	local, releaseLocal := reserveLocal(canonical)
	select {
	case <-ctx.Done():
		releaseLocal(false)
		return nil, ctx.Err()
	case <-local.gate:
	}
	var once sync.Once
	return func() error {
		once.Do(func() { releaseLocal(true) })
		return nil
	}, nil
}

// Acquire holds shared connection access or exclusive snapshot access until release.
func Acquire(ctx context.Context, path string, exclusive bool) (func() error, error) {
	canonical, err := canonicalPath(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(canonical+Suffix, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		acquired, err := tryLock(f, exclusive)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("database access lock: %w", err)
		}
		if acquired {
			if err = ctx.Err(); err != nil {
				_ = f.Close()
				return nil, err
			}
			return f.Close, nil
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
