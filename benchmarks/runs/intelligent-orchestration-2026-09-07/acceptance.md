# ATENEA product acceptance

Commit: `9ab1c03e9dca5e2167f582d9f860aae14e23424d`
Source digest: `4eb862618ef67308cd794f0bd110b48e0bda2386cbbbf6047027b33f318f2f46` (198 changed/untracked entries)
Corpus: `bb57b8555c44fdd754bc72855c7899adda318677b48ee5696820a710efec98a5`
Source: **DIRTY**

| Gate | Result | State | Scope | Scenarios | Duration |
|---|---|---|---|---|---:|
| Repository, workspace and language context | PASS | measured | local | S01, S02, S03, S04, S05, S06, S07 | 12920 ms |
| Legacy and modern MCP contracts | PASS | measured | local | S08, S09 | 22886 ms |
| Bounded recovery and persistent workflows | PASS | measured | local | S11 | 90969 ms |
| Outcome-backed provider quality | PASS | measured | local | S01, S02, S05, S06 | 7677 ms |
| Checklist, telemetry and dashboard API | PASS | measured | local | S10, S12 | 94561 ms |

## Efficiency comparison

| Metric | Before | After | State | Scope | Comparable | Non-regression |
|---|---:|---:|---|---|---|---|
| code_context_provider_calls | 22 | 2 | partial | local | false | unknown |
| fixture_read_calls | 2400 | 1800 | measured | local | true | pass |
| fixture_access_latency | 37669875 | 19375 | measured | local | true | pass |
| human_interventions | 0 | 0 | measured | local | true | pass |
| tokens | unknown | unknown | unknown | pending | false | unknown |
| provider_cost | unknown | unknown | unknown | pending | false | unknown |
| real_client_tracking_overhead | unknown | unknown | unknown | pending | false | unknown |

## Scenario evidence

