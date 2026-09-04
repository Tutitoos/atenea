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
