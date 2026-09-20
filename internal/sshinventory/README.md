# OpenSSH inventory parser

`Scan(userConfig, systemConfig)` enumerates only concrete `Host` tokens from
the selected user's config and then the system config. It reads regular local
files and expands `Include` globs in lexical order. Relative user includes
resolve against the active user's `~/.ssh`, even when the configuration file
is elsewhere. User `~/` includes resolve against the home directory itself.
System includes resolve against the system configuration directory, and system
tilde includes remain unresolved. Nested files keep the same root. Each alias
carries a file and line number. Keywords are case-insensitive; Host patterns
and aliases match case-sensitively. The snapshot digest changes when a
traversed file or Include expansion changes.

The parser never invokes `ssh`, a shell, `Match exec`, a proxy, or a network
client. It bounds file count, nesting, bytes and line length. Wildcard and
negated `Host` patterns only filter inclusion; they are not devices. A
conditional Include that cannot be resolved without evaluating `Match` yields
a conditional alias and a diagnostic. Unsupported dynamic Include tokens and
syntax errors also yield diagnostics. The static `Match all` condition is
unconditional. A single `Match originalhost` pattern list using literal names,
`*`, `?` and comma-separated exclusions is evaluated against the selected
alias. Combined criteria, `Match exec` and other dynamic criteria remain
unresolved and are never executed while listing.

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
is never run by resolution. `Match all` and a single `Match originalhost`
pattern list are supported; other `Match` criteria,
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
arguments rather than silently changing their path.
`ProxyJump none` and `ProxyCommand none` explicitly disable the proxy route,
so those selected configurations can use direct fingerprint matching, private
enrollment, provisional locking and the diagnostic; other proxy values remain
unsupported. The caller must provide a
known-hosts snapshot whose fingerprints were independently reviewed; the
builder cannot verify that review. A temporary empty config suppresses the
user and system configuration during a later probe, while the supplied
known-hosts copy is the sole trust source. The arguments disable remote
commands, PTY, stdin, agent use and forwarding, port forwarding, local commands,
connection sharing and automatic host-key updates. Password prompts are
disabled for this diagnostic plan. A leading `IdentityFile=none` prevents
OpenSSH from falling back to implicit personal keys if the selected
`IdentityFile` is missing. When the selected config specifies no identity,
public-key authentication is disabled and a rejected login is reported as
`authentication_required`. If every explicit identity file is definitely
absent at the time of a rejected login, the same hint is returned. Dynamic
paths and existing files stay `authentication_rejected`: file presence alone
does not prove a usable private key. This is a trust diagnostic, not a
substitute for normal SSH default-key behavior. `Close` removes its temporary files.
The builder revalidates the selection before and after creating the temporary
files. `Arguments` returns a defensive copy; `Revalidate` rereads the source
config and refuses a stale or closed plan.

Selected `RemoteCommand` and `LocalCommand` text, plus validated
`PermitLocalCommand` and `RequestTTY` settings, are read but never copied into
the restricted diagnostic plan. The plan's empty config, `-N`, `-T` and
`PermitLocalCommand=no` prevent those selected-session actions during the
diagnostic. Common `LocalForward`, `RemoteForward`, `DynamicForward`, agent/X11
forwarding and control-sharing settings are likewise suppressed: the plan's
effective `ssh -G` output contains no selected forwards or control path.
Selected `IdentitiesOnly`, `IdentityAgent` and bounded `AddKeysToAgent` values
are read but the diagnostic uses only explicit identity files, disables the
agent socket and prevents adding keys to an agent. It therefore may report
authentication as required when an ordinary session would use an agent.
Unsupported active options and unsupported option forms still fail resolution.

`ExecuteDirectProbe` revalidates the plan, starts the local OpenSSH client with
an eight-second limit and stops as soon as its private `-E` diagnostic log
reports public-key authentication. It never opens a remote command or returns
raw logs. Server-supplied banners are discarded separately; the loopback test
includes a forged authentication banner to guard this boundary. Failed-process
classification is advisory. The successful result is explicitly
client-reported authentication to the supplied pin, not a durable device
identity, authorization, agent-installation check or permission to send a
prompt. The caller must still establish independent review of the pin and
close the plan. This diagnostic has not established trust in a physical device
or native GUI and agent readiness.

No code here supplies a verified physical-device identity.
ProxyJump and ProxyCommand routes remain unresolved until they can be probed
without changing the configured path or its security properties.

The controlled `sshd` test starts a disposable server on IPv4 loopback, makes
temporary client/server keys and checks three real OpenSSH outcomes: a known
key with public-key authentication, an unknown key, and a changed key. It also
checks a rejected public-key login and that diagnostics do not modify their
known-hosts copy. The test skips when the local OpenSSH server fixture is
unavailable and on Windows. It does not establish native Windows support or
trust in any external host.

