package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Tutitoos/atenea/internal/backup"
	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/clock"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Recovery is what the last ugly close left behind, and what was done about it.
//
// The first start after a power cut or a SIGKILL is the delicate moment: the
// base can be half written and the paper copy of a run half renamed. Starting
// straight into work and letting it fail later is what turns an ugly close into
// silent corruption, so the damage is assessed first and only then is work
// accepted. It is the sibling of the clean stop -- if going down is orderly,
// coming up is too.
type Recovery struct {
	// Swept counts interrupted dumps removed: a record of a run that never
	// happened that way.
	Swept int
	// Torn counts receipts that could not be read and were set aside.
	Torn int
	// BaseSetAside is where a measurement base that would not answer was
	// moved, empty when the base answered. The history it held is what the
	// backups exist to restore, which is why this names a path rather than
	// just reporting a failure.
	BaseSetAside string
	// Files names what was swept or set aside, so the incident is actionable.
	Files []string
}

// Clean reports a start that found nothing to repair, which is every start
// after a clean stop.
func (r Recovery) Clean() bool {
	return r.Swept == 0 && r.Torn == 0 && r.BaseSetAside == ""
}

// Summary is the one line the status screen and the incident share.
func (r Recovery) Summary() string {
	if r.Clean() {
		return ""
	}
	out := fmt.Sprintf("%d interrupted dump(s) swept, %d torn receipt(s) set aside", r.Swept, r.Torn)
	if r.BaseSetAside != "" {
		out += "; the measurement base did not answer and was moved to " + r.BaseSetAside
	}
	return out
}

// upkeepFile is the claim, and it sits in the state root beside the very things
// it protects -- the receipts and the measurement base.
//
// Not in XDG_RUNTIME_DIR, which is where a lock of this kind would ordinarily
// go. A lock only excludes people who look in the same place, and that variable
// is set for a systemd --user service and for a login shell but unset under
// cron: the two would claim two different files and both go on sweeping. The
// state root is derived from HOME, so every process that shares the state shares
// the claim on it. The one thing the runtime directory would have given us --
// nothing stale surviving a reboot -- the pid inside the file already gives.
const upkeepFile = "upkeep.lock"

// claimUpkeep takes the exclusive right to maintain the state on disk.
//
// Two services would each sweep the receipt directory, each tick the clock, and
// each drive the flush, the roll-up and the backup against one measurement base.
// Every one of those coordinates through a lock held inside a single process, so
// the second service does not make the work twice as reliable -- it makes the
// locks stop meaning anything. The claim turns that into a refusal that names
// who already has it.
func claimUpkeep() (func(), error) {
	path := filepath.Join(platform.StateDir(), upkeepFile)
	release, err := pidlock.Claim(path)
	switch {
	case errors.Is(err, pidlock.ErrHeld):
		return nil, contract.Fail(contract.FailureUnavailable,
			"another atenea already has the upkeep (pid %d, %s): only one may sweep receipts and tick the clock",
			pidlock.Holder(path), path)
	case err != nil:
		return nil, contract.Fail(contract.FailurePermissionDenied,
			"claiming the upkeep at %s: %v", path, err)
	}
	return release, nil
}

// recoverReceipts runs the paper-copy half of the pass.
func recoverReceipts(store *checkpoint.Store) (Recovery, error) {
	found, err := store.Recover()
	if err != nil {
		return Recovery{}, err
	}
	return Recovery{Swept: found.Swept, Torn: found.Torn, Files: found.Files}, nil
}

