package clientcompat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMatrixIsVersionedDeterministicAndKeepsStatesIndependent(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	matrix := NewMatrix(now)
	if matrix.SchemaVersion != SchemaVersion || len(matrix.Entries) != 5 {
		t.Fatalf("matrix = %#v", matrix)
	}
	first, err := matrix.JSON()
	if err != nil {
		t.Fatal(err)
	}
	second, err := matrix.JSON()
	if err != nil || !slices.Equal(first, second) {
		t.Fatalf("JSON is not deterministic: %s / %s", first, second)
	}
	entry, ok := matrix.Entry("codex")
	if !ok || entry.Declared.State != Unknown || entry.Connected.State != Unknown {
		t.Fatalf("initial codex states = %#v", entry)
	}
	entry.Declared = EvidenceCheck(Pass, Evidence{ID: "binary", Detail: "/Users/gtrave/.codex/config.toml token=secret"})
	if entry.Connected.State != Unknown {
		t.Fatal("declared detection upgraded connected state")
	}
	encoded, _ := (Matrix{Entries: []Entry{entry}}).JSON()
	if strings.Contains(string(encoded), "/Users/") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("matrix leaked path or secret: %s", encoded)
	}
	if !strings.Contains(matrix.Markdown(), "| codex |") {
		t.Fatal("markdown omitted codex")
	}
}

type fakeClient struct{ root string }

func (f fakeClient) Connect(_ context.Context, injection Injection) (PilotConnection, error) {
	if injection.RunID == "" || injection.WorkflowID == "" || injection.Environment["HOME"] == "" {
		return nil, os.ErrInvalid
	}
	if !strings.HasPrefix(injection.Environment["HOME"], f.root) {
		return nil, os.ErrPermission
	}
	spec, _ := Spec(injection.Client)
	return fakeConnection{runID: injection.RunID, workflowID: injection.WorkflowID, clientInfo: spec.ClientInfo, transport: injection.Transport, snapshot: WorkflowSnapshot{
		RunID: injection.RunID, WorkflowID: injection.WorkflowID, Cursor: "cursor-" + injection.RunID,
		Profile: injection.Profile, Permissions: []string{"workflow.read"}, Requested: "2026-07-28", Observed: "2026-07-28",
		StatusInvocationID: "status-1",
		Checklist:          []ChecklistItem{{ID: "P00", Title: "base", Accepted: true, Evidence: "fixture"}, {ID: "P01", Title: "test", Accepted: false, Evidence: "pending"}},
		Accepted:           1, Total: 2, Progress: 50, Bar: expectedBar(50), ActivityCursor: 1,
		Activity:  []ActivityRecord{{InvocationID: "inv-1", Kind: "tool", Text: "tool"}},
		Notices:   []NoticeRecord{{InvocationID: "inv-1", Markdown: "> **ATENEA · tool** — uso."}},
		Effects:   []string{"read-only"},
		EffectIDs: []string{"effect-1"},
	}}, nil
}

type fakeConnection struct {
	runID, workflowID, clientInfo, transport string
	snapshot                                 WorkflowSnapshot
}

