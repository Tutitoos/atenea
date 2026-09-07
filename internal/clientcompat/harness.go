package clientcompat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpcompat"
	"github.com/Tutitoos/atenea/internal/wrap"
)

// Injection is the only configuration a pilot client receives. All writable
// homes are disposable, and the run/workflow ids are explicit correlations,
// never inferred from process state.
type Injection struct {
	Client           string
	ClientInfo       string
	Profile          string
	Transport        string
	Overlay          string
	OverlayMechanism string
	RunID            string
	WorkflowID       string
	// Args models the explicit argv overlay a real client would receive. It is
	// kept alongside Environment so fixtures can prove both injection paths.
	Args        []string
	Environment map[string]string
}

// WorkflowSnapshot is the read-only surface a fake client consumes. It is
// intentionally smaller than the workflow API: the pilot proves continuity,
// cursor recovery, checklist identity and deterministic progress formatting.
type WorkflowSnapshot struct {
	RunID      string
	WorkflowID string
	Cursor     string
	Requested  string
	Observed   string
	Checklist  []ChecklistItem
	Accepted   int
	Total      int
	Progress   int
	Bar        string
	// ActivityCursor is the cursor returned by workflow.status. ActivityAfter
	// exists for source compatibility with early pilot fixtures; requests use
	// TranscriptEvent.ActivityAfter and never this response field.
	ActivityCursor     int64
	ActivityAfter      int64
	StatusInvocationID string
	Profile            string
	Permissions        []string
	Activity           []ActivityRecord
	Notices            []NoticeRecord
	Effects            []string
	EffectIDs          []string
	// Presentation is retained for source compatibility but is never trusted
	// by RunPilot. A client must prove rendering in its transcript.
	Presentation State
}

// ChecklistItem is part of ATENEA's public orchestration contract.
type ChecklistItem struct {
	ID       string
	Title    string
	Accepted bool
	Evidence string
}

// ActivityRecord is part of ATENEA's public orchestration contract.
type ActivityRecord struct {
	InvocationID string
	Kind         string
	Text         string
}

// NoticeRecord is part of ATENEA's public orchestration contract.
type NoticeRecord struct {
	InvocationID string
	Markdown     string
}

// Transcript is an auditable protocol/client record. The harness requires a
// handshake, correlated workflow.status, activity and externally observed
// rendering event before it upgrades a corresponding claim.
type Transcript struct {
	SessionID  string
	Reconnect  bool
	Provenance Provenance
	Events     []TranscriptEvent
}

// Provenance is part of ATENEA's public orchestration contract.
type Provenance struct {
	Level             EvidenceLevel
	Source            EvidenceLevel
	Executable        string
	ExecutableVersion string
	ExecutableSHA256  string
	Recorder          string
}

// ControlledRecorder is part of ATENEA's public orchestration contract.
const ControlledRecorder = "atenea-mcp-recorder/v1"

// PresentationObservation is part of ATENEA's public orchestration contract.
type PresentationObservation struct {
	Attested     bool
	Source       EvidenceLevel
	RunID        string
	WorkflowID   string
	InvocationID string
	Sequence     int
	Text         string
}

// PresentationObserver is part of ATENEA's public orchestration contract.
type PresentationObserver interface {
	ObservePresentation(context.Context, string, string) (PresentationObservation, error)
}

// TranscriptEvent is part of ATENEA's public orchestration contract.
type TranscriptEvent struct {
	Sequence        int
	Kind            string
	ID              string
	Method          string
	RunID           string
	WorkflowID      string
	ClientInfo      string
	ProtocolVersion string
	Transport       string
	Profile         string
	Requested       string
	Observed        string
	InvocationID    string
	// ActivityAfter is the cursor sent by a workflow.status request. It is
	// deliberately separate from ActivityCursor, which is the cursor returned
	// by its response.
	ActivityAfter  int64
	ActivityCursor int64
	Effects        []string
	EffectIDs      []string
	Source         string
	Attested       bool
	Text           string
}

// TranscriptProvider is part of ATENEA's public orchestration contract.
type TranscriptProvider interface {
	Transcript(context.Context) (Transcript, error)
}

// PilotClient is part of ATENEA's public orchestration contract.
type PilotClient interface {
	Connect(context.Context, Injection) (PilotConnection, error)
}

// PilotConnection is part of ATENEA's public orchestration contract.
type PilotConnection interface {
	WorkflowStatus(context.Context, string) (WorkflowSnapshot, error)
	Reconnect(context.Context) (PilotConnection, error)
	Close() error
}

// PilotOptions is part of ATENEA's public orchestration contract.
type PilotOptions struct {
	RunID      string
	WorkflowID string
	Clients    map[string]PilotClient
	Sandbox    *Sandbox
	Provenance map[string]Provenance
	Observers  map[string]PresentationObserver
}

// PilotReport is part of ATENEA's public orchestration contract.
type PilotReport struct {
	RunID        string            `json:"run_id"`
	WorkflowID   string            `json:"workflow_id"`
	Matrix       Matrix            `json:"matrix"`
	Sentinel     State             `json:"sentinel"`
	SandboxRoots map[string]string `json:"sandbox_roots,omitempty"`
	Evidence     []Evidence        `json:"evidence,omitempty"`
}

// Sandbox creates isolated HOME/CODEX_HOME/XDG trees for a pilot run. The
// external sentinel must be outside Root; the harness checks it byte-for-byte
// after every client so a fake cannot silently write the real user profile.
type Sandbox struct {
	Root             string
	ExternalSentinel string
	RootHashes       map[string]string
	baseline         []byte
}

