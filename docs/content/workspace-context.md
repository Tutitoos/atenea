# `workspace.context`

`workspace.context` is a coordinator operation for asking `code.context` about
several explicitly named repositories. It is not a catalog capability and it
does not introduce a workspace provider. Each target is canonicalized to its
physical root, authorized, and then dispatched as one ordinary `code.context`
request carrying exactly one `Repository`.

Targets are supplied in input order and the response keeps that order. IDs and
physical roots must be unique; aliases and duplicate IDs are rejected before
any identity probe or provider call. A request contains one to eight targets,
uses at most four children concurrently by default, and may lower that limit.
The budget and read permission are shared by the coordinator and copied to
each child conservatively. The result is never flattened or cached as an
aggregate, so P11 cache and quality evidence remain scoped to each repository.

Each row carries its own result, evidence, provider, implementation, notices,
out-of-scope count, error, and cursor. A `partial` result is reported when a
child fails, is canceled before starting, returns a truncated or structural
partial payload, or reports `partial`, `lower_bound`, `unknown`, or
`source_trimmed` evidence. Continuations are bound to a repository ID; a
cursor for an unknown or different repository is rejected before dispatch.
Cursor bytes are opaque and preserved exactly up to `contract.MaxPersistedRaw`
(64 KiB). Larger input cursors fail preflight; larger provider cursors are
omitted and mark that repository partial/truncated with a safe diagnostic.

MCP exposes a special `workspace.context` tool whose schema requires explicit
`targets` with an `id`; `root` is an optional assertion checked against the
configured repository and is resolved server-side when omitted. It is
intentionally exempt from the ordinary single `repository` argument. Physical
roots are never emitted in the result envelope; nested provider paths are
rewritten to stable `repository/relative/path` references. The CLI equivalent is:

```text
atenea ask workspace.context --repo api --repo web --payload context.json --json
```

The CLI resolves IDs to configured roots and the same coordinator performs the
final physical-root and authorization preflight. Before each child, MCP sends
one visible Markdown activity notification and the CLI prints one preamble in
input order. These workspace notices are ephemeral outside a workflow and are
not presented as durable P05 outbox records; this operation does not add a
second outbox or chat transport.
