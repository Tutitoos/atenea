package checkpoint

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Recovery is what an ugly close left behind.
type Recovery struct {
	Swept int      // interrupted dumps removed
	Torn  int      // unreadable receipts set aside
	Files []string // what was swept or set aside, for the incident report
}

// Recover puts the directory back into a state the loader can trust, and is
// meant to run before the first commission is accepted.
//
// Save removes its temporary file with a defer, which covers an error return
// and nothing else: a SIGKILL or a power cut leaves the <id>.<rand>.tmp on
// disk. Nor is there an fsync before the rename, so the same cut can land a
// <id>.json the kernel never finished writing. Neither shows up until something
// tries to resume, which is the worst moment to find out.
//
// It is safe to run twice: a swept dump is gone, and a torn receipt no longer
// ends in .json, so the second pass finds nothing.
//
// The counts describe what the pass managed to do, including when it stops
// early. It stops at the first failure because everything that can fail here
// fails at the level of the directory, and the next file would only say so
// again.
func (s *Store) Recover() (Recovery, error) {
	var rec Recovery
	if !s.Enabled() {
		return rec, nil
	}

	// Held for the whole pass so that a dump being written right now cannot
	// have its temporary file swept out from under the rename.
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Nothing has ever been dumped, so nothing can have gone wrong
			// halfway. The directory stays absent, as it would after a clean
			// run that was never given work.
			return rec, nil
		}
		return rec, contract.Fail(contract.FailurePermissionDenied,
			"checkpoint directory %s: %v", s.dir, err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		switch filepath.Ext(name) {
		case ".tmp":
			// An interrupted dump is a record of a run that never happened
			// that way. There is nothing in it worth keeping.
			if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
				return rec, contract.Fail(contract.FailurePermissionDenied,
					"sweeping %s: %v", name, err)
			}
			rec.Swept++
			rec.Files = append(rec.Files, name)
		case ".json":
			if reads(filepath.Join(s.dir, name)) {
				continue
			}
			// Set aside rather than deleted. In alpha this file is the only
			// evidence of what the ugly close cost, one close produces a
			// handful of them at most, and the loader opens nothing but
			// *.json -- so a .torn is out of the way without being destroyed.
			// Deleting would leave nobody able to say what was lost.
			aside := name + ".torn"
			if err := os.Rename(filepath.Join(s.dir, name), filepath.Join(s.dir, aside)); err != nil {
				return rec, diskFailure(err, "setting aside %s: %v", name, err)
			}
			rec.Torn++
			// The name the evidence carries now, because being able to find it
			// again is the whole reason it was kept.
			rec.Files = append(rec.Files, aside)
		}
	}
	return rec, nil
}

// reads reports whether the file still comes back as a run.
//
// Unreadable and unparseable get the same verdict: the loader is the only
// reader that matters here and neither one gets past it. So does a receipt that
// parses and names no run -- "null" and "{}" both survive Unmarshal without
// filling anything in, which is what a truncated dump looks like on the days it
// happens to still close its braces.
func reads(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var run Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return false
	}
	return run.ID != ""
}
