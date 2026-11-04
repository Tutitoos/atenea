---
title: v1 contracts
weight: 8
---

# v1 contracts

Fases 10 y 11 convierten las preguntas de diseño restantes en contratos
explícitos. Una capacidad está completa cuando su comportamiento tiene código y
tests; una función que necesita otro provider o una decisión interactiva queda
registrada como límite deliberado, no implícita en la configuración. La matriz
operativa completa está en [v1.0 policy]({{< relref "v1-policy" >}}).

## Security and permissions

Effects are granted before dispatch, and an effect outside the grant is refused.
The CLI's `--allow` is the explicit escalation mechanism.

There is no *implicit* interactive permission prompt, and that is the deliberate
limit: no adapter and no daemon path stops to ask. A process running unattended
under systemd or launchd has no terminal to be asked on, so a prompt introduced
there would either hang the run or be answered by nobody. Introducing one would
require a separate UI/session contract.

What does exist is an interactive permission gate the caller asks for by name.
`--confirm` on `ask` prints the execution
summary -- the budget and the effects about to be granted -- and requires a
confirmation on a TTY before anything is dispatched; without a TTY it refuses
rather than proceeding. It is a floor for one command, opted into per
invocation. `backup discard` requires the same word for the same reason.

The distinction matters because the two are easy to conflate: Atenea will never
interrupt a run to ask, and Atenea will always ask when the caller said to.

## Provider boundaries

Provider boundary errors are mapped into the shared bins: permission and
budget refusals become `permission_denied`, authentication, rate limits and
quota exhaustion become `unavailable`, and context overflow becomes
`invalid_input`. Timeout and caller cancellation remain distinct.

`budget_usd` is a forecast and authorization check, not a hard provider-side
ceiling: the core rejects a completed result whose reported cost exceeds the
grant at the common runner boundary for every adapter, after the provider
reports the turn, and Atenea deliberately does not claim an exact provider cap.

> The built-in agents (`atenea agent`), their citation gate and the OpenCode
> model backend were measured when this contract was written; they are no
> longer part of Atenea (see #212).

## Acceptance

The implementation and all boundaries are checked by:

```sh
bash scripts/v1-readiness.sh
```

The historical design ledger remains in
[`What is not built yet`]({{< relref "not-built-yet" >}}); this page and
[`v1 readiness`]({{< relref "v1-readiness" >}}) describe the current tree.
