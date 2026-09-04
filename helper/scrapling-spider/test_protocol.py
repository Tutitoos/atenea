import json
import subprocess
import sys
from pathlib import Path

helper = Path(__file__).with_name("atenea_spider.py")
hello = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}})
for malformed in ["null", "1", "true", "[]", "{", '{"jsonrpc":"2.0","method":1}']:
    result = subprocess.run([sys.executable, str(helper)], input=malformed+"\n"+hello+"\n", text=True, capture_output=True, timeout=15)
    assert result.returncode == 0, result.stderr
    messages = [json.loads(line) for line in result.stdout.splitlines()]
    assert messages[0]["error"]["code"] == (-32700 if malformed == "{" else -32600)
    assert messages[-1]["id"] == 1 and "result" in messages[-1]
print("PASS: invalid requests do not kill the crawler helper")