`ClassifyOpenSSHFailure` converts bounded failed-process diagnostics into an
advisory hint for timeout, DNS, route, host-key and authentication rejection.
It does not keep raw logs or treat a zero exit or `Authenticated to` text as
proof of authentication. OpenSSH diagnostics can include server-controlled
banners, so these hints must never authorize trust or credentials. A rejected
login alone does not show whether credentials were absent or incorrect; the
missing-file hint uses a separate local file check and remains advisory.

`ProvisionalDirectLockKey` hashes the normalized direct hostname, port and
account to serialize two aliases that resolve to the same apparent endpoint.
It ignores display alias, config snapshot and HostKeyAlias, so a renamed alias
cannot create a second pre-authentication lock. This key is deliberately
provisional: DNS names and IP addresses do not prove a device identity, and
config or key changes still require fresh trust and authorization review.
Proxy routes are rejected until their target identity can be bound safely.

`MatchedDirectAccountKey` derives a second opaque lock key from a
fingerprint-matched ED25519 host key and the selected account. It revalidates
the config and checks that the matched entry belongs to that alias and
snapshot. Two aliases with the same matched key and account share the key;
changing the host key or account changes it. This does not prove independent
fingerprint review, user confirmation, or physical device uniqueness: cloned
machines can share a host key. Authorization must separately bind and
invalidate the current config and credential state.

`MatchDirectED25519HostKey` accepts a plain candidate ED25519 public key and
a SHA256 fingerprint supplied from an independent channel. It validates the
key blob and exact fingerprint, binds one known-hosts line to the selected
direct hostname/port or `HostKeyAlias`, and rechecks the config snapshot. It
does not persist the line or prove that the caller's fingerprint source was
independent. The UI must require explicit user review before persisting or
using it. Hashed known-host entries, host certificates, CA and revoked markers,
other key algorithms and proxy routes remain unsupported by this helper; do
not silently convert them to a plain ED25519 pin.

`ConfirmDirectED25519HostKey` checks a separately supplied, affirmative
fingerprint confirmation against the matched key and current selected config.
It returns an in-memory `ConfirmedDirectHostKey` that
`PrepareConfirmedDirectProbe` binds to an exact temporary known-hosts pin.
Mismatched fingerprints, another alias and changed config are rejected. The
library cannot prove that a person reviewed the independent source; the UI
must collect that decision explicitly. Confirmation remains in memory until
the caller explicitly invokes the private enrollment store.

`OpenPrivateDirectTrustStore` opens an app-owned directory supplied by the
caller. On macOS and Linux it requires a private, non-symlink directory and
stores each confirmed ED25519 pin in a mode-0600 file. On Windows it creates
a directory with a protected DACL for the current account and LocalSystem,
and checks the directory and pin owner and ACL before use. Record locks use
`LockFileEx`. Reads refuse a symlinked or newly swapped pin and recheck the
opened file's identity and permissions or ACL. Operations use an opened
directory handle and reject a store
directory replaced after opening, so writes remain inside the original
app-owned directory. `Enroll` is create-only:
the same key is idempotent, while a changed key at the same config/account/
host binding is rejected. `Rotate` requires the approved old fingerprint and
a separately confirmed new key. It locks the record, writes and syncs the
replacement in a private temporary file, then replaces the old pin atomically.
An absent, malformed, stale or unconfirmed pin fails closed. The library
cannot verify that a person supplied either review; the UI must gather both
explicitly. The filename binds the
config snapshot, account, port and OpenSSH known-hosts name, so a changed
config cannot silently reuse the old record. `PrepareEnrolledDirectProbe`
reloads that exact pin and still uses the restricted diagnostic plan. Its
executor rechecks the stored pin and holds the record lock during the probe,
so a plan created before rotation cannot authenticate with the retired key.
Raw OpenSSH arguments are withheld for enrolled plans to keep this check in
the execution path. No personal `known_hosts` file is edited. The caller must
choose and protect the
parent directory, and the UI must gather the user's confirmation. The Windows
storage tests passed under two native account sessions; that does not establish
Windows controller, vault, connector or GUI readiness. A stored
pin does not authorize prompts, commands, credentials or agent installation.
`EnrolledDirectHostKeyFingerprint` reads the current local pin without a
network call, so the UI can show the approved fingerprint separately from
connectivity and remote-agent status. A stale config or malformed pin fails
closed; the reported fingerprint alone does not authenticate a live host.
The loopback `sshd` fixture exercises confirmation, enrollment, reload and
client-observed public-key authentication with disposable host and client keys.
It also rotates the stored pin to another confirmed fixture key and observes
that the original server key is then rejected as changed.
