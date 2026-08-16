"""Loopback recorder: answers as the Anthropic API and keeps every request body.

Nothing leaves the machine and no key is used, so every reading here is free.
The point is the SECOND request: the reported ~36,300-token block is said to
arrive with the first tool result, and a recorder is the only way to see what
the client actually sends rather than what it is billed for.

Turn 1 is answered with a tool_use so the CLI runs a tool and comes back.
Turn 2 is answered with plain text so it stops.
"""

import json
import pathlib
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

OUT = pathlib.Path("/tmp/tp5/bodies")
OUT.mkdir(parents=True, exist_ok=True)
seen = {"n": 0}


def sse(events):
    return "".join(f"event: {name}\ndata: {json.dumps(payload)}\n\n"
                   for name, payload in events)


def tool_use_turn():
    return [
        ("message_start", {"type": "message_start", "message": {
            "id": "msg_rec1", "type": "message", "role": "assistant",
            "model": "rec", "content": [], "stop_reason": None,
            "usage": {"input_tokens": 1, "output_tokens": 1,
                      "cache_creation_input_tokens": 0,
                      "cache_read_input_tokens": 0}}}),
        ("content_block_start", {"type": "content_block_start", "index": 0,
                                 "content_block": {"type": "tool_use", "id": "toolu_rec1",
                                                   "name": "Glob", "input": {}}}),
        ("content_block_delta", {"type": "content_block_delta", "index": 0,
                                 "delta": {"type": "input_json_delta",
                                           "partial_json": '{"pattern":"*.md"}'}}),
        ("content_block_stop", {"type": "content_block_stop", "index": 0}),
        ("message_delta", {"type": "message_delta",
                           "delta": {"stop_reason": "tool_use", "stop_sequence": None},
                           "usage": {"output_tokens": 8}}),
        ("message_stop", {"type": "message_stop"}),
    ]


def text_turn(text):
    return [
        ("message_start", {"type": "message_start", "message": {
            "id": "msg_rec2", "type": "message", "role": "assistant",
            "model": "rec", "content": [], "stop_reason": None,
            "usage": {"input_tokens": 1, "output_tokens": 1,
                      "cache_creation_input_tokens": 0,
                      "cache_read_input_tokens": 0}}}),
        ("content_block_start", {"type": "content_block_start", "index": 0,
                                 "content_block": {"type": "text", "text": ""}}),
        ("content_block_delta", {"type": "content_block_delta", "index": 0,
                                 "delta": {"type": "text_delta", "text": text}}),
        ("content_block_stop", {"type": "content_block_stop", "index": 0}),
        ("message_delta", {"type": "message_delta",
                           "delta": {"stop_reason": "end_turn", "stop_sequence": None},
                           "usage": {"output_tokens": 4}}),
        ("message_stop", {"type": "message_stop"}),
    ]


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        pass

    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        seen["n"] += 1
        n = seen["n"]
        (OUT / f"req{n}.json").write_bytes(raw)
        # Redacted before it touches disk. This recorder is pointed at by a real
        # client, and the next reading wants no auth source, so a live bearer
        # would otherwise land in /tmp for the sake of a header nobody is
        # measuring.
        secret = {"authorization", "x-api-key", "anthropic-api-key", "cookie"}
        kept = "".join(f"{k}: {'[redacted]' if k.lower() in secret else v}\n"
                       for k, v in self.headers.items())
        (OUT / f"req{n}.headers").write_text(kept)
        body = json.loads(raw or b"{}")

        # Decide from the request, not from a counter. The CLI opens every turn
        # with a title-generation call that carries NO tools, and answering that
        # one with a tool_use for a tool it was never given is what makes it
        # hang up. A tool_use is only valid where tools were offered, and only
        # once -- after the tool result comes back, answer in text so it stops.
        tools = body.get("tools") or []
        served = json.dumps(body.get("messages") or [])
        if tools and "tool_result" not in served:
            events = tool_use_turn()
        else:
            events = text_turn("done")
        payload = sse(events).encode()
        try:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
        except (BrokenPipeError, ConnectionResetError):
            # The client gave up on this one. The body is already on disk, which
            # is the whole reading; a dropped reply loses nothing measured.
            print(f"req{n}: client hung up after sending {len(raw)} bytes",
                  file=sys.stderr, flush=True)
            return
        print(f"recorded req{n}: {len(raw)} bytes, stream={body.get('stream')}",
              file=sys.stderr, flush=True)


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", 8791), Handler).serve_forever()
