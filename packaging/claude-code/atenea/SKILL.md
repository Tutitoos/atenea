---
name: atenea
description: Run a read-only Atenea status, metrics, traces, catalog, doctor, detect, incidents, floor, config or intent command.
disable-model-invocation: true
---

Interpret the arguments after /atenea as one Atenea read-only command.
Call only the Atenea MCP tool atenea.command, passing the command and its typed options.
Present the Markdown returned by Atenea unchanged.
Never use Bash, native Computer Use, or another tool to implement this command.

Arguments: $ARGUMENTS
