#!/usr/bin/env bash
set -euo pipefail

# The Spider helper is intentionally a Python process rather than part of the
# Go binary. This gate checks both halves that can fail independently: the
# source still parses, and the process still speaks enough MCP for the
# supervisor to attach it before a real crawl is attempted.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
helper="$root/helper/scrapling-spider/atenea_spider.py"

python3 - "$helper" <<'PY'
import json
import subprocess
import sys

path = sys.argv[1]
with open(path, "rb") as source:
    compile(source.read(), path, "exec")

proc = subprocess.Popen(
    [sys.executable, path],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
)

def request(message):
    proc.stdin.write(json.dumps(message) + "\n")
    proc.stdin.flush()
    line = proc.stdout.readline()
    if not line:
        raise RuntimeError("Spider helper closed stdout during %s" % message["method"])
    answer = json.loads(line)
    if answer.get("id") != message["id"] or "result" not in answer:
        raise RuntimeError("invalid MCP answer: %r" % answer)
    return answer["result"]

try:
    initialize = request({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {"protocolVersion": "2025-06-18", "capabilities": {}},
    })
    if initialize.get("serverInfo", {}).get("name") != "atenea-scrapling-spider":
        raise RuntimeError("unexpected initialize response: %r" % initialize)
    listed = request({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
    names = {tool.get("name") for tool in listed.get("tools", [])}
    if names != {"crawl"}:
        raise RuntimeError("unexpected tool surface: %r" % sorted(names))
finally:
    proc.terminate()
    try:
        proc.wait(timeout=2)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()
print("Scrapling Spider helper parsed and answered initialize/tools/list")
PY

python3 "$root/helper/scrapling-spider/test_protocol.py"
