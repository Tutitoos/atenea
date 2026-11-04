---
title: Provider routing and usage in chat
weight: 6
---

Atenea's MCP connection instructions ask the assistant to announce each capability
or raw-tool call in the conversation's language, naming the advertised tool,
target and purpose. Parallel calls may share a message with one line per call.
Repeated calls still get an announcement.

Over MCP, `workspace.context` sends one `notifications/message` activity line
before it dispatches. The workflow engine (`atenea workflow`) and the decision and
workflow tools that once carried durable activity cursors (`workflow.*`, `decision.plan`) are no longer part of Atenea
(see #212).

The activity line is an intent record. Provider selection and results remain in
the later usage receipt. MCP initialization instructs the client to place direct
tool preambles in the main chat before calling, but the protocol cannot force a
third-party client to render intermediate text; those clients remain partially
compatible until their real UI is validated.

`_atenea_prefer` accepts either an exact implementation (`tokensave.overview`)
or a provider (`tokensave`). Exact IDs take precedence over provider names.
Provider preferences rank only that provider's surviving implementations of
the requested capability. Unknown or incompatible preferences fail without
dispatch; valid but unavailable preferences can fall back with a routing receipt.
A notice describes intent, not confirmed usage, and never authorizes writes.

`catalog.repositories` reports capabilities and implementations per repository,
with availability and exclusion categories. This is a snapshot of declared scope,
constraints and known health, not a fresh probe or a grant of permissions.
Real dispatch and diagnostic selection share the same repository-reach filter:
a Tokensave rooted at Atenea cannot be selected for TaxiPrime. No wider Tokensave
root or additional indexing is required to enforce that boundary.

`code.context` answers from a task in prose, `code.search` is literal or regex
text, and `symbol.overview` and `symbol.calls` answer once a file or a symbol is
known. Read each advertised schema rather than substituting another tool's
arguments. The symbol and graph capabilities that only Kivgraph served are no
longer part of Atenea (see #208).

When a provider reports query evidence, the `atenea_graph_evidence` receipt
preserves it without forwarding upstream instructions. An absent completeness
verdict means unknown, not COMPLETE. LOWER_BOUND and truncation never prove
absence. Source bytes do not establish graph edges.

After a capability reaches the orchestrator, Atenea appends an `atenea_usage`
text receipt with the capability, repository, requested preference, selected
provider/implementation, dispatch flag, fallback flag, verdict, failure category
and selection exclusions. Selection without dispatch is explicitly `invoked=false`;
it must never be reported as provider usage. Failed and canceled runs also report
what actually dispatched. Pre-orchestrator refusals remain ordinary diagnostics.
Original structured results and schemas are unchanged. Receipts contain no copies
of payloads, source code, health-error text or credentials; exclusions use safe
categories such as `repository_scope`, `not_attached`, `constraints` and `health`.

One public capability invocation remains one notice even when its adapter makes
several private MCP requests. Local reads outside an Atenea dispatch
are not attributed to a provider. Atenea owns these instructions rather than
forwarding arbitrary upstream prose about tools absent from its catalog.

Restart the Atenea service after installing a new binary, then reconnect the
client MCP session to receive the updated initialization instructions. Chat rendering is
best effort: the client/model must follow the instructions; protocol tests prove
delivery and receipt generation, not that every third-party UI displays a notice.
