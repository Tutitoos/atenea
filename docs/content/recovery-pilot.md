---
title: Recovery pilot
weight: 7
---

# Recovery pilot

`atenea recovery pilot --fixture --root PATH --sentinel PATH --json` runs the
deterministic P10 harness. It requires a pre-existing sentinel outside the
disposable root, records every attempt before a possible retry, and reports
the requested and observed model, route, backend, budget, duration and retry
reason. Fixture evidence is never presented as a real provider result.

Automatic recovery is deliberately narrow. It retries at most `1 + MaxRetries`
attempts, only for `unavailable` or `timeout`, and reuses the exact route,
backend and model. Invalid input, permission, budget, unknown cost, writes,
external effects, operations and cancellation stop without a model fallback.
The pilot is read-only and does not start a real provider. Its fixture starts
the existing supervisor stdio seam and invokes the existing Kivgraph
`index --full --json` adapter against disposable fake processes. A zero index
count is rejected and never reported as success.

Kivgraph rebuilds remain full rebuilds and require explicit or standing
authorization. `recoverypilot.RebuildCoordinator` supplies the provider-neutral
lock: concurrent requests for one generation coalesce, a different generation
cannot write while it is active, and only a positive index count publishes the
generation. The fixture also proves unauthorized, duplicate/coalesced and
canceled index calls do not write. This is fixture evidence for the seams, not
certification of a live supervisor or Kivgraph installation. No real recovery
gate is run by the ordinary Go test suite.