// NewSandbox is part of ATENEA's public orchestration contract.
func NewSandbox(root, externalSentinel string) (*Sandbox, error) {
	root = filepath.Clean(root)
	externalSentinel = filepath.Clean(externalSentinel)
	if root == "." || root == string(filepath.Separator) || externalSentinel == "." || externalSentinel == string(filepath.Separator) {
		return nil, errors.New("client compatibility sandbox paths must be specific")
	}
	rel, err := filepath.Rel(root, externalSentinel)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return nil, errors.New("external sentinel must be outside the pilot sandbox")
	}
	if _, err := os.Stat(externalSentinel); errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("external sentinel must pre-exist; the pilot never creates user files")
	} else if err != nil {
		return nil, err
	}
	if entries, err := os.ReadDir(root); err == nil && len(entries) != 0 {
		return nil, errors.New("pilot sandbox must start empty")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	for _, name := range []string{"home", "codex", "config", "state", "xdg-data"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			return nil, err
		}
	}
	baseline, err := os.ReadFile(externalSentinel)
	if err != nil {
		return nil, err
	}
	hashes := make(map[string]string, 5)
	for _, name := range []string{"home", "codex", "config", "state", "xdg-data"} {
		path := filepath.Join(root, name)
		hash, err := hashTree(path)
		if err != nil {
			return nil, err
		}
		hashes[name] = hash
	}
	return &Sandbox{Root: root, ExternalSentinel: externalSentinel, RootHashes: hashes, baseline: baseline}, nil
}

func hashTree(root string) (string, error) {
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00", rel)
		hash.Write(data)
		hash.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// Environment is part of ATENEA's public orchestration contract.
func (s *Sandbox) Environment(runID string) map[string]string {
	return map[string]string{
		"HOME":                     filepath.Join(s.Root, "home"),
		"CODEX_HOME":               filepath.Join(s.Root, "codex"),
		"XDG_CONFIG_HOME":          filepath.Join(s.Root, "config"),
		"XDG_STATE_HOME":           filepath.Join(s.Root, "state"),
		"XDG_DATA_HOME":            filepath.Join(s.Root, "xdg-data"),
		"ATENEA_PILOT_RUN_ID":      runID,
		"ATENEA_PILOT_WORKFLOW_ID": "",
	}
}

// ValidateEnvironment is part of ATENEA's public orchestration contract.
func (s *Sandbox) ValidateEnvironment(environment map[string]string) error {
	allowed := map[string]bool{
		"HOME": true, "CODEX_HOME": true, "XDG_CONFIG_HOME": true,
		"XDG_STATE_HOME": true, "XDG_DATA_HOME": true,
		"ATENEA_PILOT_RUN_ID": true, "ATENEA_PILOT_WORKFLOW_ID": true,
		"ATENEA_PILOT_CLIENT": true, "ATENEA_PILOT_PROFILE": true,
		"ATENEA_PILOT_TRANSPORT": true, "ATENEA_PILOT_OVERLAY": true,
		"OPENCODE_CONFIG_CONTENT": true,
	}
	for key, value := range environment {
		if !allowed[key] {
			return fmt.Errorf("pilot environment key %q is outside the allowlist", key)
		}
		if strings.HasSuffix(key, "HOME") || strings.HasSuffix(key, "_HOME") {
			rel, err := filepath.Rel(s.Root, filepath.Clean(value))
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("pilot environment %s escapes sandbox", key)
			}
		}
	}
	return nil
}

// VerifySentinel is part of ATENEA's public orchestration contract.
func (s *Sandbox) VerifySentinel() error {
	current, err := os.ReadFile(s.ExternalSentinel)
	if err != nil {
		return err
	}
	if !slices.Equal(current, s.baseline) {
		return errors.New("external sentinel changed")
	}
	return nil
}

// RecalculateRootHashes records only the explicitly owned disposable roots;
// it never walks or reports a user's real profile. Callers can compare this
// post-run snapshot with RootHashes to audit overlay writes.
func (s *Sandbox) RecalculateRootHashes() map[string]string {
	hashes := make(map[string]string, len(s.RootHashes))
	for name := range s.RootHashes {
		hash, err := hashTree(filepath.Join(s.Root, name))
		if err != nil {
			hashes[name] = "unavailable"
			continue
		}
		hashes[name] = hash
	}
	return hashes
}

// BuildInjection is part of ATENEA's public orchestration contract.
func BuildInjection(spec ClientSpec, sandbox *Sandbox, runID, workflowID string) (Injection, error) {
	environment := sandbox.Environment(runID)
	environment["ATENEA_PILOT_WORKFLOW_ID"] = workflowID
	environment["ATENEA_PILOT_CLIENT"] = spec.Client
	environment["ATENEA_PILOT_PROFILE"] = spec.Profile
	environment["ATENEA_PILOT_TRANSPORT"] = spec.Transport
	environment["ATENEA_PILOT_OVERLAY"] = spec.Overlay
	overlay, err := (wrap.Plan{}).ClientOverlay(spec.Client, wrap.Core{ID: "atenea", Command: []string{"atenea", "mcp"}})
	if err != nil {
		return Injection{}, err
	}
	args := overlay.Args
	for key, value := range overlay.Env {
		environment[key] = value
	}
	return Injection{
		Client: spec.Client, ClientInfo: spec.ClientInfo, Profile: spec.Profile, Transport: spec.Transport,
		Overlay: spec.Overlay, OverlayMechanism: spec.OverlayMechanism, RunID: runID, WorkflowID: workflowID,
		Args:        args,
		Environment: environment,
	}, nil
}

