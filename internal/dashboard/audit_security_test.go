package dashboard

import (
	"net/http/httptest"
	"testing"
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
