package clientcompat

import (
	"context"
	"errors"
	"strings"
)

// NewFixtureClients returns deterministic, in-process surfaces for all five
// supported clients. It exercises the same argv/environment overlay contract
// as a real launch but never claims that a real client rendered the chat.
func NewFixtureClients() map[string]PilotClient {
	clients := make(map[string]PilotClient, len(Specs()))
	for _, spec := range Specs() {
		clients[spec.Client] = fixtureClient{}
	}
	return clients
}

// FixtureProvenance is part of ATENEA's public orchestration contract.
func FixtureProvenance() map[string]Provenance {
	provenance := make(map[string]Provenance, len(Specs()))
	for _, spec := range Specs() {
		provenance[spec.Client] = Provenance{Level: EvidenceFixture, Source: EvidenceFixture, Recorder: "fixture-recorder"}
	}
	return provenance
}

type fixtureClient struct{}

func (fixtureClient) Connect(_ context.Context, injection Injection) (PilotConnection, error) {
	if strings.TrimSpace(injection.RunID) == "" || strings.TrimSpace(injection.WorkflowID) == "" || injection.Environment["ATENEA_PILOT_RUN_ID"] != injection.RunID || injection.Environment["ATENEA_PILOT_WORKFLOW_ID"] != injection.WorkflowID {
		return nil, errors.New("fixture correlation injection is incomplete")
	}
	if injection.Environment["ATENEA_PILOT_CLIENT"] != injection.Client || injection.Environment["ATENEA_PILOT_PROFILE"] != injection.Profile || injection.Environment["ATENEA_PILOT_TRANSPORT"] != injection.Transport || injection.Environment["ATENEA_PILOT_OVERLAY"] != injection.Overlay {
		return nil, errors.New("fixture environment overlay is incomplete")
	}
	if (injection.Client == "codex" || injection.Client == "claude") && len(injection.Args) == 0 {
		return nil, errors.New("fixture argv overlay is missing")
	}
	spec, ok := Spec(injection.Client)
	if !ok || injection.ClientInfo != spec.ClientInfo || injection.OverlayMechanism != spec.OverlayMechanism {
		return nil, errors.New("fixture client identity or overlay contract is invalid")
	}
	return fixtureConnection{runID: injection.RunID, workflowID: injection.WorkflowID, profile: injection.Profile, transport: injection.Transport, clientInfo: injection.ClientInfo, client: injection.Client}, nil
}

type fixtureConnection struct {
	runID, workflowID, profile, transport string
	clientInfo, client                    string
	cursor                                string
}

func (c fixtureConnection) WorkflowStatus(context.Context, string) (WorkflowSnapshot, error) {
	cursor := c.cursor
	if cursor == "" {
		cursor = "1"
	}
	return fixtureSnapshot(c.runID, c.workflowID, c.profile, cursor), nil
}

func (c fixtureConnection) Reconnect(context.Context) (PilotConnection, error) {
	return fixtureConnection{runID: c.runID, workflowID: c.workflowID, profile: c.profile, transport: c.transport, clientInfo: c.clientInfo, client: c.client, cursor: "2"}, nil
}

func (c fixtureConnection) Transcript(context.Context) (Transcript, error) {
	session := "session-1"
	if c.cursor != "" {
		session = "session-2"
	}
	base := []TranscriptEvent{{Kind: "handshake", ID: "handshake-1", RunID: c.runID, ClientInfo: c.clientInfo, ProtocolVersion: "2026-07-28", Transport: c.transport, Profile: c.profile, Requested: "2026-07-28", Observed: "2026-07-28"}}
	if c.cursor != "" {
		base = append(base,
			TranscriptEvent{Kind: "request", ID: "status-2", Method: "workflow.status", RunID: c.runID, WorkflowID: c.workflowID, InvocationID: "status-2", ActivityAfter: 1},
			TranscriptEvent{Kind: "response", ID: "status-2-response", Method: "workflow.status", RunID: c.runID, WorkflowID: c.workflowID, InvocationID: "status-2", ActivityCursor: 2},
		)
		return Transcript{SessionID: session, Reconnect: true, Provenance: Provenance{Level: EvidenceFixture, Source: EvidenceFixture, Recorder: "fixture-recorder"}, Events: base}, nil
	}
	bar := expectedBar(50)
	base = append(base,
		TranscriptEvent{Kind: "notice", ID: "notice-1", RunID: c.runID, WorkflowID: c.workflowID, InvocationID: "invocation-1", Text: "> **ATENEA · tool** — busco evidencia."},
		TranscriptEvent{Kind: "request", ID: "call-1", Method: "tools/call", RunID: c.runID, WorkflowID: c.workflowID, InvocationID: "invocation-1"},
		TranscriptEvent{Kind: "response", ID: "call-1-response", Method: "tools/call", RunID: c.runID, WorkflowID: c.workflowID, InvocationID: "invocation-1"},
		TranscriptEvent{Kind: "activity", ID: "activity-1", RunID: c.runID, WorkflowID: c.workflowID, InvocationID: "invocation-1"},
		TranscriptEvent{Kind: "render", ID: "render-1", RunID: c.runID, WorkflowID: c.workflowID, InvocationID: "invocation-1", Source: "fixture", Text: "Checklist [x]\n**Progreso:** " + bar},
		TranscriptEvent{Kind: "request", ID: "status-1", Method: "workflow.status", RunID: c.runID, WorkflowID: c.workflowID, InvocationID: "status-1", ActivityAfter: 0},
		TranscriptEvent{Kind: "response", ID: "status-1-response", Method: "workflow.status", RunID: c.runID, WorkflowID: c.workflowID, InvocationID: "status-1", ActivityCursor: 1},
	)
	return Transcript{SessionID: session, Provenance: Provenance{Level: EvidenceFixture, Source: EvidenceFixture, Recorder: "fixture-recorder"}, Events: base}, nil
}

func (c fixtureConnection) Close() error { return nil }

func fixtureSnapshot(runID, workflowID, profile, cursor string) WorkflowSnapshot {
	_ = runID
	return WorkflowSnapshot{
		RunID: runID, WorkflowID: workflowID, Cursor: cursor, Profile: profile,
		Permissions: []string{"workflow.read", "activity.read"},
		Requested:   "2026-07-28", Observed: "2026-07-28",
		StatusInvocationID: "status-" + cursor,
		Checklist: []ChecklistItem{
			{ID: "P00", Title: "fixture", Accepted: true, Evidence: "fixture evidence"},
			{ID: "P01", Title: "workflow", Accepted: false, Evidence: "pending"},
		},
		Accepted: 1, Total: 2, Progress: 50, Bar: expectedBar(50), ActivityCursor: parseCursor(cursor),
		Activity:  []ActivityRecord{{InvocationID: "invocation-1", Kind: "tool", Text: "read-only"}},
		Notices:   []NoticeRecord{{InvocationID: "invocation-1", Markdown: "> **ATENEA · tool** — fixture."}},
		Effects:   []string{"read-only"},
		EffectIDs: []string{"effect-1"},
	}
}

func parseCursor(cursor string) int64 {
	if cursor == "2" {
		return 2
	}
	return 1
}