// RunPilot is part of ATENEA's public orchestration contract.
func RunPilot(ctx context.Context, options PilotOptions) (PilotReport, error) {
	runID, workflowID := sanitize(options.RunID), sanitize(options.WorkflowID)
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(workflowID) == "" {
		return PilotReport{}, errors.New("pilot requires explicit run_id and workflow_id")
	}
	if options.Sandbox == nil {
		return PilotReport{}, errors.New("pilot requires an isolated sandbox")
	}
	report := PilotReport{RunID: runID, WorkflowID: workflowID, Matrix: NewMatrix(timeNow()), Sentinel: Pass}
	for _, spec := range Specs() {
		entry, _ := report.Matrix.Entry(spec.Client)
		if !spec.Injection {
			unknown := EvidenceCheck(Unknown, Evidence{ID: "injection", Detail: "no safe generic injection; manual surface required"})
			entry.Declared, entry.Connected, entry.Tested = unknown, unknown, unknown
			entry.Presentation, entry.Reconnect = unknown, unknown
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		adapter, available := options.Clients[spec.Client]
		if !available {
			// No adapter means no observation. In particular, do not turn an
			// intentionally unsupported injection into a partial success for
			// checks that were never executed.
			unknown := EvidenceCheck(Unknown, Evidence{ID: "pilot", Detail: "no client fixture or safe injection registered"})
			entry.Declared, entry.Connected, entry.Tested = unknown, unknown, unknown
			entry.Presentation, entry.Reconnect = unknown, unknown
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		provenance, hasProvenance := options.Provenance[spec.Client]
		if !hasProvenance || !validProvenance(provenance) {
			unknown := EvidenceCheck(Unknown, Evidence{ID: "provenance", Detail: "a controlled transcript provenance record is required"})
			entry.Declared, entry.Connected, entry.Tested = unknown, unknown, unknown
			entry.Presentation, entry.Reconnect = unknown, unknown
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		entry.EvidenceLevel, entry.Source = provenance.Level, provenance.Source
		if strings.TrimSpace(provenance.ExecutableVersion) != "" {
			entry.Version = sanitize(provenance.ExecutableVersion)
		}
		injection, err := BuildInjection(spec, options.Sandbox, runID, workflowID)
		if err != nil {
			entry.Connected = EvidenceCheck(Unknown, Evidence{ID: "overlay", Detail: err.Error()})
			entry.Tested, entry.Presentation, entry.Reconnect = unknownChecks("overlay unavailable")
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		if err := options.Sandbox.ValidateEnvironment(injection.Environment); err != nil {
			entry.Declared = EvidenceCheck(Pass, Evidence{ID: "injection", Detail: "fixture registered"})
			entry.Connected = EvidenceCheck(Fail, Evidence{ID: "sandbox", Detail: err.Error()})
			entry.Tested, entry.Presentation, entry.Reconnect = unknownChecks("not tested")
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		if err := validateInjection(injection); err != nil {
			entry.Connected = EvidenceCheck(Fail, Evidence{ID: "injection", Detail: err.Error()})
			entry.Tested, entry.Presentation, entry.Reconnect = unknownChecks("invalid injection")
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		entry.Declared = EvidenceCheck(Pass, Evidence{ID: "injection", Detail: "isolated overlay injected"})
		connection, err := adapter.Connect(ctx, injection)
		if err != nil {
			entry.Connected = EvidenceCheck(Fail, Evidence{ID: "connect", Detail: err.Error()})
			entry.Tested, entry.Presentation, entry.Reconnect = unknownChecks("not tested after connect failure")
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		first, err := connection.WorkflowStatus(ctx, workflowID)
		if err != nil {
			_ = connection.Close()
			entry.Connected = EvidenceCheck(Unknown, Evidence{ID: "protocol", Detail: "workflow.status could not be correlated"})
			entry.Tested = EvidenceCheck(Fail, Evidence{ID: "workflow.status", Detail: err.Error()})
			entry.Presentation = unknownCheck("not tested")
			entry.Reconnect = unknownCheck("not tested")
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		if err := validateSnapshotForRun(first, workflowID, runID); err != nil {
			_ = connection.Close()
			entry.Connected = EvidenceCheck(Unknown, Evidence{ID: "protocol", Detail: "workflow.status failed contract validation"})
			entry.Tested = EvidenceCheck(Fail, Evidence{ID: "workflow.status", Detail: err.Error()})
			entry.Presentation = unknownCheck("not tested")
			entry.Reconnect = unknownCheck("not tested")
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		transcriptProvider, ok := connection.(TranscriptProvider)
		if !ok {
			_ = connection.Close()
			entry.Connected = EvidenceCheck(Unknown, Evidence{ID: "transcript", Detail: "client did not expose an auditable protocol transcript"})
			entry.Tested, entry.Presentation, entry.Reconnect = unknownChecks("transcript required")
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		transcript, err := transcriptProvider.Transcript(ctx)
		if err == nil {
			err = validateTranscript(transcript, injection, first, provenance)
		}
		if err != nil {
			_ = connection.Close()
			if err == nil {
				err = errors.New("invalid client transcript")
			}
			entry.Connected = EvidenceCheck(Unknown, Evidence{ID: "transcript", Detail: err.Error()})
			entry.Tested, entry.Presentation, entry.Reconnect = unknownChecks("transcript not validated")
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		entry.Connected = EvidenceCheck(Pass, Evidence{ID: "handshake", Detail: "correlated client handshake"})
		entry.Requested, entry.Observed = transcriptVersions(transcript, first)
		entry.Tested = EvidenceCheck(Pass, Evidence{ID: "workflow.status", Detail: fmt.Sprintf("cursor %s; checklist %d/%d; progress %d%%", first.Cursor, first.Accepted, first.Total, first.Progress)})
		entry.Presentation = observePresentation(ctx, options.Observers[spec.Client], provenance, runID, workflowID, transcriptInvocation(transcript))
		if err := connection.Close(); err != nil {
			entry.Reconnect = EvidenceCheck(Fail, Evidence{ID: "close", Detail: err.Error()})
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		reconnected, err := connection.Reconnect(ctx)
		if err != nil {
			entry.Reconnect = EvidenceCheck(Fail, Evidence{ID: "reconnect", Detail: err.Error()})
			report.Matrix = replaceEntry(report.Matrix, entry)
			continue
		}
		second, err := reconnected.WorkflowStatus(ctx, workflowID)
		var reconnectTranscript Transcript
		if err == nil {
			provider, ok := reconnected.(TranscriptProvider)
			if !ok {
				err = errors.New("reconnected client did not expose a new transcript")
			} else {
				reconnectTranscript, err = provider.Transcript(ctx)
				if err == nil {
					err = validateTranscript(reconnectTranscript, injection, second, provenance)
				}
			}
		}
		_ = reconnected.Close()
		if err != nil {
			entry.Reconnect = EvidenceCheck(Fail, Evidence{ID: "workflow.status", Detail: err.Error()})
		} else if err := compareSnapshots(first, second, workflowID, runID); err != nil {
			entry.Reconnect = EvidenceCheck(Fail, Evidence{ID: "continuity", Detail: err.Error()})
		} else if err := compareTranscriptReplay(transcript, reconnectTranscript); err != nil {
			entry.Reconnect = EvidenceCheck(Fail, Evidence{ID: "replay", Detail: err.Error()})
		} else if reconnectTranscript.SessionID == transcript.SessionID {
			entry.Reconnect = EvidenceCheck(Fail, Evidence{ID: "handshake", Detail: "reconnect reused the previous transport session"})
		} else {
			entry.Reconnect = EvidenceCheck(Pass, Evidence{ID: "continuity", Detail: "cursor/checklist/progress survived reconnect"})
		}
		report.Matrix = replaceEntry(report.Matrix, entry)
	}
	if err := options.Sandbox.VerifySentinel(); err != nil {
		report.Sentinel = Fail
		report.Evidence = append(report.Evidence, Evidence{ID: "external-sentinel", Detail: err.Error()})
	} else {
		report.Evidence = append(report.Evidence, Evidence{ID: "external-sentinel", Detail: "unchanged"})
	}
	report.SandboxRoots = options.Sandbox.RecalculateRootHashes()
	return report, nil
}

func compareTranscriptReplay(first, second Transcript) error {
	if !second.Reconnect || second.SessionID == first.SessionID {
		return errors.New("reconnect did not create a new read-only session")
	}
	firstIDs := map[string]bool{}
	for _, event := range first.Events {
		if event.Kind != "handshake" && event.ID != "" {
			firstIDs["id:"+event.ID] = true
		}
		if event.InvocationID != "" {
			firstIDs["invocation:"+event.InvocationID] = true
		}
	}
	for _, event := range second.Events {
		if event.Kind != "handshake" && event.ID != "" && firstIDs["id:"+event.ID] {
			return errors.New("reconnect replayed an earlier protocol id")
		}
		if event.InvocationID != "" && firstIDs["invocation:"+event.InvocationID] {
			return errors.New("reconnect replayed an earlier invocation")
		}
	}
	firstAfter, firstCursor, firstOK := transcriptActivityCursors(first)
	secondAfter, secondCursor, secondOK := transcriptActivityCursors(second)
	if !firstOK || !secondOK || secondAfter != firstCursor || secondCursor < secondAfter || firstAfter > firstCursor {
		return errors.New("reconnect did not send the prior activity cursor or return a valid cursor")
	}
	return nil
}

func transcriptActivityCursors(transcript Transcript) (requestAfter, responseCursor int64, ok bool) {
	requestSeen, responseSeen := false, false
	for _, event := range transcript.Events {
		if event.Method != "workflow.status" {
			continue
		}
		if event.Kind == "request" {
			requestAfter, requestSeen = event.ActivityAfter, true
		}
		if event.Kind == "response" {
			responseCursor, responseSeen = event.ActivityCursor, true
		}
	}
	return requestAfter, responseCursor, requestSeen && responseSeen
}

func validateInjection(injection Injection) error {
	if injection.Environment["ATENEA_PILOT_RUN_ID"] != injection.RunID || injection.Environment["ATENEA_PILOT_WORKFLOW_ID"] != injection.WorkflowID || injection.Environment["ATENEA_PILOT_CLIENT"] != injection.Client || injection.Environment["ATENEA_PILOT_PROFILE"] != injection.Profile || injection.Environment["ATENEA_PILOT_TRANSPORT"] != injection.Transport || injection.Environment["ATENEA_PILOT_OVERLAY"] != injection.Overlay {
		return errors.New("argv/environment correlation mismatch")
	}
	switch injection.Client {
	case "codex":
		if len(injection.Args) != 2 || injection.Args[0] != "-c" || !strings.HasPrefix(injection.Args[1], "mcp_servers.atenea=") {
			return errors.New("codex overlay must use -c mcp_servers.atenea")
		}
	case "claude":
		if len(injection.Args) != 2 || injection.Args[0] != "--mcp-config" || injection.Args[1] == "" {
			return errors.New("Claude overlay must use --mcp-config") //nolint:staticcheck // Product name starts the diagnostic.
		}
	case "opencode":
		if injection.Environment["OPENCODE_CONFIG_CONTENT"] == "" {
			return errors.New("OpenCode overlay must use OPENCODE_CONFIG_CONTENT")
		}
	case "chatgpt", "omp":
		return errors.New("client has no safe generic injection")
	}
	return nil
}

func unknownChecks(detail string) (Check, Check, Check) {
	check := unknownCheck(detail)
	return check, check, check
}

func unknownCheck(detail string) Check {
	return EvidenceCheck(Unknown, Evidence{ID: "not-tested", Detail: detail})
}

// timeNow is a variable for deterministic tests without exposing a clock in
// the public pilot contract.
var timeNow = func() time.Time { return time.Now().UTC() }

func validateSnapshotForRun(snapshot WorkflowSnapshot, workflowID, runID string) error {
	if snapshot.WorkflowID != workflowID || strings.TrimSpace(snapshot.Cursor) == "" {
		return errors.New("workflow.status did not return the requested workflow and cursor")
	}
	if runID != "" && snapshot.RunID != runID {
		return errors.New("workflow.status did not preserve the correlated run")
	}
	if strings.TrimSpace(snapshot.Profile) == "" || strings.TrimSpace(snapshot.Requested) == "" || strings.TrimSpace(snapshot.Observed) == "" || strings.TrimSpace(snapshot.StatusInvocationID) == "" {
		return errors.New("workflow.status omitted profile or protocol versions")
	}
	if _, err := mcpcompat.Parse(snapshot.Requested); err != nil {
		return fmt.Errorf("workflow.status requested version: %w", err)
	}
	if _, err := mcpcompat.Parse(snapshot.Observed); err != nil {
		return fmt.Errorf("workflow.status observed version: %w", err)
	}
	if len(snapshot.Permissions) == 0 {
		return errors.New("workflow.status omitted application permissions")
	}
	if snapshot.Total <= 0 || snapshot.Accepted < 0 || snapshot.Accepted > snapshot.Total || snapshot.Progress != snapshot.Accepted*100/snapshot.Total {
		return errors.New("workflow.status returned inconsistent checklist progress")
	}
	if snapshot.Bar != expectedBar(snapshot.Progress) {
		return errors.New("workflow.status returned an inconsistent progress bar")
	}
	if len(snapshot.Checklist) != snapshot.Total {
		return errors.New("workflow.status returned an incomplete checklist")
	}
	seen := make(map[string]bool, len(snapshot.Checklist))
	accepted := 0
	for _, item := range snapshot.Checklist {
		if strings.TrimSpace(item.ID) == "" || seen[item.ID] || strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Evidence) == "" {
			return errors.New("workflow.status returned an incomplete checklist item")
		}
		seen[item.ID] = true
		if item.Accepted {
			accepted++
		}
	}
	if accepted != snapshot.Accepted {
		return errors.New("workflow.status checklist count disagrees with accepted count")
	}
	if err := validateActivity(snapshot); err != nil {
		return err
	}
	return nil
}

func validateActivity(snapshot WorkflowSnapshot) error {
	if len(snapshot.Activity) == 0 || len(snapshot.Notices) == 0 {
		return errors.New("workflow.status omitted activity or notice evidence")
	}
	activitySeen := map[string]bool{}
	for _, record := range snapshot.Activity {
		if strings.TrimSpace(record.InvocationID) == "" || activitySeen[record.InvocationID] {
			return errors.New("workflow.status returned duplicate activity invocation ids")
		}
		activitySeen[record.InvocationID] = true
	}
	noticeSeen := map[string]bool{}
	for _, notice := range snapshot.Notices {
		if strings.TrimSpace(notice.InvocationID) == "" || noticeSeen[notice.InvocationID] || strings.TrimSpace(notice.Markdown) == "" {
			return errors.New("workflow.status returned duplicate or incomplete notices")
		}
		noticeSeen[notice.InvocationID] = true
	}
	seen := map[string]bool{}
	if len(snapshot.Effects) > 0 && len(snapshot.Effects) != len(snapshot.EffectIDs) {
		return errors.New("workflow.status effect records lack one effect id each")
	}
	for _, effect := range snapshot.Effects {
		if strings.TrimSpace(effect) == "" || seen[effect] {
			return errors.New("workflow.status returned duplicate effects")
		}
		seen[effect] = true
	}
	seen = map[string]bool{}
	for _, effectID := range snapshot.EffectIDs {
		if strings.TrimSpace(effectID) == "" || seen[effectID] {
			return errors.New("workflow.status returned duplicate effect ids")
		}
		seen[effectID] = true
	}
	for _, record := range snapshot.Activity {
		if activityRequiresEffectID(record.Kind) && len(snapshot.EffectIDs) == 0 {
			return errors.New("effectful activity lacks effect ids")
		}
	}
	return nil
}

func activityRequiresEffectID(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "read", "read-only", "query", "inspect", "observe", "status", "lookup":
		return false
	default:
		return strings.Contains(strings.ToLower(kind), "write") || strings.Contains(strings.ToLower(kind), "effect") || strings.Contains(strings.ToLower(kind), "mutat") || strings.Contains(strings.ToLower(kind), "create") || strings.Contains(strings.ToLower(kind), "update") || strings.Contains(strings.ToLower(kind), "delete") || strings.Contains(strings.ToLower(kind), "commit") || strings.Contains(strings.ToLower(kind), "push") || strings.Contains(strings.ToLower(kind), "install") || strings.Contains(strings.ToLower(kind), "deploy") || strings.Contains(strings.ToLower(kind), "migrat")
	}
}

func expectedBar(progress int) string {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	filled := progress * 20 / 100
	return strings.Repeat("█", filled) + strings.Repeat("░", 20-filled)
}

func validProvenance(provenance Provenance) bool {
	return ValidateProvenance(provenance) == nil
}

// ValidateProvenance is part of ATENEA's public orchestration contract.
func ValidateProvenance(provenance Provenance) error {
	if !provenance.Level.Valid() || provenance.Level == EvidenceUnknown || provenance.Source != provenance.Level || strings.TrimSpace(provenance.Recorder) == "" {
		return errors.New("provenance has no valid level, source, or recorder")
	}
	if provenance.Level == EvidenceFixture && provenance.Recorder != "fixture-recorder" {
		return errors.New("fixture provenance must name the fixture recorder")
	}
	if provenance.Level == EvidenceReal {
		if provenance.Recorder != ControlledRecorder || strings.TrimSpace(provenance.Executable) == "" || strings.TrimSpace(provenance.ExecutableVersion) == "" || len(provenance.ExecutableSHA256) != 64 {
			return errors.New("real provenance requires the controlled recorder and executable identity")
		}
		for _, r := range provenance.ExecutableSHA256 {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
				return errors.New("real provenance executable hash is not hexadecimal")
			}
		}
		data, err := os.ReadFile(provenance.Executable)
		if err != nil {
			return fmt.Errorf("read executable for provenance: %w", err)
		}
		got := fmt.Sprintf("%x", sha256.Sum256(data))
		if !strings.EqualFold(got, provenance.ExecutableSHA256) {
			return errors.New("real provenance executable hash mismatch")
		}
		version, err := exec.Command(provenance.Executable, "--version").Output()
		if err != nil || strings.TrimSpace(string(version)) == "" || !strings.Contains(string(version), provenance.ExecutableVersion) {
			return errors.New("real provenance executable version is not verified")
		}
	}
	return nil
}

func validateTranscript(transcript Transcript, injection Injection, snapshot WorkflowSnapshot, expected Provenance) error {
	if !validProvenance(transcript.Provenance) || transcript.Provenance.Level != expected.Level || transcript.Provenance.Source != expected.Source || transcript.Provenance.ExecutableSHA256 != expected.ExecutableSHA256 {
		return errors.New("transcript provenance is missing or does not match the controlled source")
	}
	handshake, status, statusResponse, activity, notices := false, false, false, false, false
	noticeAt, requestAt, responseAt, activityAt, renderAt := -1, -1, -1, -1, -1
	statusRequestAt, statusResponseAt := -1, -1
	statusRequestAfter, statusResponseCursor := int64(0), int64(0)
	toolRequestID, toolInvocation := "", ""
	noticeInvocation, activityInvocation := "", ""
	noticeCount, toolRequestCount := 0, 0
	lastSequence := 0
	ids := map[string]bool{}
	invocations := map[string]map[string]bool{}
	for index, event := range transcript.Events {
		if event.Sequence > 0 {
			if event.Sequence <= lastSequence {
				return errors.New("transcript sequence is not monotonic")
			}
			lastSequence = event.Sequence
		}
		if event.ID != "" {
			if ids[event.ID] {
				return errors.New("transcript contains duplicate protocol ids")
			}
			ids[event.ID] = true
		}
		if event.InvocationID != "" {
			if invocations[event.InvocationID] == nil {
				invocations[event.InvocationID] = map[string]bool{}
			}
			if invocations[event.InvocationID][event.Kind] {
				return errors.New("transcript contains duplicate invocation ids")
			}
			invocations[event.InvocationID][event.Kind] = true
		}
		switch event.Kind {
		case "handshake":
			if event.RunID == injection.RunID && event.ClientInfo == injection.ClientInfo && event.ProtocolVersion == snapshot.Requested && event.Requested == snapshot.Requested && event.Observed == snapshot.Observed && event.Transport == injection.Transport && event.Profile == injection.Profile {
				if _, err := mcpcompat.Parse(event.ProtocolVersion); err != nil {
					return fmt.Errorf("transcript handshake version: %w", err)
				}
				if _, err := mcpcompat.Parse(event.Observed); err != nil {
					return fmt.Errorf("transcript observed version: %w", err)
				}
				handshake = true
			}
		case "request", "response":
			if event.Method == "workflow.status" {
				if len(event.Effects) != 0 || len(event.EffectIDs) != 0 {
					return errors.New("workflow.status transcript contains effects")
				}
				if event.Kind == "request" && event.ID != "" && event.InvocationID == snapshot.StatusInvocationID && event.RunID == injection.RunID && event.WorkflowID == injection.WorkflowID {
					status = true
					statusRequestAt, statusRequestAfter = index, event.ActivityAfter
				}
				if event.Kind == "response" && event.ID != "" && event.InvocationID == snapshot.StatusInvocationID && event.RunID == injection.RunID && event.WorkflowID == injection.WorkflowID {
					statusResponse = true
					statusResponseAt, statusResponseCursor = index, event.ActivityCursor
				}
			}
			if event.Method == "tools/call" && event.InvocationID == "" {
				return errors.New("tool request lacks invocation correlation")
			}
			if event.Method == "tools/call" && event.Kind == "request" {
				toolRequestCount++
				if requestAt >= 0 {
					return errors.New("transcript contains duplicate tool requests")
				}
				requestAt, toolRequestID, toolInvocation = index, event.ID, event.InvocationID
			} else if event.Method == "tools/call" && event.Kind == "response" {
				if responseAt >= 0 || event.ID == "" {
					return errors.New("transcript contains duplicate tool responses")
				}
				if toolInvocation == "" || event.InvocationID != toolInvocation {
					return errors.New("tool response invocation does not match request")
				}
				responseAt = index
			}
		case "activity":
			if event.InvocationID != "" && event.RunID == injection.RunID && event.WorkflowID == injection.WorkflowID && activityAt < 0 {
				activityAt = index
				activityInvocation = event.InvocationID
				activity = true
			}
		case "notice":
			if event.InvocationID != "" && event.RunID == injection.RunID && event.WorkflowID == injection.WorkflowID && strings.HasPrefix(strings.TrimSpace(event.Text), "> **ATENEA") {
				noticeCount++
				if noticeAt < 0 {
					noticeAt = index
					noticeInvocation = event.InvocationID
					notices = true
				}
			}
		case "render":
			if event.InvocationID != "" && event.RunID == injection.RunID && event.WorkflowID == injection.WorkflowID && strings.Contains(event.Text, "Progreso") && strings.Contains(event.Text, expectedBar(snapshot.Progress)) && strings.Contains(event.Text, "[x]") {
				renderAt = index
			}
		}
	}
	if strings.TrimSpace(transcript.SessionID) == "" {
		return errors.New("transcript lacks a transport session id")
	}
	if !handshake || !status || !statusResponse || statusRequestAt < 0 || statusResponseAt < 0 {
		return errors.New("transcript lacks correlated handshake or workflow.status")
	}
	if statusResponseAt <= statusRequestAt {
		return errors.New("workflow.status response precedes its request")
	}
	if statusResponseCursor < statusRequestAfter || statusResponseCursor != snapshotActivityCursor(snapshot) {
		return errors.New("workflow.status response cursor is before the request cursor or snapshot")
	}
	if transcript.Reconnect {
		if noticeCount != 0 || toolRequestCount != 0 || activity || notices || requestAt >= 0 || responseAt >= 0 || activityAt >= 0 || renderAt >= 0 {
			return errors.New("reconnect transcript contains a write or tool replay")
		}
		if snapshot.StatusInvocationID == "" {
			return errors.New("reconnect status invocation is missing")
		}
		return nil
	}
	if !activity || !notices {
		return errors.New("transcript lacks correlated activity")
	}
	if noticeCount != 1 || toolRequestCount != 1 || noticeAt < 0 || requestAt < 0 || responseAt < 0 || activityAt < 0 || renderAt < 0 || noticeAt >= requestAt || requestAt >= responseAt || responseAt >= activityAt || activityAt >= renderAt {
		return errors.New("transcript activity is not ordered notice, request, response, activity, render")
	}
	if toolRequestID == "" || activityInvocation != toolInvocation || noticeInvocation != toolInvocation {
		return errors.New("transcript lacks a correlated tools/call")
	}
	for _, record := range snapshot.Activity {
		if record.InvocationID != toolInvocation || !invocations[record.InvocationID]["activity"] {
			return errors.New("activity invocation is not present in the transcript")
		}
	}
	for _, notice := range snapshot.Notices {
		if notice.InvocationID != toolInvocation || !invocations[notice.InvocationID]["notice"] {
			return errors.New("notice invocation is not present in the transcript")
		}
	}
	return nil
}

func transcriptVersions(transcript Transcript, snapshot WorkflowSnapshot) (string, string) {
	requested, observed := snapshot.Requested, snapshot.Observed
	for _, event := range transcript.Events {
		if event.Kind == "handshake" {
			if event.Requested != "" {
				requested = event.Requested
			}
			if event.Observed != "" {
				observed = event.Observed
			}
		}
	}
	return requested, observed
}

func transcriptInvocation(transcript Transcript) string {
	for _, event := range transcript.Events {
		if event.Kind == "activity" && event.InvocationID != "" {
			return event.InvocationID
		}
	}
	return ""
}

func observePresentation(ctx context.Context, observer PresentationObserver, provenance Provenance, runID, workflowID, invocationID string) Check {
	if observer == nil || provenance.Level != EvidenceReal || provenance.Source != EvidenceReal {
		return EvidenceCheck(Unknown, Evidence{ID: "chat", Detail: "no independent presentation observer"})
	}
	observation, err := observer.ObservePresentation(ctx, runID, workflowID)
	if err != nil || !observation.Attested || observation.Source != EvidenceReal || observation.Sequence <= 0 || observation.RunID != runID || observation.WorkflowID != workflowID || observation.InvocationID != invocationID || !strings.Contains(observation.Text, "Progreso") || !strings.Contains(observation.Text, "[x]") {
		if err == nil {
			err = errors.New("observer did not provide an independent correlated real attestation")
		}
		return EvidenceCheck(Unknown, Evidence{ID: "chat", Detail: err.Error()})
	}
	return EvidenceCheck(Pass, Evidence{ID: "chat", Detail: "independent real presentation observer attested the render"})
}

func compareSnapshots(first, second WorkflowSnapshot, workflowID string, runID ...string) error {
	expectedRunID := ""
	if len(runID) > 0 {
		expectedRunID = runID[0]
	}
	if err := validateSnapshotForRun(second, workflowID, expectedRunID); err != nil {
		return err
	}
	if expectedRunID != "" && first.RunID != expectedRunID {
		return errors.New("workflow.status initial snapshot lost the correlated run")
	}
	order := cursorCompare(second.Cursor, first.Cursor)
	if order == 2 {
		return errors.New("workflow.status cursors are not comparable after reconnect")
	}
	if order < 0 {
		return errors.New("workflow.status cursor regressed after reconnect")
	}
	if snapshotActivityCursor(second) < snapshotActivityCursor(first) {
		return errors.New("activity_after regressed after reconnect")
	}
	if first.RunID != second.RunID || first.Profile != second.Profile || !slices.Equal(first.Permissions, second.Permissions) || first.Requested != second.Requested || first.Observed != second.Observed || first.StatusInvocationID == second.StatusInvocationID {
		return errors.New("workflow.status changed run profile permissions or protocol after reconnect")
	}
	if first.Accepted != second.Accepted || first.Total != second.Total || first.Progress != second.Progress || first.Bar != second.Bar || checklistDigest(first.Checklist) != checklistDigest(second.Checklist) || activityDigest(first.Activity, first.Notices, first.Effects, first.EffectIDs) != activityDigest(second.Activity, second.Notices, second.Effects, second.EffectIDs) {
		return errors.New("workflow.status changed checklist or progress after reconnect")
	}
	return nil
}

func snapshotActivityCursor(snapshot WorkflowSnapshot) int64 {
	if snapshot.ActivityCursor != 0 || snapshot.ActivityAfter == 0 {
		return snapshot.ActivityCursor
	}
	return snapshot.ActivityAfter
}

func cursorCompare(first, second string) int {
	if first == second {
		return 0
	}
	parse := func(value string) (int64, bool) {
		value = strings.TrimSpace(value)
		for i := len(value) - 1; i >= 0; i-- {
			if value[i] < '0' || value[i] > '9' {
				if i == len(value)-1 {
					return 0, false
				}
				value = value[i+1:]
				break
			}
		}
		n, err := strconv.ParseInt(value, 10, 64)
		return n, err == nil
	}
	a, aok := parse(first)
	b, bok := parse(second)
	if aok && bok {
		if a < b {
			return -1
		}
		return 1
	}
	return 2
}

func activityDigest(activity []ActivityRecord, notices []NoticeRecord, effects []string, effectIDs ...[]string) string {
	var b strings.Builder
	for _, item := range activity {
		b.WriteString("a:")
		b.WriteString(item.InvocationID)
		b.WriteByte(':')
	}
	for _, item := range notices {
		b.WriteString("n:")
		b.WriteString(item.InvocationID)
		b.WriteByte(':')
	}
	for _, item := range effects {
		b.WriteString("e:")
		b.WriteString(item)
		b.WriteByte(':')
	}
	if len(effectIDs) > 0 {
		for _, item := range effectIDs[0] {
			b.WriteString("i:")
			b.WriteString(item)
			b.WriteByte(':')
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}

func checklistDigest(items []ChecklistItem) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString(item.ID)
		b.WriteByte(':')
		b.WriteString(item.Title)
		b.WriteByte(':')
		b.WriteString(strconv.FormatBool(item.Accepted))
		b.WriteByte(':')
		b.WriteString(item.Evidence)
		b.WriteByte(';')
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}

func replaceEntry(matrix Matrix, replacement Entry) Matrix {
	for i := range matrix.Entries {
		if matrix.Entries[i].Client == replacement.Client {
			matrix.Entries[i] = replacement
			return matrix
		}
	}
	matrix.Entries = append(matrix.Entries, replacement)
	return matrix.Normalize()
}

// JSON is part of ATENEA's public orchestration contract.
func (r PilotReport) JSON() ([]byte, error) {
	reportCopy := r.Normalize()
	reportCopy.Matrix = reportCopy.Matrix.Normalize()
	return json.Marshal(reportCopy)
}

// MarshalJSON is part of ATENEA's public orchestration contract.
func (r PilotReport) MarshalJSON() ([]byte, error) {
	type plain PilotReport
	return json.Marshal(plain(r.Normalize()))
}

// Markdown is part of ATENEA's public orchestration contract.
func (r PilotReport) Markdown() string {
	r = r.Normalize()
	return fmt.Sprintf("# Piloto de compatibilidad\n\nRun `%s`, workflow `%s`, sentinel `%s`.\n\n%s", mdCell(r.RunID), mdCell(r.WorkflowID), r.Sentinel, r.Matrix.Markdown())
}

// Normalize is part of ATENEA's public orchestration contract.
func (r PilotReport) Normalize() PilotReport {
	r.RunID, r.WorkflowID = sanitize(r.RunID), sanitize(r.WorkflowID)
	r.Evidence = slices.Clone(r.Evidence)
	for i := range r.Evidence {
		r.Evidence[i].ID = sanitize(r.Evidence[i].ID)
		r.Evidence[i].Detail = sanitize(r.Evidence[i].Detail)
	}
	return r
}
