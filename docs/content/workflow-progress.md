---
title: "Workflow progress and telemetry"
weight: 37
---

# Workflow progress and telemetry

Each workflow keeps its plan revision, ordered points, acceptance evidence and
publication cursor in the workflow SQLite store. Atenea saves a state change
before publishing the complete Markdown checklist. Reconnection replays only
notices whose durable acknowledgement is missing.

```text
atenea workflow status WORKFLOW_ID --format compact
atenea workflow status WORKFLOW_ID --format markdown
atenea workflow export WORKFLOW_ID --format json
atenea workflow compare BASELINE_ID CANDIDATE_ID
atenea workflow panel WORKFLOW_ID
atenea agents scorecard
```

The progress bar always has 20 segments. Its integer percentage is
`accepted / current × 100`, truncated. Pending, running, blocked and review
states remain unchecked. Retired points remain visible but do not count in the
denominator. Acceptance requires complete implementation evidence and all
configured review stages.

Agent and tool receipts use workflow, agent run, invocation, thread, turn and
usage revision identities. Replayed receipts are idempotent; reusing an
identity with different contents is rejected. Provider token deltas are stored
only when observed. Missing usage is `unknown`, partial usage is `partial`, and
estimates remain separate from measured values. Internal tool notices count as
tool uses after the parent turn closes; their duration stays unknown when the
client exposes no completion event.

The read-only dashboard deep link `/workflows/WORKFLOW_ID` shows the persisted
checklist and expandable sections for agents, models, effort, tools, duration,
tokens, evidence and published notices. The first activity page is bounded to
200 rows and says when more data remains after its cursor.
`workflow panel` verifies that the workflow exists in the same default store
served by the dashboard and that the dashboard is enabled before printing or
opening its URL. Event-stream updates invalidate that workflow snapshot by
its durable run id.

Telemetry older than 90 days is pruned opportunistically when a new receipt is
written. Checklist and evidence remain part of the workflow record.
