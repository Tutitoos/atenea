---
title: Controlled MCP activation
---

# Controlled MCP activation

New MCP integrations pass through a durable proposal before they can enter
the active settings file. Discovery performs a real protocol handshake and
records requested and observed protocol versions, server identity and declared
protocol capabilities:

```bash
atenea mcp propose --id docs \
  --url http://127.0.0.1:40010/mcp \
  --protocol auto

atenea mcp proposals
```

The proposal says `functional validation: not tested` because a successful
handshake does not prove that a tool works. Descriptions are never interpreted
as capabilities or permission grants. Raw exposure therefore requires an
explicit allow-list and effect ceiling:

```bash
atenea mcp propose --id scanner \
  --url http://127.0.0.1:40020/mcp \
  --protocol modern-pin \
  --expose raw \
  --tool scan \
  --effect read
```

Creating a proposal changes no active configuration. Activation is refused
until the exact evidence-bound proposal is explicitly authorized:

```bash
atenea mcp activate scanner --authorize
```

Activation validates the resulting settings before replacing the file. It
appends a new `[[mcp_server]]` block and refuses an existing identifier, so an
older integration is never silently replaced during transition. Restart or
reload the service to make the new declaration connected. A real tool call is
still required before the integration can be reported as functionally tested.
