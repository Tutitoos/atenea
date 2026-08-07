---
title: Open questions
weight: 7
---

# Open questions

Not gaps against the design — the design never asked for these. Noticed
using Atenea day to day, not yet decided, written down as they came up so the
next session starts from this instead of memory. An entry leaves this list
by being answered, not by quietly falling off it.

- **Scope violations never touch health.** `readAnswer` counts out-of-scope
  hits into a per-call Notice (`"N match(es) fell outside the requested
  scope and were dropped"`) the same way it already reports them, but
  nothing feeds that count into the funnel's health or score the way
  repeated `permission_denied` failures already do. A provider that wanders
  out of scope on every call pays no cost today and keeps getting picked.
  Undecided: does a scope violation deserve the same fault-streak treatment
  as a hard failure, or is a Notice — read by whoever asked, never scored by
  the funnel — the correct permanent shape for this?
