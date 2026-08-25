# atenea-desktop-helper

The far side of `internal/adapter/desktop`. Speaks MCP over its own stdin and
stdout, exactly like the other stdio servers Atenea supervises, so the Go side
reuses `internal/mcpstdio` rather than inventing a transport.

## Why a separate process rather than cgo

Two measured reasons, not taste.

**TCC.** macOS attributes a device permission to the *responsible ancestor*,
not to the binary that asks. Measured on macOS 26.6: a signed executable whose
own identifier was never authorized reports full Accessibility and Screen
Recording merely for having been launched from a terminal that has them. What
follows is that the permission has to belong to `atenea` itself, and that
`atenea` must be the responsible ancestor -- which it is when launchd starts
it as a LaunchAgent, and is not when somebody runs it from a shell.

**Coverage.** The CI matrix builds and tests on macOS as well as Linux, and a
GitHub runner has no graphical session and no way to grant TCC. A cgo slab
inside the Go packages would be compiled and counted on the macOS legs while
being unreachable there, dragging the profile under the 80% target the gate
enforces. Out here it is not in the Go profile at all.

## Building

    swift build -c release --package-path helper

Then point `orchestrator.desktop.process.command` at
`helper/.build/release/atenea-desktop-helper`.

## Why it is built here and shipped nowhere

Distributing a macOS binary that drives the screen needs a Developer ID
signature and notarization, which need a paid Apple Developer Program
membership. Measured on macOS 26.6, the weaker options do not substitute:

    spctl -a -vv -t exec probe        -> rejected          (ad-hoc)
    spctl -a -vv -t exec probe-dev    -> rejected          (Apple Development)

An Apple Development signature is rejected on another machine exactly as an
unsigned binary is, so signing with one buys nothing for distribution.

What does work is compiling here. Locally built code carries
`com.apple.provenance` but not `com.apple.quarantine`, so Gatekeeper never
enters and **no certificate is needed by anybody** to build or run this.

## What a certificate would buy, and it is not the right to run

Only that a TCC grant survives a rebuild. An ad-hoc signature's designated
requirement is:

    designated => cdhash H"2634171fccd56db347de42bd21dc9b929c8e8f68"

pinned to a hash that changes on every build -- measured, even rebuilding
identical source, because `swiftc` output is not reproducible. With any real
certificate it becomes:

    designated => identifier "com.tutitoos.atenea.desktop" and anchor apple generic and ...

which has no hash in it: three builds with three different hashes produced an
identical requirement. So signing your own local build with your own Apple
Development identity -- free with any Apple ID -- means granting Accessibility
once instead of after every rebuild. An optional upgrade, not a requirement:

    codesign -f -s "Apple Development: YOUR NAME (TEAMID)" \
      --identifier com.tutitoos.atenea.desktop --options runtime \
      helper/.build/release/atenea-desktop-helper

## And the permission is not the helper's anyway

macOS attributes a TCC grant to the **responsible ancestor**, not to the
binary that asks. Measured: a signed helper reported full access launched from
a terminal that held it, and no access launched from anywhere else; a binary
with an identifier that was never authorized reported full access for the same
reason. A `.app` bundle does not change this -- only launchd does.

What follows is that the grant has to belong to `atenea` itself, which happens
when launchd starts it as a LaunchAgent. `internal/adapter/desktop` refuses
the device effect otherwise rather than succeeding on a terminal's permission.
