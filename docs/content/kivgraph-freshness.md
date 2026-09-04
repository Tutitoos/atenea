---
title: Kivgraph freshness during publication
weight: 6
---

# Kivgraph freshness during publication

Graph status and source freshness must describe the same generation. Older
Kivgraph builds can return a new snapshot ID with an inventory check of the
previous generation when publication overlaps the check.

Atenea retries only the status read, at most three times, when the two positive
generation IDs disagree. Cancellation stops the retries. A persistent mismatch
withholds results without starting another index. An aligned `stale` result is
not a publication race and continues through the existing authorization and
single-rebuild safeguards.

The additive `content_generation` field in `atenea_graph_evidence` records the
generation actually checked alongside `snapshot_id`. It contains no source
content or provider arguments. The receipt format and contract 4.0 remain
compatible.

If sources change during indexing, the user-facing failure names that cause and
asks the operator to pause edits before retrying. Raw child-process output is
not copied into that message. A failed post-publication verification names the
expected and served generations and still withholds graph results.

Local installation tests require a quiet window across every registered
repository, not only the repository named by the query. Installation, live
validation, publication and merge are separate delivery states.