func (f fakeConnection) WorkflowStatus(context.Context, string) (WorkflowSnapshot, error) {
	return f.snapshot, nil
}
func (f fakeConnection) Reconnect(context.Context) (PilotConnection, error) {
	f.snapshot.Cursor = "cursor-" + f.runID + "-2"
	f.snapshot.ActivityCursor = 2
	f.snapshot.StatusInvocationID = "status-2"
	return f, nil
}
func (f fakeConnection) Close() error { return nil }
func (f fakeConnection) Transcript(context.Context) (Transcript, error) {
	base := []TranscriptEvent{
		{Kind: "handshake", ID: "h-1", RunID: f.runID, ClientInfo: f.clientInfo, ProtocolVersion: "2026-07-28", Transport: f.transport, Profile: f.snapshot.Profile, Requested: f.snapshot.Requested, Observed: f.snapshot.Observed},
	}
	if f.snapshot.StatusInvocationID == "status-2" {
		base = append(base,
			TranscriptEvent{Kind: "request", ID: "s-2", Method: "workflow.status", RunID: f.runID, WorkflowID: f.snapshot.WorkflowID, InvocationID: "status-2", ActivityAfter: 1},
			TranscriptEvent{Kind: "response", ID: "s-2-response", Method: "workflow.status", RunID: f.runID, WorkflowID: f.snapshot.WorkflowID, InvocationID: "status-2", ActivityCursor: 2},
		)
		return Transcript{SessionID: "session-second", Reconnect: true, Provenance: Provenance{Level: EvidenceFixture, Source: EvidenceFixture, Recorder: "fixture-recorder"}, Events: base}, nil
	}
	base = append(base,
		TranscriptEvent{Kind: "notice", ID: "n-1", RunID: f.runID, WorkflowID: f.snapshot.WorkflowID, InvocationID: "inv-1", Text: "> **ATENEA · tool** — uso."},
		TranscriptEvent{Kind: "request", ID: "call-1", Method: "tools/call", RunID: f.runID, WorkflowID: f.snapshot.WorkflowID, InvocationID: "inv-1"},
		TranscriptEvent{Kind: "response", ID: "call-1-response", Method: "tools/call", RunID: f.runID, WorkflowID: f.snapshot.WorkflowID, InvocationID: "inv-1"},
		TranscriptEvent{Kind: "activity", ID: "a-1", RunID: f.runID, WorkflowID: f.snapshot.WorkflowID, InvocationID: "inv-1"},
		TranscriptEvent{Kind: "render", ID: "r-1", RunID: f.runID, WorkflowID: f.snapshot.WorkflowID, InvocationID: "inv-1", Source: "fixture", Text: "Checklist [x] Progreso " + expectedBar(50)},
		TranscriptEvent{Kind: "request", ID: "s-1", Method: "workflow.status", RunID: f.runID, WorkflowID: f.snapshot.WorkflowID, InvocationID: "status-1", ActivityAfter: 0},
		TranscriptEvent{Kind: "response", ID: "s-1-response", Method: "workflow.status", RunID: f.runID, WorkflowID: f.snapshot.WorkflowID, InvocationID: "status-1", ActivityCursor: 1},
	)
	return Transcript{SessionID: "session-first", Provenance: Provenance{Level: EvidenceFixture, Source: EvidenceFixture, Recorder: "fixture-recorder"}, Events: base}, nil
}

func TestPilotUsesIsolatedInjectionAndVerifiesReconnectAndSentinel(t *testing.T) {
	base := t.TempDir()
	sentinel := filepath.Join(base, "external-sentinel")
	if err := os.WriteFile(sentinel, []byte("pre-created\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sandbox, err := NewSandbox(filepath.Join(base, "pilot"), sentinel)
	if err != nil {
		t.Fatal(err)
	}
	clients := map[string]PilotClient{}
	for _, spec := range Specs() {
		if spec.Injection {
			clients[spec.Client] = fakeClient{root: sandbox.Root}
		}
	}
	report, err := RunPilot(t.Context(), PilotOptions{RunID: "run-001", WorkflowID: "wf-001", Clients: clients, Provenance: FixtureProvenance(), Sandbox: sandbox})
	if err != nil {
		t.Fatal(err)
	}
	if report.Sentinel != Pass || len(report.Entries()) != 5 {
		t.Fatalf("pilot report = %#v", report)
	}
	for _, client := range []string{"claude", "codex", "opencode"} {
		entry, _ := report.Matrix.Entry(client)
		if entry.Connected.State != Pass || entry.Tested.State != Pass || entry.Reconnect.State != Pass {
			t.Fatalf("%s = %#v", client, entry)
		}
	}
	for _, client := range []string{"chatgpt", "omp"} {
		entry, _ := report.Matrix.Entry(client)
		if entry.Connected.State != Unknown || entry.Reconnect.State != Unknown {
			t.Fatalf("unmeasured %s = %#v", client, entry)
		}
	}
}

func (p PilotReport) Entries() []Entry { return p.Matrix.Entries }

func TestPilotJSONContainsNoExternalPath(t *testing.T) {
	matrix := NewMatrix(time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC))
	report := PilotReport{RunID: "run|002\nunsafe", WorkflowID: "wf-002\nunsafe", Matrix: matrix, Sentinel: Pass, Evidence: []Evidence{{ID: "safe", Detail: "unchanged"}}}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), string(filepath.Separator)+"Users"+string(filepath.Separator)) {
		t.Fatalf("report leaked a path: %s", encoded)
	}
	if strings.Contains(string(encoded), "\nunsafe") || strings.Contains(string(encoded), "run|002\n") {
		t.Fatalf("report leaked unsanitized identifiers: %s", encoded)
	}
}

