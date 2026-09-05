#!/usr/bin/env python3
"""Install a launcher with explicit, verified Node and CLI identities.

No PATH or NVM glob is consulted at launch. Existing launchers are backed up.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import time


def install(node, entry, output, expected_version):
    node, entry, output = (Path(value).expanduser().absolute() for value in (node, entry, output))
    node, entry = node.resolve(strict=True), entry.resolve(strict=True)
    node_version = subprocess.check_output([str(node), "--version"], text=True, timeout=10).strip()
    if int(node_version.lstrip("v").split(".")[0]) < 22:
        raise ValueError("Node 22 or newer is required")
    cli_version = subprocess.check_output([str(node), str(entry), "--version"], text=True, timeout=10).strip()
    if cli_version != expected_version:
        raise ValueError(f"CLI version mismatch: expected {expected_version}, observed {cli_version}")
    body = "#!/bin/sh\nset -eu\n"
    body += f"[ \"$({shlex.quote(str(node))} --version)\" = {shlex.quote(node_version)} ] || {{ echo 'Pinned Node changed; validate and repin the launcher' >&2; exit 78; }}\n"
    body += f"exec {shlex.quote(str(node))} {shlex.quote(str(entry))} \"$@\"\n"
    manifest = {"node": str(node), "node_version": node_version, "entry": str(entry),
                "cli_version": cli_version, "entry_sha256": hashlib.sha256(entry.read_bytes()).hexdigest()}
    output.parent.mkdir(parents=True, exist_ok=True)
    if output.exists():
        backup = output.with_name(output.name + f".backup.{time.time_ns()}")
        backup.write_bytes(output.read_bytes())
        backup.chmod(output.stat().st_mode & 0o777)
    with tempfile.NamedTemporaryFile(dir=output.parent, delete=False) as file:
        temporary = Path(file.name)
        try:
            file.write(body.encode()); file.flush(); os.fsync(file.fileno())
            temporary.chmod(0o755)
            os.replace(temporary, output)
        finally:
            temporary.unlink(missing_ok=True)
    manifest_path = output.with_name(output.name + ".runtime.json")
    descriptor = os.open(manifest_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as file:
        json.dump(manifest, file, indent=2)
    return manifest


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for argument in ("node", "entry", "output", "expected-version"):
        parser.add_argument("--" + argument, required=True)
    arguments = parser.parse_args()
    print(json.dumps(install(arguments.node, arguments.entry, arguments.output, arguments.expected_version), indent=2))
