---
title: What a capability is worth
weight: 10
---

# What a capability is worth

This project's premise is that a model answering a question about a repository
should be handed a *capability* — one narrow, indexed, declared tool — instead
of the general file-searching tools every coding CLI already ships. Until
2026-08-14 that was an argument. This page is the first measurement.

The short version: **the same question, the same model, answered 19× faster and
15× cheaper through `code.search` than through the CLI's built-in grep.**

## The measurement

Both runs used the same Claude Code CLI, the same model (`claude-opus-5`), the
same repository (this one), and the same question, worded identically:

> Use the atenea `code.search` tool to find the literal text `SweepOrphans` in
> this repository. Reply with just the count and the file:line list.

The two runs differed in one thing: whether Atenea's MCP server was reachable in
the turn.

| | built-ins only | `code.search` |
|---|---|---|
| wall clock | **115 s** | **6 s** |
| cost (CLI's own `total_cost_usd`) | **$0.56** | **$0.038** |
| answer | 8 matches, correct | 8 matches, correct |

Both answers were right. That matters more than the ratio: this is not a
trade of accuracy for speed, and if the cheap one had been wrong the numbers
would mean nothing.

## Why the difference is that large

A grep-shaped tool answers one question per call, and the model does not know
which call is the last one. Reading the built-in run's turn back, it spent its
115 seconds the way a person would: a broad search, then a narrower one, then
opening three files to check the hits were real, then a fourth to be sure the
list was complete. Every one of those steps is a round trip carrying the whole
conversation, and each one is priced.

`code.search` is one call against an index that already knows the answer's
shape. The model asks once, receives a complete list, and stops. There is no
verification loop because the tool's contract already promises what the loop was
checking.

So the saving is not "the tool is faster than grep" — grep is very fast. It is
that **a declared capability collapses a search loop into one exchange**, and the
loop is what costs money.

## What this does not prove

- **One question, one repository, one model.** A question with no crisp answer
  — "how does this project handle retries" — has no reason to collapse the same
  way, because the loop is doing real work rather than re-checking itself.
- **The index was warm.** Building it is not free, and this measurement does not
  price it. On a repository nobody has indexed, the first question pays for both.
- **The prices are the CLI's own list-price arithmetic on subscription traffic**,
  not a reconciled bill. The ratio is more trustworthy than either absolute.
- **It says nothing about correctness at scale.** Two right answers is two.

## The measurement that produced it

The context is worth keeping, because the number was found by accident while
chasing a different bug. Five orchestrator runs against this repository had cost
$6.68, and the first four dispatched **zero** capabilities: the agent adapter
passed `--safe-mode`, which the CLI's own `--help` says disables MCP servers,
and it handed the turn the CLI's whole built-in tool set beside Atenea's
capabilities. A model holding `Grep` next to `code.search` uses `Grep`.

That is where the control came from — the built-in column is not a strawman
constructed to lose, it is **what this project was actually doing for four runs
while believing otherwise**. The fix is commit `de60922`; the run that followed
dispatched 17 capabilities and its exploration cost fell from $2.16 to $1.63.

Two things follow for anyone extending this. The comparison is only honest while
both sides get the same question — the temptation is to give the capability a
question shaped like its index. And a capability that is merely *offered* beside
the built-ins is not offered at all: it has to replace them, which is why the
explorer now carries `Read` and `Glob` and nothing else that searches.
