package scrapling

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"testing"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
)

// errNoServer stands in for a far side nothing here intends to reach: the
// gate refuses or allows before any session is asked for, so a fuzz case that
// gets past it must fail on the dial rather than on a nil pointer.
var errNoServer = errors.New("no server in a fuzz run")

// A fetched page is the purest case of the category the CI fuzz gate exists
// for: bytes from somebody else's machine, chosen by somebody else, arriving
// through a server this process does not control. A site can serve any bytes
// it likes, including invalid UTF-8, a JSON object with the right keys and the
// wrong types, or a body several megabytes long with no structure at all.
//
// What is asserted is the weakest useful thing and the right one: it does not
// panic. A malformed answer is a provider failure this adapter already sorts
// into a bin; a panic takes the service down with every other chat attached.
func FuzzFetchAnswerNeverPanics(f *testing.F) {
	f.Add(`{"status":200,"url":"https://example.com/","title":"Example","text":"hello"}`)
	f.Add(`{"status_code":403,"html":"<div id=\"cf-browser-verification\"></div>"}`)
	f.Add(`{"status":"two hundred","text":42}`)
	f.Add(`{"final_url":"http://10.1.2.3/admin","body":"internal"}`)
	f.Add(`<html>just the page</html>`)
	f.Add(`{}`)
	f.Add(``)
	f.Add(`{"text":"\ud800"}`)
	f.Add(`[[[[[[[[[[`)

	f.Fuzz(func(t *testing.T, raw string) {
		out, err := answerOf("make_request", raw)
		if err != nil {
			return
		}
		// Every reader of an answer runs, because a shape that decodes and
		// then panics on the way out is the same outage as one that panics
		// while decoding.
		for _, format := range []string{"text", "markdown", "html", "", "yaml"} {
			_ = out.pick(format)
		}
		_ = blocked(out.status, out.content)
		// The body is what the caller would receive, so it must round-trip
		// through the encoder the transport uses without exploding on a lone
		// surrogate or a control byte.
		if _, err := json.Marshal(map[string]any{"content": out.pick("text")}); err != nil {
			return
		}
	})
}

// The gate parses hostnames and CIDR text that ultimately came from a settings
// file, and a URL that came from a model. None of the three is trusted input.
func FuzzTheGateNeverPanics(f *testing.F) {
	f.Add("https://example.com/", "127.0.0.0/8")
	f.Add("http://[::1]:80/", "::1/128")
	f.Add("file:///etc/passwd", "*.lan")
	f.Add("http://%zz/", "localhost")
	f.Add("", "")
	f.Add("http://a.b.c.d.e.f/", "0.0.0.0/0")
	f.Add("https://\x00/", "10.0.0.0/8")

	f.Fuzz(func(t *testing.T, rawURL, denial string) {
		runner, err := New(Options{
			Session: func(ctx context.Context) (*mcpstdio.Session, error) { return nil, errNoServer },
			Denied:  []string{denial},
			Resolve: func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
			},
		})
		if err != nil {
			return
		}
		_ = runner.mayReach(context.Background(), rawURL)
	})
}
