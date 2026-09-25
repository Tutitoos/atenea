package decision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/internal/sourceidentity"
)

// AcceptedPlanReference selects an exact local acceptance revision. It conveys
// semantic context, never an effect grant or authorization to execute.
type AcceptedPlanReference struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

// AcceptedPlanStore holds operator-accepted context outside the repository.
// An empty Directory uses the current user's configuration directory.
type AcceptedPlanStore struct {
	Directory string
}

type acceptedPlanRecord struct {
	Version int           `json:"version"`
	Context IntentContext `json:"context"`
	Source  string        `json:"source"`
}

var acceptedPlanID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

func (s AcceptedPlanStore) filename(id string) (string, error) {
	if !acceptedPlanID.MatchString(id) {
		return "", fmt.Errorf("invalid accepted plan id")
	}
	dir := s.Directory
	if dir == "" {
		dir = filepath.Join(platform.ConfigDir(), "accepted-plans")
	}
	return filepath.Join(dir, id+".json"), nil
}

// Accept records an explicit local operator acceptance. Reusing an id replaces
// its current revision atomically. It does not establish the operator's identity.
func (s AcceptedPlanStore) Accept(ctx context.Context, id, root string, intent IntentContext) (AcceptedPlanReference, error) {
	filename, err := s.filename(id)
	if err != nil {
		return AcceptedPlanReference{}, err
	}
	if intent.AcceptedPlanID != "" || intent.AcceptedPlanRevision != "" || intent.AcceptedPlanCurrent {
		return AcceptedPlanReference{}, fmt.Errorf("accept context must not contain acceptance assertions")
	}
	if strings.TrimSpace(intent.Repository) == "" || strings.TrimSpace(intent.ActiveObjective) == "" {
		return AcceptedPlanReference{}, fmt.Errorf("accept context requires repository and active_objective")
	}
	if err := validateIntentContext(&intent); err != nil {
		return AcceptedPlanReference{}, err
	}
	if strings.TrimSpace(root) == "" {
		return AcceptedPlanReference{}, fmt.Errorf("repository source path is required")
	}
	source, err := sourceidentity.Discover(ctx, root)
	if err != nil || source.Fingerprint == "" {
		return AcceptedPlanReference{}, fmt.Errorf("cannot identify accepted plan source: %v", err)
	}
	// A receipt inside its own source tree would invalidate itself on creation.
	dir, err := filepath.Abs(filepath.Dir(filename))
	if err != nil {
		return AcceptedPlanReference{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return AcceptedPlanReference{}, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return AcceptedPlanReference{}, err
	}
	boundary := source.Root
	if source.Git {
		// A configured root can be a subdirectory. Source identity deliberately
		// observes its whole Git worktree, so receipts must be outside that too.
		rawRoot, err := exec.CommandContext(ctx, "git", "-C", source.Root, "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return AcceptedPlanReference{}, fmt.Errorf("cannot locate source worktree: %w", err)
		}
		boundary, err = filepath.EvalSymlinks(strings.TrimSpace(string(rawRoot)))
		if err != nil {
			return AcceptedPlanReference{}, err
		}
	}
	rel, err := filepath.Rel(boundary, dir)
	if err != nil || (rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return AcceptedPlanReference{}, fmt.Errorf("accepted plan store must be outside the repository")
	}
	checked := intent
	checked.AcceptedPlanID, checked.AcceptedPlanRevision, checked.AcceptedPlanCurrent = id, strings.Repeat("0", 64), true
	if err := validateIntentContext(&checked); err != nil {
		return AcceptedPlanReference{}, err
	}
	raw, err := json.Marshal(acceptedPlanRecord{Version: 1, Context: intent, Source: source.Fingerprint})
	if err != nil {
		return AcceptedPlanReference{}, err
	}
	tmp, err := os.CreateTemp(dir, ".accept-*")
	if err != nil {
		return AcceptedPlanReference{}, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return AcceptedPlanReference{}, err
	}
	if err := tmp.Close(); err != nil {
		return AcceptedPlanReference{}, err
	}
	if err := os.Rename(tmp.Name(), filename); err != nil {
		return AcceptedPlanReference{}, err
	}
	return AcceptedPlanReference{ID: id, Revision: acceptedRevision(raw)}, nil
}

func acceptedRevision(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// Resolve checks both the selected receipt revision and current source identity.
// Local same-user file tampering is outside this store's trust boundary.
func (s AcceptedPlanStore) Resolve(ctx context.Context, ref AcceptedPlanReference, repository, root string) (*IntentContext, error) {
	filename, err := s.filename(ref.ID)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128*1024 {
		return nil, fmt.Errorf("accepted plan is unavailable or invalid")
	}
	raw, err := os.ReadFile(filename)
	if err != nil || ref.Revision == "" || acceptedRevision(raw) != ref.Revision {
		return nil, fmt.Errorf("accepted plan revision is missing or has changed")
	}
	var record acceptedPlanRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.Version != 1 {
		return nil, fmt.Errorf("invalid accepted plan record")
	}
	if repository == "" || record.Context.Repository != repository {
		return nil, fmt.Errorf("accepted plan repository does not match")
	}
	source, err := sourceidentity.Discover(ctx, root)
	if err != nil || source.Fingerprint == "" || source.Fingerprint != record.Source {
		return nil, fmt.Errorf("accepted plan source has changed or cannot be verified")
	}
	intent := record.Context
	intent.locallyVerified = true
	intent.AcceptedPlanID, intent.AcceptedPlanRevision, intent.AcceptedPlanCurrent = ref.ID, ref.Revision, true
	if err := validateIntentContext(&intent); err != nil {
		return nil, err
	}
	return &intent, nil
}
