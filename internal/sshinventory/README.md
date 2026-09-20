# OpenSSH inventory parser

`Scan(userConfig, systemConfig)` enumerates only concrete `Host` tokens from
the selected user's config and then the system config. It reads regular local
files and expands `Include` globs in lexical order. Relative includes stay
relative to the top-level config directory (`~/.ssh` or `/etc/ssh` in a normal
installation). Each alias carries a file and line number. Keywords are
case-insensitive; Host patterns and aliases match case-sensitively. The snapshot digest
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

`ResolveStatic` is the first selected-alias boundary. It applies active
`Host` blocks, ordered `Include` files and OpenSSH's first-value rule for
`HostName`, `User`, `Port`, `HostKeyAlias`, `ProxyJump` and `ProxyCommand`;
`IdentityFile` entries accumulate. The proxy command is returned as text and
is never run by resolution. `Match all` is supported; other `Match` criteria,
canonicalization, dynamic target tokens and unrecognized active options fail
with `ErrUnresolved`. A before/after inventory fingerprint detects ordinary
configuration changes during this read. The selected values are provisional:
the later probe must review the route and known-host policy, verify the
fingerprint again and bind an authenticated destination and account before
trust or credentials can be used.

`RevalidateSelection` repeats static resolution against the same user/system
roots and rejects a changed snapshot or modified selected values. It is a
precondition check, not a verified host identity or an authorization grant.

`PrepareDirectProbe` builds a restricted OpenSSH argument list for a selected
direct route. It never starts SSH. It rejects proxy routes and unsafe target
arguments rather than silently changing their path. The caller must provide a
known-hosts snapshot whose fingerprints were independently reviewed; the
builder cannot verify that review. A temporary empty config suppresses the
user and system configuration during a later probe, while the supplied
known-hosts copy is the sole trust source. The arguments disable remote
commands, PTY, stdin, agent use and forwarding, port forwarding, local commands,
connection sharing and automatic host-key updates. Password prompts are
disabled for this diagnostic plan. `Close` removes its temporary files.
The builder revalidates the selection before and after creating the temporary
files. A future executor must revalidate again immediately before use.

This is only a command plan. No code here executes it, interprets OpenSSH
errors, enrolls keys, proves authenticated connectivity or supplies a stable
target identity. A future executor must recheck the config snapshot and
reviewed trust evidence immediately before use, impose a bounded lifetime,
and verify platform-specific private-file access (especially Windows ACLs).
ProxyJump and ProxyCommand routes remain unresolved until they can be probed
without changing the configured path or its security properties.

The controlled `sshd` test starts a disposable server on IPv4 loopback, makes
temporary client/server keys and checks three real OpenSSH outcomes: a known
key with public-key authentication, an unknown key, and a changed key. It also
checks a rejected public-key login and that diagnostics do not modify their
known-hosts copy. The test skips when the local OpenSSH server fixture is
unavailable and on Windows. It does not establish native Windows support or
trust in any external host.

`ClassifyOpenSSHFailure` converts a bounded failed-process stderr into an
advisory hint for timeout, DNS, route, host-key and authentication rejection.
It does not keep raw logs or treat a zero exit or `Authenticated to` text as
proof of authentication. OpenSSH diagnostics can include server-controlled
banners, so these hints must never authorize trust or credentials. A rejected
login alone does not show whether credentials were absent or incorrect.

`ProvisionalDirectLockKey` hashes the normalized direct hostname, port and
account to serialize two aliases that resolve to the same apparent endpoint.
It ignores display alias, config snapshot and HostKeyAlias, so a renamed alias
cannot create a second pre-authentication lock. This key is deliberately
provisional: DNS names and IP addresses do not prove a device identity, and
config or key changes still require fresh trust and authorization review.
Proxy routes are rejected until their target identity can be bound safely.

`MatchDirectED25519HostKey` accepts a plain candidate ED25519 public key and
a SHA256 fingerprint supplied from an independent channel. It validates the
key blob and exact fingerprint, binds one known-hosts line to the selected
direct hostname/port or `HostKeyAlias`, and rechecks the config snapshot. It
does not persist the line or prove that the caller's fingerprint source was
independent. The UI must require explicit user review before persisting or
using it. Hashed known-host entries, host certificates, CA and revoked markers,
other key algorithms and proxy routes remain unsupported by this helper; do
not silently convert them to a plain ED25519 pin.
