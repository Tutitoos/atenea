# Fixed acceptance corpus

`v2` is the current provider-free baseline. Its `manifest.json` defines exactly
nine stable scenarios and `schema.json` documents the manifest shape. Fixtures
are intentionally small and local so they can be hashed, copied into tests, and
compared without invoking a model, network, MCP server, or managed process.

`v2` replaces `v1` (#212, step 3). The workflow engine, the recovery pilot and
the knowledge store were removed, and with them the three scenarios only they
fed (S10 plan progress, S11 bounded recovery, S12 client chat visibility) and
the fixtures `progress.md`, `recovery.json` and `chat-visibility.json`. The
other nine scenarios and their fixtures are byte-identical to `v1`. The code
pins and verifies only `v2` (`CanonicalCorpusSHA256` in
`internal/benchmark/corpus`); `v1` is kept unchanged as historical evidence for
the dated reports under `benchmarks/runs/`, which quote its hash
`bb57b8555c44fdd754bc72855c7899adda318677b48ee5696820a710efec98a5`, and is no
longer loadable by the current code.

The provider-free run validates every fixture first, then reports all nine
product capabilities as `unsupported` until their delivery supplies an
integration probe. A valid fixture is never presented as proof that ATENEA
answered the corresponding capability:

| IDs | Coverage |
|---|---|
| S01–S06 | Go, TypeScript, Dart context, workspace scope, and Go/TypeScript implementation markers |
| S07 | Dart semantic implementation coverage, measured separately |
| S08–S09 | MCP target declarations `2025-06-18` and `2026-07-28` |

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
