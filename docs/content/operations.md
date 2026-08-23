---
title: "Operations"
weight: 6
---

# Operations

This is the runbook for a published Atenea installation. It assumes a release
binary, not a checkout, and keeps the distinction clear: a green GitHub
workflow proves the artifact was built; `status` proves the service currently
answering on this machine.

## Release acceptance

The `postrelease` workflow runs after a successful `release` workflow on Linux
`amd64`/`arm64` and macOS `amd64`/`arm64`. It downloads the public installer and
proves, in a temporary directory:

1. checksum verification and first installation;
2. a second pinned installation with `atenea.previous` creation;
3. rollback to the previous binary;
4. removal of both binaries.

The same check can be run by hand:

```sh
bash scripts/release-smoke.sh 1.0.2
```

The check never passes `--service`, so it does not install a persistent agent.
Service installation is a separate, host-specific operation.

## First response

Start with the cheapest truthful screen:

```sh
atenea version
atenea status
atenea service status
atenea incidents
```

Interpret them in this order:

- `version` identifies the binary and contract actually being invoked.
- `status` reports the running service when its socket answers, and says when
  it had to fall back to disk.
- `service status` distinguishes installed, enabled and active.
- `incidents` reads the crash notebook; `incidents clear` marks entries read but
  does not delete them.

For a provider problem, use the declared probe before changing configuration:

```sh
atenea detect --repo REPOSITORY_ID
atenea catalog
atenea ask code.search --repo REPOSITORY_ID --set query=health
```

Do not treat `health=unknown` as a failure. It means that this process has not
observed a current answer yet. A down provider should carry a reason in the
health line and in the incident record.

For a human-reviewed direct execution, add the explicit TTY confirmation:

```sh
atenea task "find every TODO" --confirm
atenea ask code.search --repo current --set query=TODO --confirm
```

`--confirm` refuses piped or unattended input. The summary shows the requested
budget and effects before any provider is started.

Backups are inspectable and restorable into a new directory:

```sh
atenea backup list
atenea backup restore SNAPSHOT_NAME /tmp/atenea-restored
atenea backup restore SNAPSHOT_NAME /path/to/current --replace
atenea backup promote /path/to/current
atenea backup discard /path/to/current --confirm
```

Restore never overwrites an existing target unless --replace is supplied.
Replacement first renames the current directory to a retained
TARGET.atenea-previous sidecar, publishes the restored snapshot atomically,
and rolls back the rename if publication fails. A second replacement is refused
until the retained previous state is dealt with explicitly. Snapshot and restore
publication syncs file contents and directory entries where the platform supports it.
Promote reverses the last replacement and retains the current state as
TARGET.atenea-current. Discard is destructive and requires --confirm.

## Service recovery

Linux uses a per-user systemd unit:

```sh
atenea service status
systemctl --user status atenea.service
systemctl --user restart atenea.service
journalctl --user -u atenea.service -n 100 --no-pager
```

macOS uses a per-user launchd agent:

```sh
atenea service status
launchctl print gui/$(id -u)/com.tutitoos.atenea
launchctl kickstart -k gui/$(id -u)/com.tutitoos.atenea
```

If the service was installed with an old binary, reinstall the pinned release;
the unit path stays stable and the next restart picks up the new executable.
Do not use `sudo`: the service intentionally owns only the current user's
configuration, state, socket and provider sessions.

## Rollback and removal

The installer keeps exactly one previous binary when updating:

```sh
bash /tmp/atenea-install.sh --rollback
```

After a rollback, restart the service so the process in memory is replaced:

```sh
systemctl --user restart atenea.service                 # Linux
launchctl kickstart -k gui/$(id -u)/com.tutitoos.atenea # macOS
```

To remove Atenea and its service definition:

```sh
bash /tmp/atenea-install.sh --uninstall --service
```

This removes the installed binary and rollback copy. The state directory is
preserved deliberately; inspect or back it up before removing it manually.

## What to record in an incident

Record the release version, operating system/architecture, command output,
provider ID, repository ID, and the latest incident text. Include the service
manager's native output and whether the socket answered. A useful report can
be reproduced from those facts; “it was red” cannot.
