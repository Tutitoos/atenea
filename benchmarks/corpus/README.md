# Fixed acceptance corpus

`v1` is the provider-free baseline for the ATENEA improvement plan. Its
`manifest.json` defines exactly twelve stable scenarios and `schema.json`
documents the manifest shape. Fixtures are intentionally small and local so
they can be hashed, copied into tests, and compared without invoking a model,
network, MCP server, or managed process.

The provider-free run validates every fixture first, then reports all twelve
product capabilities as `unsupported` until their delivery supplies an
integration probe. A valid fixture is never presented as proof that ATENEA
answered the corresponding capability:

| IDs | Coverage |
|---|---|
| S01–S06 | Go, TypeScript, Dart context, workspace scope, and Go/TypeScript implementation markers |
| S07 | Dart semantic implementation coverage, measured separately |
| S08–S09 | MCP target declarations `2025-06-18` and `2026-07-28` |
| S10 | Markdown checklist and progress-bar representation |
| S11 | Provider failure injection, reserved for an integration run |
| S12 | Live client chat rendering, reserved for a host integration run |

Run it with:

```sh
go run ./cmd/atenea-benchmark --corpus-only --output /tmp/atenea-corpus
```

The command writes `corpus.json` and `corpus.md`. Evidence includes the
benchmark manifest (commit and environment), SHA-256 hashes for the manifest
and every referenced fixture, fixture-preflight state, product-capability
state, and passed/failed/unsupported counts. The code-owned catalog fixes the
scenario identity and allowed states; the manifest cannot turn a check into a
skip. The default command also requires the independently declared corpus hash
to match before executing any scenario.
