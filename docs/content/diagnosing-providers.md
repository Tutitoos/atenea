---
title: When a provider looks flaky
weight: 5
---

# When a provider looks flaky

A report that reads *"Serena is unstable"* is almost never about Serena.

It is the shape Atenea's own funnel gives to a fault underneath **every**
provider on the same transport. The funnel hides the fault wherever it has
somewhere else to go and exposes it only where it does not, so a whole-seam
outage arrives on your desk named after the one capability with no alternative.

This page is the write-up of a real instance. The specific bug is worth knowing;
the shape is worth knowing better, because the next one will wear a different
name.

## Why one name comes back

Two different questions decide whether an implementation can answer, and the
funnel keeps them apart on purpose. **Reach** is a wiring decision made before
anything ran: is an adapter attached that claims this implementation? **Health**
is what a probe found. An implementation nobody wired up and one that is wired
and broken are different findings, and only the second is worth debugging.

`atenea status` prints both, in the orchestrator block:

```text
serves     ripgrep
no runner  claude.search, codebase-memory.search, serena.definition,
           serena.implementations, serena.references, serena.search
```

That is the stock catalogue: seven implementations declared, one reachable,
because `runners = ["omp"]` attaches a single adapter. Attaching Serena — the
configuration this write-up is about — makes it four:

| Capability | Reachable with Serena attached | Over |
| --- | --- | --- |
| `code.search` | `ripgrep` | a local binary |
| `symbol.definition` | `serena.definition` | **local HTTP** |
| `symbol.references` | `serena.references` | **local HTTP** |
| `symbol.implementations` | `serena.implementations` | **local HTTP** |

Note what the catalogue does *not* buy you here. `code.search` declares four
implementations, but three of them have no adapter — `serena.search` is
deliberately excluded even from the Serena adapter, which is wired for symbols
and refuses to claim a text search it has no code for. So the coverage is
lopsided in a way the capability list alone does not show: text search stands on
a local binary, and **every symbol capability stands on one transport**.

Now break that transport.

`code.search` never touched it. `ripgrep` answers, commissions finish, and
nothing is reported — correctly, because nothing failed.

The three symbol capabilities lose their only implementation at the same
instant. No survivor, so they fail out loud, every time.

The operator sees: *symbols are broken, search is fine.* The only name attached
to the broken half is Serena. So the report says Serena — and the next hour goes
into a component that is running perfectly.

> The blast radius of a transport fault is every capability whose **whole**
> implementation set sits on that transport. That set has a provider's name on
> it, and the transport does not, so the report comes back naming the provider.
> Before believing it, look at what still works and ask what it reaches over.

## The specific bug: `localhost` is not an address

Servers behind a local proxy bind an **address**, and clients are configured
with a **name**. Those are only the same thing while name resolution agrees with
them.

Observed on one machine, four MCP servers behind local proxies:

```text
$ ss -lntp
LISTEN  127.0.0.1:40010   thv    # serena
LISTEN  127.0.0.1:40011   thv    # context7
LISTEN  127.0.0.1:40020   thv    # semgrep
LISTEN  127.0.0.1:40021   thv    # chrome-devtools
```

Every one of them is bound to `127.0.0.1` — IPv4, and only IPv4. Nothing is
listening on `::1`.

The client configuration had been rewritten to point at `http://localhost:PORT`.
That works, right up until the moment `localhost` resolves to `::1` first —
which is the default nearly everywhere, and arrives on its own with a resolver
update, an `/etc/hosts` edit, a container network change, or a distribution
bumping `nsswitch.conf`. Nobody has to do anything wrong. The machine simply
starts answering a question differently one morning.

When it flips, the client tries `::1`, gets connection refused, and **all four
servers become unreachable in the same instant**. Not degraded. Not slow. Gone,
together, for a reason that is nowhere near any of them.

The nasty part is the state in between. A resolver that returns both families
and a client that retries can produce a connection that works most of the time
and fails some of the time — which is the literal definition of the word in the
original report.

