# iOS Simulator visual fallback certification

This directory defines the provider-real certification for the fixed priority
chain `official_computer_use -> atenea_desktop_visual -> agent_device`. It is a
protocol, not evidence that the run happened. Store each run in a dated
directory with one `result.json` conforming to `result.schema.json`; never copy
local unit-test numbers into a provider-real report.

## Fixed environment

- iPhone 14, iOS 16.4. If unavailable, mark certification pending; do not
  substitute another runtime.
- Fixtures `R34w-30` and `S34CG50`.
- Simulator visible, Fit Screen, not full-screen, normalized on the named
  monitor before each series.
- A new Codex task created after service restart and started explicitly with
  `@Computer` for the official backend.
- Atenea configured with
  `action_applications = ["com.apple.iphonesimulator"]`.

Record the Atenea commit, binary/helper versions, macOS/Xcode/Simulator
versions, display IDs/frames/scales and geometry generation before running.

## Matrix

Run every backend against both fixtures and each scenario below. Perform five
recorded warm-ups, excluded from aggregates, followed by exactly 30 measured
cycles per cell.

| Scenario | Setup |
|---|---|
| `centered` | Window centered on the intended display |
| `fitted` | Window fitted without full-screen |
| `moved` | Window moved to the other display |
| `spanning` | Window intersects both displays |
| `rescaled_capture` | Returned image is downscaled |
| `flutter_canvas_no_ax` | Flutter canvas exposes no useful accessibility nodes |
| `rotation` | Device rotates after observation; stale action must be refused |
| `resize` | Window resizes after observation; stale action must be refused |
| `human_interrupt` | Human input interrupts; chain stops without replay or fallback |

The reversible functional cycle is `Inicio -> Viajes -> Inicio`. A functional
cycle succeeds only when a post-action observation confirms the expected
screen. A safety scenario succeeds only when the expected refusal/stop is
observed and no later backend or duplicate action occurs.

## Required measurements

Each cycle records requested and actual backend, classification and cause,
observation, selection, action, verification and total milliseconds, whether
the action was sent, whether the destination was visually verified, fallback
count, unknown-action count, duplicate-action count and final evidence path.
Aggregate each cell with p50/p95 for every phase, errors, fallbacks and unknown
actions. Preserve raw evidence and compute tables from it.

Inject `unavailable`, `unsupported`, `recoverable_denied`, `unverified` and
`unknown_after_mutation` failures into the full chain. A terminal denial or
human interruption must stop immediately. For `unknown_after_mutation`, observe
before classification and never issue a second mutation. Confirm the Atenea
border, virtual cursor and miniature remain visible and that `agent-device` is
not invoked while either visual route works.

Validate a completed artifact with:

```sh
scripts/validate-ios-simulator-visual-result.sh path/to/result.json
```
