# P13 `symbol.implementations`

Date: `2026-09-06`
Provider baseline: **Kivgraph** `e28323742c8f148a859dcc54045727629ab4ba8e` from upstream `main`.

| Atenea fact | State | Evidence |
|---|---|---|
| declared | proven | Native `find_implementations` adapter and contract |
| connected | unknown | No live provider/client connection in this run |
| functionally tested | proven | Local adapter contract tests |

Atenea consumes the native Kivgraph tool and derives `resolution=exact` only
after validating language-specific evidence. The provider wire has no
`resolution` field. Coverage retains `exact`, `candidate`,
`unresolved_related`, and `package_level`; candidate and unresolved evidence
never become locations.

Go accepts `GO_TYPES_USE/structural` and `GO_OBJECT_PATH/typed` with
`EXACT_TYPECHECKED`. TypeScript accepts its declared or structural provenance
with the exact confidences published by the pinned provider:
`EXACT_TYPECHECKED`, `EXACT_DECLARATION_MAPPED`, `EXACT_PACKAGE_MAPPED`, and
`STRUCTURAL_CERTAIN`.

Component tests observed Kivgraph generating Dart `IMPLEMENTS`/`OVERRIDES`
evidence, but the real CLI attempt reached `kivgraph index --full` and failed
with `LadybugDB native support is unavailable`; no generation was published.
Atenea keeps Dart `symbol.implementations` disabled until a real Atenea
provider/client E2E run. `symbol.unresolved` remains outside the advertised
capability set.
