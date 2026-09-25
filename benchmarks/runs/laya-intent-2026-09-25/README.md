# Laya intent evaluation, 2026-09-25

`report.json` contains 48 per-case predictions and aggregate scores from the
curated `internal/decision/testdata/intent-evaluation.jsonl` corpus. The
requests were sent to a local Laya 0.3.20 service with the multilingual
checkpoint pinned. The checkpoint and corpus hashes are recorded in the
report. ATENEA generated both rules and gated Laya dry-run plans using a local
valid settings file; all 48 plan pairs compiled and had no effects outside
their explicit grants.

This is a frozen measurement of the rules baseline at the Laya integration
revision. Later decision-classifier changes do not rewrite this corpus or
report; issue #165 owns a separate representative evaluation of the updated
decision behavior. Treat these figures as historical evidence for this exact
corpus and revision, not as confirmation of later classifier changes.

The artifact omits request text, private settings, credentials, and full plan
details. `id` values map to corpus order, so the predictions can be checked
against the public fixture. `observe_duration_ms` includes model inference and
planning work. The report is model-real and local-plan evidence. It is not a
blind human plan review, a live traffic study, or a workflow execution result.

The earlier Python-only comparison used a different choice order and produced
different confidence-gate metrics. The runner now sends choices in the same
order as the Go adapter. The report here represents the current Go request
shape. Keep rules as the default until a larger representative held-out set
and independent blind plan reviews support a change.
