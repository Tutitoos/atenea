# iOS Simulator Computer Use reproducibility report

Status: pending provider-real execution

## Revisions and environment

| Item | Value |
|---|---|
| Atenea commit | pending |
| Atenea version | pending |
| Desktop helper version | pending |
| Codex version | pending |
| Official Computer Use server/skill | pending |
| `node_repl` / `@oai/sky` preflight | pending |
| macOS / Xcode / Simulator | pending |
| Device / runtime | iPhone 14 / iOS 16.4 required |
| Display geometry and scales | pending |
| Geometry generation | pending |

## Aggregate results

Populate this table from the validated `result.json`; do not hand-copy timing
samples. Add one row per backend, fixture and scenario.

| Requested backend | Actual backend | Fixture | Scenario | Cycles | Verified | Errors | Fallbacks | Unknown actions | Observation p50/p95 ms | Selection p50/p95 ms | Action p50/p95 ms | Verification p50/p95 ms | Total p50/p95 ms |
|---|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| pending | pending | pending | pending | 30 | pending | pending | pending | pending | pending | pending | pending | pending | pending |

## Failure injection and safety evidence

| Injection | Classification | Mutation sent? | Subsequent observation | Fallback invoked? | Duplicate action? | Evidence |
|---|---|---:|---|---:|---:|---|
| Official backend absent | `unavailable` | pending | pending | pending | no required | pending |
| Unsupported surface | `unsupported` | pending | pending | pending | no required | pending |
| Permission/window/geometry refusal | `recoverable_denied` | no required | pending | pending | no required | pending |
| Verification misses destination | `unverified` | yes | pending | pending | no required | pending |
| Ambiguous result after mutation | `unknown_after_mutation` | yes/unknown | required before classification | no required | no required | pending |
| Human interruption | `human_interrupted` | possible partial | terminal stop | no required | no required | pending |

## Visual and routing assertions

- [ ] Official Computer Use completes the reversible flow when available.
- [ ] Atenea completes it after a classified official-backend failure.
- [ ] `agent-device` appears only after both visual routes fail before mutation.
- [ ] Simulator is the only actionable macOS application.
- [ ] Atenea border, virtual cursor and miniature remain visible.
- [ ] No unknown post-mutation result produces a second action.
- [ ] `windowNotFoundAtPosition` is attributed to the official backend.

## Errors and attachments

List sanitized error text, raw result path, screenshots/video references if
authorized, and any environmental limitation. Do not include credentials,
personal data, exported TaxiPrime data or persistent application changes.

This report is prepared for local review only. Sending it to OpenAI requires
separate authorization.