- **S01**: preflight `passed`, state `measured`, scope `local`, fixture `7f49bfc3cc5fd2b266778370a8ff7f4b40a744f4b0f4af1572729127ac204691`; {"Time":"2026-09-07T04:21:58.79449+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph"}
{"Time":"2026-09-07T04:21:59.751219+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestContextUsesIntentIdentityAndBoundsCallsForTwentyRows"}
{"Time":"2026-09-07T04:21:59.751329+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestContextUsesIntentIdentityAndBoundsCallsForTwent…
- **S02**: preflight `passed`, state `measured`, scope `local`, fixture `6c00f0a1a109aa4af02b53a62f393e5bf8767b2741ba957087809de9d5c4dbc2`; {"Time":"2026-09-07T04:22:01.694792+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph"}
{"Time":"2026-09-07T04:22:02.54672+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestContextUsesIntentIdentityAndBoundsCallsForTwentyRows"}
{"Time":"2026-09-07T04:22:02.546813+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestContextUsesIntentIdentityAndBoundsCallsForTwent…
- **S03**: preflight `passed`, state `measured`, scope `local`, fixture `2bef7d2310868148688987f02780bb765ca44b79ae3b74b20464b02142783b56`; {"Time":"2026-09-07T04:22:03.887004+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/dartcoverage"}
{"Time":"2026-09-07T04:22:04.339466+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/dartcoverage","Test":"TestRunRevalidatesS03S07WithoutChangingCorpus"}
{"Time":"2026-09-07T04:22:04.339775+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/dartcoverage","Test":"TestRunRevalidatesS03S07WithoutChangingCorpus","Output":"=== RUN   TestRu…
- **S04**: preflight `passed`, state `measured`, scope `local`, fixture `3ad990dd854a9b6a0ab23f71162e622031f03457fe0b4cae490b593b72383bdf`; {"Time":"2026-09-07T04:22:05.601799+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/workspacecontext"}
{"Time":"2026-09-07T04:22:06.020698+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/workspacecontext","Test":"TestRunPreservesInputOrderAndSeparatesRepositories"}
{"Time":"2026-09-07T04:22:06.02087+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/workspacecontext","Test":"TestRunPreservesInputOrderAndSeparatesRepositories","Outp…
- **S05**: preflight `passed`, state `measured`, scope `local`, fixture `7f49bfc3cc5fd2b266778370a8ff7f4b40a744f4b0f4af1572729127ac204691`; {"Time":"2026-09-07T04:22:07.976389+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph"}
{"Time":"2026-09-07T04:22:08.825492+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestImplementationsP13AcceptsGoAndTypeScriptExactRelations"}
{"Time":"2026-09-07T04:22:08.825616+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestImplementationsP13AcceptsGoAndTypeScriptExac…
- **S06**: preflight `passed`, state `measured`, scope `local`, fixture `6c00f0a1a109aa4af02b53a62f393e5bf8767b2741ba957087809de9d5c4dbc2`; {"Time":"2026-09-07T04:22:10.886017+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph"}
{"Time":"2026-09-07T04:22:11.739865+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestImplementationsP13AcceptsGoAndTypeScriptExactRelations"}
{"Time":"2026-09-07T04:22:11.73995+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestImplementationsP13AcceptsGoAndTypeScriptExact…
- **S07**: preflight `unsupported`, state `measured`, scope `local`, fixture `2bef7d2310868148688987f02780bb765ca44b79ae3b74b20464b02142783b56`; {"Time":"2026-09-07T04:22:13.3817+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/dartcoverage"}
{"Time":"2026-09-07T04:22:13.714945+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/dartcoverage","Test":"TestDartImplementationPreflightDoesNotDispatch"}
{"Time":"2026-09-07T04:22:13.71504+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/dartcoverage","Test":"TestDartImplementationPreflightDoesNotDispatch","Output":"=== RUN   TestDar…
- **S08**: preflight `passed`, state `measured`, scope `local`, fixture `2cde451169c13f5e140ba8f2753518cb929833ce14b2858c737fd534b7d8e4ab`; {"Time":"2026-09-07T04:22:14.962766+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/mcpcompat"}
{"Time":"2026-09-07T04:22:15.369998+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/mcpcompat","Test":"TestParseAcceptsOnlyExactSupportedVersions"}
{"Time":"2026-09-07T04:22:15.370108+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/mcpcompat","Test":"TestParseAcceptsOnlyExactSupportedVersions","Output":"=== RUN   TestParseAcceptsOnlyE…
- **S09**: preflight `passed`, state `measured`, scope `local`, fixture `fa0af92d970afd698247a89af7095345c54aac7f13a41c4efad8a96b01519087`; {"Time":"2026-09-07T04:22:16.601736+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/mcpcompat"}
{"Time":"2026-09-07T04:22:16.893741+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/mcpcompat","Test":"TestParseAcceptsOnlyExactSupportedVersions"}
{"Time":"2026-09-07T04:22:16.893865+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/mcpcompat","Test":"TestParseAcceptsOnlyExactSupportedVersions","Output":"=== RUN   TestParseAcceptsOnlyE…
- **S11**: preflight `unsupported`, state `measured`, scope `local`, fixture `0620ab45955587c5461d2c67d905e0ca129e11e24cad5aaf97d79faa42686fd6`; {"Time":"2026-09-07T04:22:22.318007+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/recoverypilot"}
{"Time":"2026-09-07T04:22:23.420974+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/recoverypilot","Test":"TestRunFixtureKivgraphScenarioUsesProductionIndexBoundary"}
{"Time":"2026-09-07T04:22:23.421052+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/recoverypilot","Test":"TestRunFixtureKivgraphScenarioUsesProductionIndexBoundary"…
- **S01**: preflight `passed`, state `measured`, scope `local`, fixture `7f49bfc3cc5fd2b266778370a8ff7f4b40a744f4b0f4af1572729127ac204691`; {"Time":"2026-09-07T04:21:58.79449+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph"}
{"Time":"2026-09-07T04:21:59.751219+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestContextUsesIntentIdentityAndBoundsCallsForTwentyRows"}
{"Time":"2026-09-07T04:21:59.751329+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestContextUsesIntentIdentityAndBoundsCallsForTwent…
- **S02**: preflight `passed`, state `measured`, scope `local`, fixture `6c00f0a1a109aa4af02b53a62f393e5bf8767b2741ba957087809de9d5c4dbc2`; {"Time":"2026-09-07T04:22:01.694792+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph"}
{"Time":"2026-09-07T04:22:02.54672+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestContextUsesIntentIdentityAndBoundsCallsForTwentyRows"}
{"Time":"2026-09-07T04:22:02.546813+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestContextUsesIntentIdentityAndBoundsCallsForTwent…
- **S05**: preflight `passed`, state `measured`, scope `local`, fixture `7f49bfc3cc5fd2b266778370a8ff7f4b40a744f4b0f4af1572729127ac204691`; {"Time":"2026-09-07T04:22:07.976389+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph"}
{"Time":"2026-09-07T04:22:08.825492+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestImplementationsP13AcceptsGoAndTypeScriptExactRelations"}
{"Time":"2026-09-07T04:22:08.825616+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestImplementationsP13AcceptsGoAndTypeScriptExac…
- **S06**: preflight `passed`, state `measured`, scope `local`, fixture `6c00f0a1a109aa4af02b53a62f393e5bf8767b2741ba957087809de9d5c4dbc2`; {"Time":"2026-09-07T04:22:10.886017+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph"}
{"Time":"2026-09-07T04:22:11.739865+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestImplementationsP13AcceptsGoAndTypeScriptExactRelations"}
{"Time":"2026-09-07T04:22:11.73995+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/adapter/kivgraph","Test":"TestImplementationsP13AcceptsGoAndTypeScriptExact…
- **S10**: preflight `passed`, state `measured`, scope `local`, fixture `3bfd14670146a6020f3fcd2713bd0abd7c8b1669b6266d53e61ec19fa29e3995`; {"Time":"2026-09-07T04:22:18.995634+02:00","Action":"start","Package":"github.com/Tutitoos/atenea/internal/workflow"}
{"Time":"2026-09-07T04:22:20.08087+02:00","Action":"run","Package":"github.com/Tutitoos/atenea/internal/workflow","Test":"TestPlanProgressUsesTwentySegmentsAndTruncatedPercent"}
{"Time":"2026-09-07T04:22:20.080973+02:00","Action":"output","Package":"github.com/Tutitoos/atenea/internal/workflow","Test":"TestPlanProgressUsesTwentySegmentsAndTruncatedPercent","Output":"=== RUN   Tes…
- **S12**: preflight `unsupported`, state `unknown`, scope `pending`, fixture `5d59a50bae278b693b0525162ef29ea76b443dc20118e4196fbb5d90f7a20581`; local API, SSE and formatting paths passed; real client rendering pending

Limits:
- Provider-real evidence remains pending unless a gate says real_provider.
- Live client rendering remains pending unless a gate says real_client.
- Unknown metrics stay unknown and are never converted to zero.
- No improvement percentage is claimed.
