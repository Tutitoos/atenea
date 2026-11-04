# Fixed acceptance corpus

`v3` is the current provider-free baseline. Its `manifest.json` defines exactly
three stable scenarios and `schema.json` documents the manifest shape. Fixtures
are intentionally small and local so they can be hashed, copied into tests, and
compared without invoking a model, network, MCP server, or managed process.

`v3` replaces `v2` (#208). The Kivgraph provider was removed, and with it the
six scenarios only it and the Dart coverage matrix probed: S01 and S02 (Go and
TypeScript context), S03 (Dart context), S05 and S06 (Go and TypeScript
implementation relations) and S07 (Dart implementation coverage). The three
that remain, S04, S08 and S09, keep their ids, titles and fixtures
byte-identical to `v2`. The Go, TypeScript and Dart files under `v3/fixtures`
exist only because `fixtures/workspace.json` names them as the file scopes of
its three repositories and the S04 check requires them; no scenario probes
them. The code pins and verifies only `v3` (`CanonicalCorpusSHA256` in
`internal/benchmark/corpus`). `v1` and `v2` are kept unchanged as historical
evidence for the dated reports under `benchmarks/runs/`, which quote their
hashes (`v1` `bb57b8555c44fdd754bc72855c7899adda318677b48ee5696820a710efec98a5`,
`v2` `d642cd100829353a9483dc1c1824c55798aafae633ce0c43a771f5f05cd26490`), and
neither is loadable by the current code. `v2` itself replaced `v1` when the
workflow engine was removed (#212, step 3).

The provider-free run validates every fixture first, then reports all three
product capabilities as `unsupported` until their delivery supplies an
integration probe. A valid fixture is never presented as proof that ATENEA
answered the corresponding capability:

| IDs | Coverage |
|---|---|
| S04 | Workspace scope: two or more repositories keep their file identities explicit |
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
