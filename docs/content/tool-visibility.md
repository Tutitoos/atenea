---
title: Provider routing and usage in chat
weight: 6
---

Atenea's MCP connection instructions ask the assistant to announce each capability
or raw-tool call in the conversation's language, naming the advertised tool,
target and purpose. Parallel calls may share a message with one line per call.
Repeated calls still get an announcement.

`_atenea_prefer` accepts either an exact implementation (`kivgraph.overview`)
or a provider (`kivgraph`). Exact IDs take precedence over provider names.
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

Kivgraph's intent lookup is `symbol.intent_search`. Its `code.context`
implementation composes intent retrieval, symbol details and optional source.
Read each advertised schema rather than substituting another tool's arguments.

Structural questions prefer the graph: names use `symbol.search`, outgoing
reach uses `symbol.dependencies`, incoming impact uses `symbol.impact`, and
cross-repository consumers use `symbol.consumers`. Literal text remains
`code.search`. `symbol.source` retrieves up to twenty declarations; it preserves
the provider's source reanchoring and unavailable notices.

Graph reads require a content inventory matching the generation. Set
`orchestrator.kivgraph.auto_reindex_registered = true` only with explicit
standing authorization to rebuild registered repositories. The default is false.
`graph.ensure_fresh` is explicit maintenance, requiring read/write/process.
Neither path registers projects or grants general writes. Concurrent operations
share a process-safe lane. A failed automatic attempt requires explicit
maintenance before retrying that generation. Waiting is bounded by
`index_timeout` (30 minutes by default), and cancellation stops the owned child.
Unverified, stale or changing results are withheld.

Client timeouts are an independent ceiling: for authorized long rebuilds, set
the applicable `desktop_profile.tool_timeout` above `index_timeout` (for
example 31m versus 30m), and align the client's own MCP deadline. Codex uses
`mcp_servers.atenea.tool_timeout_sec`; generated Codex wrappers preserve the
profile timeout. This does not grant additional effects. Other clients may
still impose their own deadline. Explicit maintenance can retry failed repairs;
successful maintenance reopens runtime-failed graph queries for a new probe.

The `atenea_graph_evidence` receipt preserves observed generation, freshness,
coverage, completeness and pagination without forwarding upstream instructions.
An absent completeness verdict means unknown, not COMPLETE. LOWER_BOUND and
truncation never prove absence. Source bytes do not establish graph edges.

After a capability reaches the orchestrator, Atenea appends an `atenea_usage`
text receipt with the capability, repository, requested preference, selected
provider/implementation, dispatch flag, fallback flag, verdict, failure category
and selection exclusions. Selection without dispatch is explicitly `invoked=false`;
it must never be reported as provider usage. Failed and canceled runs also report
what actually dispatched. Pre-orchestrator refusals remain ordinary diagnostics.
The `atenea_kivgraph_usage` key is retained in the same text block as a compatibility
alias only when Kivgraph dispatched: clients should announce the invocation once.
Original structured results and schemas are unchanged. Receipts contain no copies
of payloads, source code, health-error text or credentials; exclusions use safe
categories such as `repository_scope`, `not_attached`, `constraints` and `health`.

This is adapter-level visibility, not a trace of every internal Kivgraph MCP
request. Raw tools already identify their backend in the advertised name and
retain their passthrough result and the preamble rule. Local shell/file reads
outside Atenea are not recorded and must not be attributed to a graph provider.
Atenea owns these instructions rather than
forwarding arbitrary upstream prose about tools absent from its catalog.

Restart the Atenea service after installing a new binary, then reconnect the
client MCP session to receive the updated initialization instructions. No
duplicate Kivgraph connection or client skill is necessary. Chat rendering is
best effort: the client/model must follow the instructions; protocol tests prove
delivery and receipt generation, not that every third-party UI displays a notice.
