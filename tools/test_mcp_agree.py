"""Regression tests for file-only MCP auditing (no clients are started)."""
import json
from pathlib import Path
import runpy
import tempfile
import unittest
from unittest.mock import patch


class ConfigTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        module = runpy.run_path(str(Path(__file__).with_name("mcp-agree")))
        self.g = module["check_omp"].__globals__
        self.g["HOME"] = str(self.root)
        self.env = patch.dict("os.environ", {"PI_CODING_AGENT_DIR": "", "XDG_CONFIG_HOME": ""})
        self.env.start()
        self.addCleanup(self.env.stop)

    def write(self, path, text):
        file = self.root / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)
        return str(file)

    def test_jsonc_preserves_strings(self):
        p = self.write("input.jsonc", '{/* comment */"url":"https://host/x",'
                       '"text":"literal ,} // /* \\\"", // comment\n"list":[1,],}')
        self.assertEqual(self.g["load_jsonc"](p),
                         {"url": "https://host/x", "text": 'literal ,} // /* "', "list": [1]})

    def test_jsonc_rejects_corruption(self):
        for text in ('{/* unfinished', '{"x":}', '{"x":1} junk'):
            with self.assertRaises(ValueError):
                self.g["load_jsonc"](self.write("input.jsonc", text))

    def test_opencode_jsonc_and_disabled_core(self):
        self.write(".config/opencode/opencode.jsonc",
                   '{"mcp":{"atenea":{"enabled":false},"headroom":{}},}')
        self.g["check_opencode"]()
        self.assertEqual(len(self.g["fails"]), 1)
        self.assertIn("deshabilitado", self.g["fails"][0][1])

    def test_opencode_checks_both_files(self):
        backend = sorted(self.g["BACKENDS"])[0]
        self.write(".config/opencode/opencode.json", json.dumps({"mcp": {backend: {}}}))
        self.write(".config/opencode/opencode.jsonc", '{"mcp":{"atenea":{},"headroom":{}}}')
        self.g["check_opencode"]()
        self.assertEqual(len(self.g["fails"]), 1)
        self.assertEqual(self.g["fails"][0][0], "installer")

    def test_missing_omp_is_not_green_or_io_error(self):
        self.g["check_omp"]()
        self.assertTrue(self.g["fails"])
        self.assertIn("sin verificar", self.g["notes"][0])

    def test_omp_default_enabled_and_denylist(self):
        cfg = {"mcpServers": {n: {"command": "/bin/echo"} for n in self.g["CORE"]},
               "disabledServers": sorted(self.g["OMP_SUPPRESSED"])}
        self.write(".omp/agent/mcp.json", json.dumps(cfg))
        self.g["check_omp"]()
        self.assertFalse(self.g["fails"])
        cfg["disabledServers"].append("atenea")
        self.write(".omp/agent/mcp.json", json.dumps(cfg))
        self.g["check_omp"]()
        self.assertEqual(len(self.g["fails"]), 1)


if __name__ == "__main__":
    unittest.main()
