package mcpcompat

import "testing"

func TestLegacyHTTPFallbackRequiresExplicitIncompatibility(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "json method missing", status: 400, body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601}}`, want: true},
		{name: "plain 400", status: 400, body: "bad request", want: true},
		{name: "404 discover unsupported", status: 404, body: "server/discover is not supported", want: true},
		{name: "405 method not allowed", status: 405, body: "method not allowed: server/discover", want: true},
		{name: "route 404", status: 404, body: "route not found", want: true},
		{name: "empty 405", status: 405, body: "", want: true},
		{name: "modern invalid params", status: 400, body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid metadata"}}`, want: false},
		{name: "modern recognized error", status: 400, body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32602}}`, want: false},
		{name: "auth", status: 401, body: "method not found", want: false},
		{name: "auth with method missing body", status: 401, body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601}}`, want: false},
		{name: "forbidden with method missing body", status: 403, body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601}}`, want: false},
		{name: "server failure", status: 500, body: "method not found", want: false},
		{name: "server failure with method missing body", status: 500, body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601}}`, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := LegacyHTTPFallback(test.status, test.body); got != test.want {
				t.Fatalf("LegacyHTTPFallback(%d, %q) = %v, want %v", test.status, test.body, got, test.want)
			}
		})
	}
}
