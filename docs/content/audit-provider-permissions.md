# Provider permission follow-up

The OpenCode model backend (`[model].backend = "opencode"`) was measured at the time of this audit; it is no longer part of Atenea (see #212). OpenCode remains a supported client.

## OpenCode finalization

The local OpenCode 1.18.20 bundle supports inline configuration, OPENCODE_PERMISSION, project-config disabling and pure mode. Finalization requires 1.18.20+ within the 1.x line; absent/older/unrecognized versions fail explicitly. It resumes session data but uses an isolated configuration directory, no external/default plugins and a dedicated deny-all agent. Provider credentials and session data are retained; a custom provider that depends on excluded configuration may be unavailable rather than silently regaining tools. This is provider permission configuration, not an OS sandbox.

## claude-mem corpus tools

claude-mem was measured at the time of this audit; it is no longer a supported or declared backend (see #207). Its proposed corpus-tool overrides (R06) are moot and were removed.
