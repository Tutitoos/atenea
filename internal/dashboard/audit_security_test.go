package dashboard

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestUntrustedHostIsRejected rejects external authorities despite forwarded local headers.
func TestUntrustedHostIsRejected(t *testing.T) {
	s, e := NewServer(Config{Enabled: true, Listeners: []Listener{{Addr: "127.0.0.1:7779", Mode: "loopback"}}}, Provider{Snapshot: func() (any, error) { return map[string]string{"data": "private"}, nil }})
	if e != nil {
		t.Fatal(e)
	}
	for _, host := range []string{"attacker.example:7779", "127.0.0.1.attacker.example", "localhost.attacker.example"} {
		req := httptest.NewRequest("GET", "http://"+host+"/api/v1/snapshot", nil)
		req.RemoteAddr = "127.0.0.1:45678"
		req.Header.Set("X-Forwarded-Host", "localhost")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != 403 {
			t.Fatal(host, w.Code)
		}
	}
}

func TestTailscaleDNSHostIsAllowedOnlyForTailscaleListener(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
		host string
		want bool
	}{
		{name: "tailscale hostname", mode: "tailscale", host: "macbook-air.tail1234.ts.net", want: true},
		{name: "tailscale hostname and port", mode: "tailscale", host: "macbook-air.tail1234.ts.net:8443", want: true},
		{name: "loopback mode", mode: "loopback", host: "macbook-air.tail1234.ts.net"},
		{name: "suffix trick", mode: "tailscale", host: "macbook-air.tail1234.ts.net.attacker.example"},
		{name: "empty node name", mode: "tailscale", host: "ts.net"},
		{name: "empty label", mode: "tailscale", host: "macbook-air..tail1234.ts.net"},
		{name: "invalid label", mode: "tailscale", host: "-macbook.tail1234.ts.net"},
		{name: "arbitrary external host", mode: "tailscale", host: "attacker.example"},
		{name: "tailscale IPv4", mode: "tailscale", host: "100.78.253.91:4444", want: true},
		{name: "LAN IPv4", mode: "tailscale", host: "192.168.1.137:4444"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: Config{Listeners: []Listener{{Addr: "127.0.0.1:8788", Mode: tc.mode}}}}
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/", nil)
			req.Host = tc.host
			if got := s.allowedHost(req); got != tc.want {
				t.Fatalf("allowedHost(%q) = %t, want %t", tc.host, got, tc.want)
			}
		})
	}
}

func TestWildcardTailscaleListenerAuthorizesOnlyTailnetPeers(t *testing.T) {
	s := &Server{cfg: Config{Listeners: []Listener{{Addr: "0.0.0.0:4444", Mode: "tailscale"}}}}
	for _, tc := range []struct {
		remote string
		want   bool
	}{
		{remote: "100.78.253.91:50000", want: true},
		{remote: "[fd7a:115c:a1e0::1234]:50000", want: true},
		{remote: "192.168.1.20:50000"},
		{remote: "127.0.0.1:50000"},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://100.78.253.91:4444/api/v1/snapshot", nil)
		req.RemoteAddr = tc.remote
		if got := s.authorized(req); got != tc.want {
			t.Errorf("authorized(%q) = %t, want %t", tc.remote, got, tc.want)
		}
	}
}

// TestLANListenerUsesConnection preserves LAN authorization when listener ports coincide.
func TestLANListenerUsesConnection(t *testing.T) {
	s := &Server{cfg: Config{Listeners: []Listener{{Addr: "127.0.0.1:7779", Mode: "loopback"}, {Addr: "192.168.1.20:7779", Mode: "lan"}}}, authSessions: map[string]time.Time{"valid": time.Now().Add(time.Hour)}}
	req := httptest.NewRequest("GET", "https://192.168.1.20:7779/api/v1/snapshot", nil)
	req.RemoteAddr = "192.168.1.30:12345"
	req.TLS = &tls.ConnectionState{}
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("192.168.1.20"), Port: 7779}))
	req.AddCookie(&http.Cookie{Name: "atenea_dashboard", Value: "valid"})
	if !s.allowedHost(req) {
		t.Fatal("fixture host not allowed")
	}
	if !s.authorized(req) {
		t.Fatalf("valid LAN session rejected: selected %s listener", s.listenerForRequest(req).Mode)
	}
}

// TestListenerIgnoresHostAndFailsClosedWhenAmbiguous checks listener identity cannot be selected by Host.
func TestListenerIgnoresHostAndFailsClosedWhenAmbiguous(t *testing.T) {
	s := &Server{cfg: Config{Listeners: []Listener{{Addr: "127.0.0.1:7779", Mode: "loopback"}, {Addr: "192.168.1.20:7780", Mode: "lan"}}}}
	for _, listener := range s.cfg.Listeners {
		req := httptest.NewRequest("GET", "http://localhost:9999/", nil)
		req = req.WithContext(context.WithValue(req.Context(), listenerContextKey{}, listener))
		if got := s.listenerForRequest(req); got != listener {
			t.Fatal(got)
		}
	}
	req := httptest.NewRequest("GET", "http://localhost:7779/", nil)
	if got := s.listenerForRequest(req); got.Mode != "" {
		t.Fatal("ambiguous request authorized", got)
	}
}
