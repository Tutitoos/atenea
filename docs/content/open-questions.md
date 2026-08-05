---
title: Open questions
weight: 7
---

# Open questions

Not gaps against the design — the design never asked for these. Noticed
using Atenea day to day, not yet decided, written down on 2026-08-05 so the
next session starts from this instead of memory. An entry leaves this list
by being answered, not by quietly falling off it.

- **`claude.search`'s `scope` input is advisory, not enforced.** `omp` makes
  scope real by construction — `targets()` refuses any path that leaves the
  repository before ripgrep ever runs. `claude-code` only ever writes scope
  into the prompt (`"Only under these paths: ..."`) and never checks a
  returned match against it afterward, unlike the sensitive-path list, which
  the same adapter filters twice on purpose ("belt and braces", its own
  words). Undecided: does `scope` need the same double-check sensitive paths
  already get, or is advisory-only the honest limit of what an agentic far
  side can promise?

- **`codebase-memory.search` is declared with no adapter behind it.** Already
  tracked in [What is not built yet]({{< relref "not-built-yet" >}}) as the
  one of `code.search`'s four catalogue entries nothing explains — `no
  runner` on every status screen since it shipped. Undecided: build the
  adapter, or delete the entry.

- **No capability answers "what does this file contain."** `symbol.definition`,
  `.references` and `.implementations` all need a name or a position handed
  in first; nothing lists a file's own symbols the way an editor's outline
  pane does. Wanted this while dogfooding ordinary day-to-day use — it does
  not belong to any of the four capability families the design already has,
  so it has no natural id yet either.
