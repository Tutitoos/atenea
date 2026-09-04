// Package dbaccess coordinates DuckDB connections and snapshots across Atenea processes.
package dbaccess

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Suffix identifies coordination files, which are not snapshot data.
const Suffix = ".atenea-access.lock"

// Acquire holds shared connection access or exclusive snapshot access until release.
// Kernel file locks conflict across descriptors even in the same process.
func Acquire(ctx context.Context, path string, exclusive bool) (func() error, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if os.IsNotExist(err) {
		parent, e := filepath.EvalSymlinks(filepath.Dir(absolute))
		if e != nil {
			return nil, e
		}
		canonical = filepath.Join(parent, filepath.Base(absolute))
		err = nil
	}
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