// openBase opens the measurement base and makes sure it answers.
//
// Opening proves a file exists; the design asks whether the base RESPONDS, so
// this queries it. A base that does not answer cannot be repaired in place, and
// refusing to start over it would be the wrong trade: the funnel already copes
// with having no measurements at all -- that is the cold start it was built for
// -- and the history that was lost is exactly what the backups protect. So the
// wreckage is moved aside under its own name, a fresh base is opened where it
// was, and the incident says where to look.
//
// The one case that must NOT be treated as damage is another live Atenea
// holding the database. Moving a healthy file out from under a running process
// would manufacture the corruption this check exists to catch, so the two are
// told apart by their bin and only `unavailable` sets anything aside.
func openBase(cfg config.Metrics) (*metrics.Store, string, error) {
	store, err := metrics.Open(cfg.Path, metrics.Options{BufferLimit: cfg.BufferLimit})
	if err == nil {
		if checkErr := store.Check(context.Background()); checkErr == nil {
			return store, "", nil
		} else if contract.KindOf(checkErr) != contract.FailureUnavailable {
			// Locked by a live Atenea, or anything else that is not damage.
			// The store is usable; whoever else holds it will let go.
			return store, "", nil
		}
		_ = store.Close()
	} else if contract.KindOf(err) != contract.FailureUnavailable {
		return nil, "", err
	}

	moved, asideErr := metrics.SetAside(cfg.Path, time.Now())
	if asideErr != nil {
		// Nothing to move means the open failed for a reason that is not a
		// damaged file -- a directory that cannot be created, a full disk.
		// That is the caller's problem to report, not something to paper over.
		if err == nil {
			err = contract.Fail(contract.FailureUnavailable,
				"measurement base at %s does not answer and could not be moved: %v",
				cfg.Path, asideErr)
		}
		return nil, "", err
	}
	fresh, err := metrics.Open(cfg.Path, metrics.Options{BufferLimit: cfg.BufferLimit})
	if err != nil {
		return nil, moved, err
	}
	return fresh, moved, nil
}

// openCopies prepares the backup store, or reports why there is none.
//
// A store that cannot even be described -- a folder inside the tree it copies,
// a rotation that keeps nothing -- is a settings mistake and stops the core.
// Copying is the one maintenance task whose absence is invisible until the day
// it is needed, so a broken setup must not degrade quietly into no backups.
func openCopies(cfg config.Backup, source, configPath string) (*backup.Store, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	return backup.New(backup.Options{
		Source: source,
		Dir:    cfg.Dir,
		Keep:   cfg.Keep,
		Extras: []backup.Extra{{Source: configPath, Dest: "config/atenea.toml"}},
	})
}

// buildLanes registers every background rhythm in the one clock.
//
// The clock exists even with nothing in it. A core with measuring and copying
// both switched off still has a maintenance lane, it simply has nothing to run,
// and that keeps the shutdown path from having to ask which kind of core it is
// stopping.
//
// Every job is wrapped so its failure reaches the crash notebook. A job that
// runs on a beat has nobody waiting on its return value -- that is the whole
// point of a beat -- so without the wrapper a backup failing every six hours
// for a week looks exactly like a backup succeeding every six hours for a week.
func buildLanes(cfg config.Config, store *metrics.Store, copies *backup.Store, receipts *checkpoint.Store, book *notebook.Notebook, health func(context.Context) error) (*clock.Clock, error) {
	watch := &maintenance{book: book, store: store}
	jobs := make([]clock.Job, 0, 5)
	if store != nil {
		jobs = append(jobs,
			clock.Job{
				Name:  jobFlush,
				Every: cfg.Metrics.Flush,
				Run:   watch.wrap(jobFlush, store.Flush),
			},
			clock.Job{
				Name:  jobCompact,
				Every: cfg.Metrics.Compact,
				Run: watch.wrap(jobCompact, func(ctx context.Context) error {
					// Guarded by a mark on disk rather than by the beat alone.
					// The beat only exists while a core is held up; most Atenea
					// processes are a command that lives for a second, and the
					// history still has to be kept in shape for them.
					_, err := store.CompactIfDue(ctx, time.Now(), cfg.Metrics.Compact)
					return err
				}),
			})
	}
	// Retention runs before the backup in this list for a reason that is not
	// ordering -- the clock does not honor list order -- but reading: what a
	// copy carries is what retention left, and the two rhythms are the pair
	// that decides how much of the past this machine holds. Keep of zero is
	// the operator saying to forget nothing, so the job is not scheduled at
	// all rather than scheduled and skipped.
	if cfg.Retention.Keep > 0 {
		jobs = append(jobs, clock.Job{
			Name:  jobRetention,
			Every: cfg.Retention.Every,
			Run: watch.wrap(jobRetention, func(ctx context.Context) error {
				return pruneHistory(ctx, cfg, receipts)
			}),
		})
	}
	if copies != nil {
		jobs = append(jobs, clock.Job{
			Name:  jobBackup,
			Every: cfg.Backup.Every,
			Run: watch.wrap(jobBackup, func(ctx context.Context) error {
				// Due is read from the newest copy on disk, not from this
				// beat. A service restarted more often than six hours -- a
				// reboot, an upgrade, a crash loop -- would otherwise never
				// reach its first backup, and the rhythm that never fires is
				// the one whose absence nobody notices until it matters.
				_, _, err := copies.SnapshotIfDue(ctx, time.Now(), cfg.Backup.Every,
					func(ctx context.Context) error { return quiesce(ctx, store) })
				return err
			}),
		})
	}
	if health != nil && cfg.Core.HealthProbeEvery > 0 {
		jobs = append(jobs, clock.Job{
			Name:  jobMCPHealth,
			Every: cfg.Core.HealthProbeEvery,
			Run:   watch.wrap(jobMCPHealth, health),
		})
	}
	return clock.New(jobs...)
}

