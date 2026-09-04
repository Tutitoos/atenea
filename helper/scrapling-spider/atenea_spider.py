#!/usr/bin/env python3
"""An MCP server over stdio that crawls one site, for Atenea's web.crawl.

# Why this exists at all

Scrapling's MCP server has thirteen tools and none of them crawls. The Spider
API that does is Python-only, so reaching it means a process of our own -- the
same shape, and for the same reason, as helper/Sources/atenea-desktop-helper:
the logic belongs to somebody else's library, and keeping it out here keeps it
out of the Go coverage profile and out of the Go build.

# Why one host

The destination gate lives in Go. It resolves a hostname and judges the
address, because a name is somebody else's claim about where it points -- and
that check is the whole reason web.fetch is an adapter rather than a
passthrough.

A crawler's frontier lives here, which leaves two ways to gate it. One is to
re-implement the gate in Python, and that is refused: a security control with
two implementations is a security control with two behaviors, and the second
one drifts silently. The other is to make the frontier unable to leave the host
the gate already approved, which is what `allowed_domains` below does.

So a crawl here is single-host by construction. That is a real limit and it is
the honest one: Atenea gates the seed, and nothing this process fetches can be
anywhere else.

# What is not negotiable from the outside

robots_txt_obey is set here and is not an argument. Scrapling defaults it to
False, so it has to be turned on deliberately, and a caller who could turn it
back off per call would make "does Atenea respect robots.txt" a question with
no answer. If it should ever be possible to ignore it, that belongs in
[web] in the settings file, where somebody has to write it down once and can be
asked why.
"""

import json
import sys
from urllib.parse import urlparse

PROTOCOL = "2025-06-18"
DEFAULT_DELAY = 0.5
MAX_PAGES_CEILING = 500


def log(message):
    """Diagnostics go to stderr; stdout is the JSON-RPC channel and nothing else."""
    sys.stderr.write(str(message) + "\n")
    sys.stderr.flush()


def crawl(url, max_pages=25, max_depth=2, selector="", extraction="markdown",
          stealth=False, delay=DEFAULT_DELAY):
    """Walk one site from url and return what it found.

    Bounded twice on purpose. max_depth is what the caller asked for; max_pages
    is the ceiling that makes a mistake in the first one survivable, because a
    depth of three on a site with a calendar is not a small number.
    """
    from scrapling.spiders import CrawlRule, CrawlSpider, LinkExtractor

    seed = urlparse(url)
    if seed.scheme not in ("http", "https") or not seed.hostname:
        raise ValueError("crawl needs an http or https url with a host")
    host = seed.hostname.lower()
    pages = int(max(1, min(max_pages, MAX_PAGES_CEILING)))
    depth_limit = int(max(0, max_depth))

    found = []
    stopped = "the crawl ran out of links"

    class Walk(CrawlSpider):
        name = "atenea-web-crawl"
        start_urls = [url]
        # The frontier cannot leave the host Atenea's gate already approved.
        # See the module docstring: this is the gate, expressed the only way it
        # can be from in here.
        allowed_domains = {host}
        robots_txt_obey = True
        download_delay = float(delay)
        concurrent_requests = 2
        concurrent_requests_per_domain = 2
        autothrottle_enabled = True
        # The library logs at DEBUG by default and its stdout is this
        # process's JSON-RPC channel.
        logging_level = 40

        def rules(self):
            return [CrawlRule(
                link_extractor=LinkExtractor(allow_domains=[host]),
                callback=self.parse,
            )]

        async def parse(self, response):
            nonlocal stopped
            depth = int((getattr(response, "meta", None) or {}).get("depth", 0))
            body = _body(response, selector, extraction)
            found.append({
                "url": str(getattr(response, "url", "") or ""),
                "depth": depth,
                "status": int(getattr(response, "status", 0) or 0),
                "title": _title(response),
                "content": body,
            })
            if len(found) >= pages:
                stopped = "the page budget of %d was reached" % pages
                self.pause()
                return
            if depth >= depth_limit:
                return
            for rule in self.rules():
                for link in rule.link_extractor.extract(response):
                    request = response.follow(link, callback=rule.callback)
                    request.meta = dict(getattr(request, "meta", None) or {})
                    request.meta["depth"] = depth + 1
                    yield request

    if stealth:
        Walk.configure_sessions(stealthy=True)

    result = Walk().start()
    if getattr(result, "paused", False) and "budget" not in stopped:
        stopped = "the crawl was paused"
    return {"pages": found[:pages], "stopped_by": stopped, "host": host}


