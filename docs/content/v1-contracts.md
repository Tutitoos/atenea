---
title: v1 contracts
weight: 8
---

# v1 contracts

Fases 10 y 11 convierten las preguntas de diseño restantes en contratos
explícitos. Una capacidad está completa cuando su comportamiento tiene código y
tests; una función que necesita otro provider o una decisión interactiva queda
registrada como límite deliberado, no implícita en la configuración. La matriz
operativa completa está en [v1.0 policy](v1-policy.md).

## Structural search

`symbol.search` is the language-aware counterpart to `code.search`:

- `code.search` remains literal or regex text search and keeps its existing
  output contract;
- `symbol.search` asks Serena's indexed `find_symbol` surface for declarations;
- results include qualified `name`, provider `kind`, relative `path`,
  `line`, `end_line` and deterministic `rank`;
- `scope`, `kind` and `limit` are applied by Atenea after the provider answer;
- sensitive paths are removed before the result crosses the adapter boundary;
- `serena.search` is not an alias for text search; the shipped implementation
  is `serena.symbol_search`.

This keeps structural ranking from changing the meaning of the established
text-search capability.

## Security and permissions

The v1 behavior is intentionally non-interactive at the adapter and daemon
layers. Effects are granted before dispatch, and an effect outside the grant is
refused. The CLI's `--allow` is the explicit escalation mechanism. Adding a
prompt would require a separate UI/session contract and is therefore not
silently introduced into a process that may be running unattended.

## Provider boundaries

OpenCode is supported as a client/wrapper surface. It is not registered as a
local-model provider because that requires a stable invocation, cancellation,
usage and result contract that is independent of OpenCode's client protocol.

Agent `limits.max_tokens` is carried, validated and used by the planner to
narrow the model client's observed incremental read boundary when a grant
exists. `budget_usd` is a forecast and authorization check, not a hard
provider-side ceiling. The observed boundary is not equivalent to preventing
the provider from finishing an in-flight event; Atenea deliberately does not
claim an exact provider cap.

Citation evidence is retained by the reviewer and trace layers. It is not a
hard acceptance gate until a threshold exists that handles abbreviated paths,
renames and composed routes without rejecting correct answers or accepting
invented ones.

## Acceptance

The implementation and all boundaries are checked by:

```sh
bash scripts/v1-readiness.sh
```

The historical design ledger remains in
[`What is not built yet`](not-built-yet.md); this page and
[`v1 readiness`](v1-readiness.md) describe the current tree.
