package metrics

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Check reports whether the base answers.
//
// It runs a real read against the table the store writes to, because opening
// only proves the file is there. The first start after a power cut is exactly
// when a half-written database still opens and then falls over on the first
// query, and finding that out mid-commission turns an ugly close into silent
// corruption.
//
// Nothing is flushed on the way: at startup there is no batch yet, and a probe
// that writes is a probe that can break the thing it was sent to inspect.
func (s *Store) Check(ctx context.Context) error {
	db, err := s.connect(ctx)
	if err != nil {
		return notAnswering(s.path, err)
	}
	defer func() { _ = db.Close() }()

	var rows int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM measurement").Scan(&rows); err != nil {
		return notAnswering(s.path, err)
	}
	return nil
}

// notAnswering sorts the two reasons a check comes back empty-handed.
//
// A held lock is another Atenea in the middle of its flush, which is ordinary
// and is not damage. It gets a bin of its own because the answer to a base that
// does not answer is to set it aside, and doing that to a file a live process
// is writing into would manufacture the corruption this check exists to catch.
func notAnswering(path string, err error) error {
	if isLocked(err) {
		return contract.Fail(contract.FailureTimeout,
			"metrics: %s is held by another process: %v", path, err)
	}
	return contract.Fail(contract.FailureUnavailable,
		"metrics: %s does not answer: %v", path, err)
}

// SetAside moves a base that does not answer out of the way and reports where
// it went.
//
// A database that will not answer cannot be repaired from here, and refusing to
// start helps nobody: the funnel already copes with having no measurements --
// that is a cold start, and every implementation is in break-in until it has
// earned its numbers again -- and the history that was lost is exactly what the
// backups exist to put back. So the honest move is to step out of the way, keep
// the wreckage where somebody can look at it, and say where it is.
//
// The write-ahead log goes with it, under either spelling. A stale log left
// beside a fresh database is how the replacement gets corrupted too.
func SetAside(path string, now time.Time) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", contract.Fail(contract.FailureInvalidInput, "metrics: no database path")
	}
	aside := path + ".corrupt-" + now.UTC().Format("20060102T150405Z")
	if err := os.Rename(path, aside); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", contract.Fail(contract.FailureNotFound,
				"metrics: there is nothing at %s to set aside", path)
		}
		return "", contract.Fail(contract.FailurePermissionDenied,
			"metrics: cannot move %s aside: %v", path, err)
	}
	// A missing sidecar is the ordinary case: the log is folded into the
	// database on a clean close, so one only exists when the close was not.
	for _, suffix := range [...]string{".wal", "-wal"} {
		err := os.Rename(path+suffix, aside+suffix)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			// The base is already moved, so the caller is told where it went
			// even though the log was left behind.
			return aside, contract.Fail(contract.FailurePermissionDenied,
				"metrics: cannot move %s aside: %v", path+suffix, err)
		}
	}
	return aside, nil
}
