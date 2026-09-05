import os
from pathlib import Path
import runpy
import subprocess
import tempfile
import unittest


class LauncherTests(unittest.TestCase):
    def test_pinned_execution_with_minimum_path_and_changed_runtime(self):
        install = runpy.run_path(str(Path(__file__).parents[1] / "scripts/pin-node-launcher.py"))["install"]
        with tempfile.TemporaryDirectory(prefix="node launcher ") as directory:
            root = Path(directory)
            node, entry, output = (root / name for name in ("node", "cli.js", "launcher"))
            node.write_text('#!/bin/sh\nif [ "$1" = --version ]; then echo v22.9.0; elif [ "$2" = --version ]; then echo 0.20.10; else printf "%s\\n" "$@"; fi\n')
            node.chmod(0o755); entry.write_text("fixture")
            install(node, entry, output, "0.20.10")
            observed = subprocess.check_output([str(output), "literal $(not-run)"], text=True, env={"PATH": "/usr/bin:/bin"})
            self.assertEqual(observed.splitlines(), [str(entry.resolve()), "literal $(not-run)"])
            with self.assertRaises(ValueError):
                install(node, entry, output, "unknown")
            node.write_text('#!/bin/sh\necho v24.0.0\n')
            result = subprocess.run([str(output)], capture_output=True, env={"PATH": "/usr/bin:/bin"})
            self.assertEqual(result.returncode, 78)


if __name__ == "__main__":
    unittest.main()
