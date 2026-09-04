# Provider permission follow-up

## OpenCode finalization

The local OpenCode 1.18.20 bundle supports inline configuration, OPENCODE_PERMISSION, project-config disabling and pure mode. Finalization requires 1.18.20+ within the 1.x line; absent/older/unrecognized versions fail explicitly. It resumes session data but uses an isolated configuration directory, no external/default plugins and a dedicated deny-all agent. Provider credentials and session data are retained; a custom provider that depends on excluded configuration may be unavailable rather than silently regaining tools. This is provider permission configuration, not an OS sandbox.

## claude-mem corpus tools — proposed local configuration change

The locally inspected mcp-server.cjs declares build_corpus as creating a knowledge corpus, rebuild_corpus as rebuilding it, and prime_corpus/reprime_corpus as creating or replacing AI session state. Their handlers send bodies to corpus maintenance endpoints. query_corpus invokes the primed knowledge agent; it is not assumed to be a free, stateless read. Actual billing and remote implementation were not exercised.

The following overrides belong inside the existing claude-mem MCP declaration, before the next server declaration. They are a proposal, not an applied modification to the operator's configuration. They add conservative write/external permissions to the existing read/process declaration.

```toml
[[mcp_server.tool]]
name = "build_corpus"
effects = ["read", "write", "process"]

[[mcp_server.tool]]
name = "rebuild_corpus"
effects = ["read", "write", "process"]

[[mcp_server.tool]]
name = "prime_corpus"
effects = ["read", "write", "process", "external"]

[[mcp_server.tool]]
name = "reprime_corpus"
effects = ["read", "write", "process", "external"]

[[mcp_server.tool]]
name = "query_corpus"
effects = ["read", "write", "process", "external"]
```

R06 is confirmed for the audited local declaration, not for Atenea defaults (which do not ship that MCP). Validate the edited configuration and review its profile permissions before applying it. No corpus, memory or paid session was changed during remediation.
