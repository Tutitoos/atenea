package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// retentionMark is the file that remembers when the retention pass last ran.
// It sits beside the receipts it governs and is not a .json file, so List and
// Prune never mistake it for a run.
const retentionMark = "retention.mark"

// PruneIfDue prunes closed receipts that stopped being updated more than keep
// ago, at most once per every, and reports how many went.
//
// Guarded by a mark on disk rather than by the caller's beat: most Atenea
// processes are a command that lives for a second and has no rhythm of its
// own. The pass takes the same pid lock a run takes, under a name no run can
// have, so two Ateneas starting together do not both walk every receipt; the
// one that loses the claim simply is not the one doing it.
//
// keep of zero keeps everything. A mark this store cannot read counts as due,
// because a corrupt timestamp must not switch retention off for good.
func (s *Store) PruneIfDue(now time.Time, keep, every time.Duration) (int, error) {
	if !s.Enabled() || keep <= 0 {
		return 0, nil
	}
	if every <= 0 {
		return 0, contract.Fail(contract.FailureInvalidInput,
			"checkpoint: retention interval must be above 0, got %s", every)
	}
	if _, err := os.Stat(s.dir); errors.Is(err, os.ErrNotExist) {
		return 0, nil // nothing was ever saved, so nothing is old
	}
	release, err := s.Lock("retention")
	if err != nil {
		return 0, nil // another process holds the pass
	}
	defer release()

	mark := filepath.Join(s.dir, retentionMark)
	if raw, err := os.ReadFile(mark); err == nil {
		if last, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw))); err == nil && now.Sub(last) < every {
			return 0, nil
		}
	}
	removed, err := s.Prune(now.Add(-keep))
	if err != nil {
		return 0, err
	}
	temp := mark + ".tmp"
	if err := os.WriteFile(temp, []byte(now.UTC().Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		return removed, diskFailure(err, "checkpoint: retention mark: %v", err)
	}
	if err := os.Rename(temp, mark); err != nil {
		_ = os.Remove(temp)
		return removed, diskFailure(err, "checkpoint: retention mark: %v", err)
	}
	return removed, nil
}