func TestFixturePilotCoversEverySurfaceWithoutCertifyingPresentation(t *testing.T) {
	base := t.TempDir()
	sentinel := filepath.Join(base, "external-sentinel")
	if err := os.WriteFile(sentinel, []byte("owner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sandbox, err := NewSandbox(filepath.Join(base, "pilot"), sentinel)
	if err != nil {
		t.Fatal(err)
	}
	report, err := RunPilot(t.Context(), PilotOptions{
		RunID: "run|fixture\nunsafe", WorkflowID: "workflow|fixture\nunsafe",
		Clients: NewFixtureClients(), Provenance: FixtureProvenance(), Sandbox: sandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Sentinel != Pass || len(sandbox.RootHashes) != 5 {
		t.Fatalf("sandbox/report = %#v / %#v", sandbox.RootHashes, report)
	}
	for _, spec := range Specs() {
		entry, ok := report.Matrix.Entry(spec.Client)
		if !ok {
			t.Fatalf("fixture entry %s = %#v", spec.Client, entry)
		}
		if !spec.Injection {
			if entry.Connected.State != Unknown || entry.Source != EvidenceUnknown {
				t.Fatalf("unsupported fixture entry %s = %#v", spec.Client, entry)
			}
			continue
		}
		if entry.Connected.State != Pass || entry.Tested.State != Pass || entry.Reconnect.State != Pass || entry.Presentation.State != Unknown || entry.Source != EvidenceFixture {
			t.Fatalf("fixture entry %s = %#v", spec.Client, entry)
		}
		if entry.Requested == "unknown" || entry.Observed == "unknown" {
			t.Fatalf("fixture protocol evidence missing for %s: %#v", spec.Client, entry)
		}
	}
	markdown := report.Markdown()
	if strings.Contains(markdown, "\nunsafe") || !strings.Contains(markdown, `run\|fixture unsafe`) {
		t.Fatalf("markdown did not sanitize identifiers: %q", markdown)
	}
}

func TestSandboxNeverCreatesAnExternalSentinel(t *testing.T) {
	base := t.TempDir()
	if _, err := NewSandbox(filepath.Join(base, "pilot"), filepath.Join(base, "missing")); err == nil {
		t.Fatal("sandbox created or accepted a missing external sentinel")
	}
}

func TestUnverifiedRealProvenanceCannotCertifyFixtureOrPresentation(t *testing.T) {
	base := t.TempDir()
	sentinel := filepath.Join(base, "sentinel")
	if err := os.WriteFile(sentinel, []byte("owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	sandbox, err := NewSandbox(filepath.Join(base, "pilot"), sentinel)
	if err != nil {
		t.Fatal(err)
	}
	provenance := map[string]Provenance{}
	for _, spec := range Specs() {
		provenance[spec.Client] = Provenance{Level: EvidenceReal, Source: EvidenceReal, Recorder: "grep"}
	}
	report, err := RunPilot(t.Context(), PilotOptions{RunID: "r", WorkflowID: "w", Clients: NewFixtureClients(), Provenance: provenance, Sandbox: sandbox})
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []string{"claude", "codex", "opencode"} {
		entry, _ := report.Matrix.Entry(client)
		if entry.Connected.State == Pass || entry.Presentation.State == Pass || entry.Source == EvidenceReal {
			t.Fatalf("unverified real claim accepted for %s: %#v", client, entry)
		}
	}
}

func TestBuildInjectionUsesTheWrapPlanContracts(t *testing.T) {
	base := t.TempDir()
	sentinel := filepath.Join(base, "sentinel")
	if err := os.WriteFile(sentinel, []byte("owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	sandbox, err := NewSandbox(filepath.Join(base, "pilot"), sentinel)
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []string{"codex", "claude", "opencode"} {
		spec, _ := Spec(client)
		injection, err := BuildInjection(spec, sandbox, "r", "w")
		if err != nil {
			t.Fatal(err)
		}
		switch client {
		case "codex":
			if len(injection.Args) < 2 || injection.Args[0] != "-c" || strings.Contains(injection.Args[1], "{}") {
				t.Fatalf("codex placeholder overlay: %#v", injection.Args)
			}
		case "claude":
			if len(injection.Args) != 2 || injection.Args[0] != "--mcp-config" || !strings.Contains(injection.Args[1], "mcpServers") {
				t.Fatalf("claude overlay: %#v", injection.Args)
			}
		case "opencode":
			if !strings.Contains(injection.Environment["OPENCODE_CONFIG_CONTENT"], `"atenea"`) {
				t.Fatalf("opencode overlay: %#v", injection.Environment)
			}
		}
	}
}

type claimingPresentationObserver struct{}

func (claimingPresentationObserver) ObservePresentation(context.Context, string, string) (PresentationObservation, error) {
	return PresentationObservation{Attested: true, Source: EvidenceReal, RunID: "r", WorkflowID: "w", InvocationID: "invocation-1", Sequence: 1, Text: "Checklist [x] Progreso"}, nil
}

func TestFixtureCannotUpgradePresentationToReal(t *testing.T) {
	check := observePresentation(t.Context(), claimingPresentationObserver{}, Provenance{Level: EvidenceFixture, Source: EvidenceFixture, Recorder: "fixture-recorder"}, "r", "w", "invocation-1")
	if check.State != Unknown {
		t.Fatalf("fixture presentation was certified: %#v", check)
	}
}

func TestTranscriptRequiresExactToolInvocationForNoticeAndActivity(t *testing.T) {
	spec, _ := Spec("codex")
	injection := Injection{Client: "codex", ClientInfo: spec.ClientInfo, Profile: spec.Profile, Transport: spec.Transport, RunID: "r", WorkflowID: "w"}
	snapshot := fixtureSnapshot("r", "w", spec.Profile, "1")
	for _, kind := range []string{"notice", "activity"} {
		t.Run(kind, func(t *testing.T) {
			connection := fixtureConnection{runID: "r", workflowID: "w", profile: spec.Profile, transport: spec.Transport, clientInfo: spec.ClientInfo, client: "codex"}
			transcript, err := connection.Transcript(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for i := range transcript.Events {
				if transcript.Events[i].Kind == kind {
					transcript.Events[i].InvocationID = "other"
					break
				}
			}
			if err := validateTranscript(transcript, injection, snapshot, transcript.Provenance); err == nil {
				t.Fatalf("accepted %s with an invocation unrelated to tools/call", kind)
			}
		})
	}
}

func TestReconnectAllowsAnUnchangedActivityCursorForAnEmptyPage(t *testing.T) {
	first := fixtureSnapshot("r", "w", "fixture", "1")
	second := fixtureSnapshot("r", "w", "fixture", "2")
	second.ActivityCursor = first.ActivityCursor
	if err := compareSnapshots(first, second, "w", "r"); err != nil {
		t.Fatalf("equal empty-page cursor rejected: %v", err)
	}
}

func TestEffectfulActivityRequiresUniqueEffectIDs(t *testing.T) {
	snapshot := fixtureSnapshot("r", "w", "fixture", "1")
	snapshot.Activity = []ActivityRecord{{InvocationID: "invocation-1", Kind: "write", Text: "mutate"}}
	snapshot.Effects = []string{"mutate"}
	snapshot.EffectIDs = nil
	if err := validateSnapshotForRun(snapshot, "w", "r"); err == nil {
		t.Fatal("accepted effectful activity without effect ids")
	}
}

func TestTranscriptRejectsStatusResponseBeforeRequest(t *testing.T) {
	spec, _ := Spec("codex")
	injection := Injection{Client: "codex", ClientInfo: spec.ClientInfo, Profile: spec.Profile, Transport: spec.Transport, RunID: "r", WorkflowID: "w"}
	snapshot := fixtureSnapshot("r", "w", spec.Profile, "1")
	connection := fixtureConnection{runID: "r", workflowID: "w", profile: spec.Profile, transport: spec.Transport, clientInfo: spec.ClientInfo, client: "codex"}
	transcript, err := connection.Transcript(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	request, response := -1, -1
	for i, event := range transcript.Events {
		if event.Method == "workflow.status" && event.Kind == "request" {
			request = i
		}
		if event.Method == "workflow.status" && event.Kind == "response" {
			response = i
		}
	}
	if request < 0 || response < 0 {
		t.Fatal("fixture transcript lacks status request/response")
	}
	transcript.Events[request], transcript.Events[response] = transcript.Events[response], transcript.Events[request]
	if err := validateTranscript(transcript, injection, snapshot, transcript.Provenance); err == nil {
		t.Fatal("accepted workflow.status response before request")
	}
}

func TestReconnectRejectsChecklistReordering(t *testing.T) {
	first := fixtureSnapshot("r", "w", "fixture", "1")
	second := fixtureSnapshot("r", "w", "fixture", "2")
	second.Checklist[0], second.Checklist[1] = second.Checklist[1], second.Checklist[0]
	if err := compareSnapshots(first, second, "w", "r"); err == nil {
		t.Fatal("accepted a reordered checklist after reconnect")
	}
}
