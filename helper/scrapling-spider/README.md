# The crawl helper

One MCP server over stdio, in Python, that walks a site for `web.crawl`.

## Why it exists

Scrapling's own MCP server has thirteen tools and none of them crawls. The
Spider API that does is Python-only, so reaching it means a process of our own.
It is the same shape as `../Sources/atenea-desktop-helper` and for the same
reason: the logic belongs to somebody else's library, and keeping it out here
keeps it out of the Go build and out of the Go coverage profile.

The difference from that one is that this is not compiled. There is nothing to
build — it is a script run by whichever interpreter Scrapling is installed on.

## Running it

It needs `scrapling` importable, and nothing else this repository does not
already assume:

```sh
uv tool install --python 3.12 "scrapling[fetchers,ai]"
scrapling install     # the browsers, for the stealth level
```

`uv tool` keeps it on its own interpreter rather than the system one, which
matters here: Scrapling needs Python 3.10+ and macOS ships 3.9. Point the
settings file at that interpreter and this script:

```toml
[orchestrator.scrapling.spider]
command = "~/.local/share/uv/tools/scrapling/bin/python"
args = ["/absolute/path/to/helper/scrapling-spider/atenea_spider.py"]
instance = "shared"
lifecycle = "on_demand"
ready_timeout = "30s"
```

Omit the block and `web.crawl` is simply not served — the adapter does not
claim implementations it has no far side for.

## Versioning

There is no version of its own and there should not be. What it wraps is
Scrapling's Spider API, so the version that matters is Scrapling's, and that is
what it reports on the MCP handshake — `serverInfo.version` is read from the
installed package rather than written down here. Atenea files every measurement
under that string, so upgrading Scrapling starts a fresh baseline instead of
averaging itself into the numbers that came before.

The tool surface is one tool, `crawl`, and its arguments are built by
`internal/adapter/scrapling`. Nothing else calls it.

## Two things that are not arguments

**`robots.txt` is always obeyed.** Scrapling defaults `robots_txt_obey` to
`False`, so it is turned on here deliberately. A caller who could turn it off
per call would make "does Atenea respect robots.txt" a question with no answer.
If it should ever be possible, that belongs in `[web]` in the settings file,
where somebody writes it down once and can be asked why.

**A crawl cannot leave the host it started on.** Atenea's destination gate
resolves a hostname and judges the address, and it lives in Go. A crawler's
frontier is discovered as it goes, and it is discovered here — so gating it
would mean either a round trip per link or a second copy of the gate in this
file. The second is refused: a security control with two implementations has
two behaviors, and the one nobody is looking at is the one that drifts.

So `allowed_domains` is pinned to the seed's host and the link extractor to the
same. Atenea gates the seed, and nothing this process fetches can be anywhere
else. That is a real limit on the capability and it is the honest way to hold
the line.
