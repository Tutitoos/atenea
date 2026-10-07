"""Keep the legacy Laya runner's wire schema aligned with ATENEA's Go client."""

import importlib.util
import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path


SPEC = importlib.util.spec_from_file_location(
    "evaluate_laya_intents", Path(__file__).with_name("evaluate-laya-intents.py")
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class CriteriaOrderTests(unittest.TestCase):
    def test_wire_options_match_go_encoding_json_order(self):
        seen = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                payload = json.loads(body)
                seen.extend(payload["questions"]["intent"]["criteria"])
                response = json.dumps({
                    "model": "fixture", "routing": {"model": "multilingual"},
                    "answers": {"intent": {"choice": "plan", "answer_confidence": 0.9}},
                }).encode()
                self.send_response(200)
                self.send_header("Content-Length", str(len(response)))
                self.end_headers()
                self.wfile.write(response)

            def log_message(self, *_):
                pass

        server = HTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            intent, confidence, _, route = MODULE.predict(
                f"http://127.0.0.1:{server.server_port}/v1/systemone",
                "Plan the change", 2, "", "multilingual"
            )
        finally:
            server.shutdown()
            thread.join()
            server.server_close()
        self.assertEqual(seen, ["change", "plan", "search", "understand"])
        self.assertEqual((intent, confidence, route), ("plan", 0.9, "multilingual"))


if __name__ == "__main__":
    unittest.main()