def _body(response, selector, extraction):
    """The page, or whatever the selector matched, in the requested rendering."""
    try:
        target = response.css(selector) if selector else response
    except Exception:  # a selector the page cannot answer is an empty match
        return ""
    if selector:
        parts = []
        for element in target or []:
            parts.append(_render(element, extraction))
        return "\n\n".join(p for p in parts if p and p.strip())
    return _render(target, extraction)


def _render(node, extraction):
    for attribute in {"markdown": ("markdown",), "html": ("html_content", "body"),
                      "text": ("get_all_text", "text")}.get(extraction, ("text",)):
        value = getattr(node, attribute, None)
        if callable(value):
            try:
                value = value()
            except Exception:
                continue
        if value:
            return str(value)
    return ""


def _title(response):
    try:
        node = response.css_first("title")
    except Exception:
        return ""
    if node is None:
        return ""
    text = getattr(node, "text", "")
    return str(text() if callable(text) else text or "").strip()


TOOLS = [{
    "name": "crawl",
    "description": (
        "Walk one site from a starting url and return the pages found. "
        "Single-host by construction and robots.txt is always obeyed."
    ),
    "inputSchema": {
        "type": "object",
        "required": ["url"],
        "properties": {
            "url": {"type": "string"},
            "max_pages": {"type": "integer", "default": 25},
            "max_depth": {"type": "integer", "default": 2},
            "selector": {"type": "string"},
            "extraction_type": {"type": "string", "enum": ["text", "html", "markdown"]},
            "stealth": {"type": "boolean", "default": False},
        },
    },
}]


def handle(message):
    method = message.get("method")
    if method == "initialize":
        return {
            "protocolVersion": PROTOCOL,
            "serverInfo": {"name": "atenea-scrapling-spider", "version": _version()},
            "capabilities": {"tools": {}},
        }
    if method == "tools/list":
        return {"tools": TOOLS}
    if method == "tools/call":
        params = message.get("params") or {}
        if params.get("name") != "crawl":
            return {"content": [{"type": "text", "text": "no tool named %r" % params.get("name")}],
                    "isError": True}
        args = params.get("arguments") or {}
        try:
            answer = crawl(
                url=args.get("url", ""),
                max_pages=args.get("max_pages", 25),
                max_depth=args.get("max_depth", 2),
                selector=args.get("selector", "") or "",
                extraction=args.get("extraction_type", "markdown") or "markdown",
                stealth=bool(args.get("stealth", False)),
            )
        except Exception as failure:  # a dead crawl is an answer, not a crash
            return {"content": [{"type": "text", "text": "%s: %s" % (type(failure).__name__, failure)}],
                    "isError": True}
        return {"content": [{"type": "text", "text": json.dumps(answer)}],
                "structuredContent": answer}
    return {}


def _version():
    try:
        import scrapling
        return str(getattr(scrapling, "__version__", "0"))
    except Exception:
        return "0"


def valid_request(message):
    if not isinstance(message, dict) or message.get("jsonrpc") != "2.0" or not isinstance(message.get("method"), str):
        return False
    if "id" in message:
        request_id = message["id"]
        if isinstance(request_id, bool) or not isinstance(request_id, (str, int, float, type(None))):
            return False
    return "params" not in message or isinstance(message["params"], dict)


def write_error(code, message):
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": None, "error": {"code": code, "message": message}}) + "\n")
    sys.stdout.flush()


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            message = json.loads(line)
        except ValueError:
            try:
                write_error(-32700, "Parse error")
            except (BrokenPipeError, ValueError):
                break
            continue
        if not valid_request(message):
            try:
                write_error(-32600, "Invalid Request")
            except (BrokenPipeError, ValueError):
                break
            continue
        if "id" not in message:
            continue  # a notification answers nobody
        try:
            result = handle(message)
        except Exception as failure:
            log("handler failed: %r" % (failure,))
            result = {}
        try:
            sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": message["id"],
                                         "result": result}) + "\n")
            sys.stdout.flush()
        except (BrokenPipeError, ValueError):
            # A readiness probe hangs up after the handshake, which is not a
            # crash and must not be reported as one.
            break


if __name__ == "__main__":
    main()
