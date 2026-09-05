"""Regression tests for file-only MCP auditing (no clients are started)."""
import json
import sys
import time
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
        self.env = patch.dict("os.environ", {"PI_CODING_AGENT_DIR": "", "XDG_CONFIG_HOME": "", "ATENEA_CONFIG": ""})
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


    def test_policy_and_missing_disk_do_not_infer_runtime(self):
        self.write(".config/atenea/atenea.toml", "")
        self.g["audit_client_declarations"]("codex")
        policy = self.g["effective_policy"]("codex")
        self.assertEqual(policy["profile"], "chatgpt")
        self.assertEqual(policy["received_tools"], "unobserved")
        self.assertFalse(self.g["fails"])

    def test_hybrid_excludes_backends_and_respects_allowlist(self):
        self.write(".config/atenea/atenea.toml", '''[[desktop_profile]]
name="shared"
mcp_mode="hybrid"
direct_mcp=["*"]
[[implementation]]
provider="engine"
[[mcp_server]]
id="engine"
expose="on"
[[mcp_server]]
id="direct"
expose="on"
[[mcp_server]]
id="hidden"
expose="off"
''')
        self.assertEqual(self.g["effective_policy"]("omp")["expected_wrapper_servers"], ["atenea", "direct"])

    def test_discovery_records_version_schema_and_all_pages(self):
        script = self.write("server.py", '''import json,sys
for line in sys.stdin:
    req=json.loads(line)
    if "id" not in req: continue
    if req["method"]=="initialize": result={"serverInfo":{"name":"fixture","version":"1.2"}}
    elif req.get("params",{}).get("cursor")=="next": result={"tools":[{"name":"two","inputSchema":{"type":"object"}}]}
    else: result={"tools":[{"name":"one","inputSchema":{"type":"object"}}],"nextCursor":"next"}
    print(json.dumps({"id":req["id"],"result":result}),flush=True)
''')
        out = self.g["handshake"]("test",sys.executable,[script],want_metadata=True, timeout=2)
        self.assertEqual(out["server"]["version"], "1.2")
        self.assertEqual(out["tools"], ["one", "two"])
        self.assertEqual(len(out["schema_hash"]),64)

    def test_discovery_timeout_is_bounded(self):
        started=time.monotonic()
        self.assertIsNone(self.g["handshake"]("test",sys.executable,["-c","import time;time.sleep(10)"],timeout=0.1))
        self.assertLess(time.monotonic()-started,3)
        self.assertTrue(self.g["fails"])


if __name__ == "__main__":
    unittest.main()
