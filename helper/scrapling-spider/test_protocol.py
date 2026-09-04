import json
import subprocess
import sys
from pathlib import Path

helper = Path(__file__).with_name("atenea_spider.py")
hello = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}})
cases = [
    ("{", -32700),
    ('{"jsonrpc":', -32700),
    ("null", -32600),
    ("1", -32600),
    ("true", -32600),
    ("[]", -32600),
    ('{"jsonrpc":"2.0","method":1}', -32600),
    ('{"jsonrpc":"2.0","id":{},"method":"initialize","params":{}}', -32600),
    ('{"jsonrpc":"2.0","id":true,"method":"initialize","params":{}}', -32600),
    ('{"jsonrpc":"2.0","id":NaN,"method":"initialize","params":{}}', -32600),
    ('{"jsonrpc":"2.0","id":Infinity,"method":"initialize","params":{}}', -32600),
    ('{"jsonrpc":"2.0","id":-Infinity,"method":"initialize","params":{}}', -32600),
    ('{"jsonrpc":"2.0","id":2,"method":"initialize","params":1}', -32600),
]
for request, code in cases:
    result = subprocess.run([sys.executable, str(helper)], input=request+"\n"+hello+"\n", text=True, capture_output=True, timeout=15)
    assert result.returncode == 0, result.stderr
    messages = [json.loads(line) for line in result.stdout.splitlines()]
    assert messages[0]["error"]["code"] == code, (request, messages)
    assert messages[0]["id"] is None
    assert messages[-1]["id"] == 1 and "result" in messages[-1]

finite = json.dumps({"jsonrpc": "2.0", "id": 1.5, "method": "initialize", "params": {}})
result = subprocess.run([sys.executable, str(helper)], input=finite+"\n", text=True, capture_output=True, timeout=15)
messages = [json.loads(line) for line in result.stdout.splitlines()]
assert result.returncode == 0 and messages[0]["id"] == 1.5 and "result" in messages[0], (result.stderr, messages)
print("PASS: parse errors and invalid requests remain distinct and do not kill the crawler helper")