// fileRecovery writes down what the ugly close cost.
//
// An ugly close is exactly the kind of internal fault the notebook exists for:
// nobody was watching when it happened, and the evidence is a set of files that
// are no longer where they were. Recording it is what lets `atenea incidents`
// answer "why is yesterday's run missing" a week later.
func fileRecovery(book *notebook.Notebook, found Recovery) {
	if found.Clean() {
		return
	}
	_ = book.Record(notebook.Incident{
		Op:      "core.recover",
		Detail:  found.Summary(),
		Fields:  found.Files,
		PID:     os.Getpid(),
		Version: buildinfo.Version,
	})
}

// pruneHistory removes the record of runs older than the retention window.
//
// Two stores, one pass, and the trace database owns the mark: it is the one of
// the two that can read and write "when did this last run" inside the same
// transaction as the delete, which is what stops two Ateneas starting together
// from both deciding they are the one to do it. The receipts follow the answer
// rather than keeping a second mark that could disagree with the first.
//
// The trace store is opened here rather than held by the core because nothing
// else in the core has one: traces are opened per command, by whoever is about
// to write to them. A daily pass can afford to open a file.
func pruneHistory(ctx context.Context, cfg config.Config, receipts *checkpoint.Store) error {
	traces, err := trace.Open(ctx, "")
	if err != nil {
		return err
	}
	defer func() { _ = traces.Close() }()

	now := time.Now()
	rows, err := traces.PruneIfDue(ctx, now, cfg.Retention.Keep, cfg.Retention.Every)
	if err != nil {
		return err
	}
	if rows == 0 {
		// Not due, or nothing old enough. Either way the receipts are not
		// walked: reading every one of them to find nothing is the cost this
		// mark exists to avoid paying on every beat.
		return nil
	}
	_, err = receipts.Prune(now.Add(-cfg.Retention.Keep))
	return err
}

// quiesce folds both write-ahead logs into their database files, so the copy
// about to be taken is a copy of a settled tree.
//
// The two databases in the state root are the only files in it that are not
// finished the moment they are written: DuckDB and SQLite both keep a log
// beside the file, and a directory copier reaches the two halves at two
// different instants. What that produced was a snapshot that opens and is
// missing whatever had not been folded in -- crash-consistent, not consistent,
// and silent about the difference.
//
// The trace store is opened here rather than held, for the same reason
// pruneHistory opens one: nothing else in the core has a trace store, and a
// rhythm that fires every six hours can afford to open a file.
//
// A failure stops the copy. A snapshot of a tree nobody could settle is
// exactly the snapshot this exists to avoid, and taking it anyway would put
// the failure in a place nobody reads while the copy claims to be fine.
func quiesce(ctx context.Context, store *metrics.Store) error {
	if store != nil {
		if err := store.Checkpoint(ctx); err != nil {
			return err
		}
	}
	traces, err := trace.Open(ctx, "")
	if err != nil {
		return err
	}
	defer func() { _ = traces.Close() }()
	return traces.Checkpoint(ctx)
}