## Why Atenea is not exposed to it

Atenea's shipped endpoint is a literal address:

```toml
[orchestrator.serena]
endpoint = "http://127.0.0.1:40010/mcp"
```

Not `localhost`. That is deliberate and it is load-bearing: the proxy binds
`127.0.0.1`, so naming `127.0.0.1` is the only spelling that cannot be broken by
something outside Atenea changing its mind about what a name means. There is no
lookup to go wrong, no address family to guess, and no failure that arrives
without anybody touching Atenea.

It reads like a tidiness wart. `localhost` is friendlier, it is what everybody
writes, and swapping it in would pass every test in this repository. It would
also hand the whole class of failure above to a component that currently cannot
have it.

**Do not change it to a name.** If a future proxy binds something else, pin the
new address — not a name that resolves to it today.

The practical consequence during the incident: Atenea kept answering symbol
questions while the CLIs on the same machine could not reach a single MCP
server. Which, if you are not expecting it, makes the fault look even more like
Serena misbehaving — same server, working for one caller and not another,
minutes apart.

## Telling the two apart

The question is always: **is one provider down, or is the ground under several
of them gone?** Two commands answer it.

```sh
atenea status
```

Read the implementation lines, not the capability names. One provider having a
bad day looks like one line changing. A transport fault looks like *every*
implementation that reaches the same way going at once, while the local ones
stay green. If `ripgrep` is fine and everything behind a proxy is not, stop
reading the provider list — the answer is not in it.

```sh
atenea select code.search --repo current
```

`select` asks the funnel who *would* answer and prints every stage with what it
dropped and why. This is how you see a fallback that is working silently — the
capability still answers, and the trace names whoever quietly stopped being
available:

```text
chosen      ripgrep  (the only surviving implementation)

funnel
  constraints  4 in -> 2 out: claude.search, ripgrep
      dropped codebase-memory.search: needs an index from provider codebase-memory, repository has none
      dropped serena.search: needs an index from provider serena, repository has none
  reach        2 in -> 1 out: ripgrep
      dropped claude.search: no attached runner serves it
  health       1 in -> 1 out: ripgrep
  choice       1 in -> 1 out: ripgrep
```

Read the stage a name was dropped at, not just that it was dropped. `reach`
means nobody wired it up — a settings question, and no reason to touch the
provider. `health` means it was wired and did not answer, which is the only one
of the two that is an outage.

Then check the seam itself, from outside Atenea:

```sh
ss -lntp | grep :PORT       # what address is the proxy actually bound to?
getent ahosts localhost     # what does the name resolve to, in what order?
```

If the first says `127.0.0.1` and the second lists `::1` first, you have found
it, and you have found it for every server on that host — not just the one in
the report.

## What not to do about it

Editing the client configuration by hand is the obvious fix and it does not
last. Tooling that registers MCP servers rewrites those entries, so a hand-edit
survives until the next time a workload starts and then quietly reverts. The
symptom returns weeks later looking like a new bug.

The fix belongs wherever the registration happens, so that the address is
written correctly every time rather than corrected after the fact.

And resist the tempting one-liner: **do not add `::1 localhost` to `/etc/hosts`
to "make both work"**. It changes the answer for every program on the machine to
solve a problem in three configuration files, and the next thing that breaks
will have nothing to do with MCP.

## The general lesson

Three things worth carrying forward:

1. **A fault reports under the name of whatever had no fallback.** Before
   trusting the name in a bug report, count the implementations behind each
   capability that is failing and each one that is not. The pattern of what
   still works locates the fault far better than the failure itself.
2. **Bind addresses and configured names are two different facts.** They agree
   until something outside your project decides otherwise, and the failure
   arrives with no change on your side to point at.
3. **A health check nobody reads is not a health check.** The invariant that
   caught this had been failing every fifteen minutes, on a timer, for as long
   as anyone cared to look back. The information existed the whole time. What
   was missing was somebody reading it.
