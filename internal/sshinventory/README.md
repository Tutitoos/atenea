# OpenSSH inventory parser

`Scan(userConfig, systemConfig)` enumerates only concrete `Host` tokens from
the selected user's config and then the system config. It reads regular local
files and expands `Include` globs in lexical order. Relative includes stay
relative to the top-level config directory (`~/.ssh` or `/etc/ssh` in a normal
installation). Each alias carries a file and line number. The snapshot digest
changes when a traversed file or Include expansion changes.

The parser never invokes `ssh`, a shell, `Match exec`, a proxy, or a network
client. It bounds file count, nesting, bytes and line length. Wildcard and
negated `Host` patterns only filter inclusion; they are not devices. A
conditional Include that cannot be resolved without evaluating `Match` yields
a conditional alias and a diagnostic. Unsupported dynamic Include tokens and
syntax errors also yield diagnostics.

This is a **display inventory**, not effective OpenSSH configuration or
authorization. The selected-host resolver must separately account for
first-value-wins options, system configuration, `Match`, canonicalization,
proxy/known-host helpers, host-key policy and changes to the snapshot. It must
refuse combinations it cannot evaluate safely. No caller may use the alias or
snapshot digest alone as a target identity or permission to connect.

The parser follows the [OpenSSH client configuration manual](https://man.openbsd.org/ssh_config)
for Host syntax and Include roots. Its conservative handling of dynamic
conditions deliberately reports unresolved state rather than running them.
