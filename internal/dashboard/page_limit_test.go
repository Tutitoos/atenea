package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDefaultCollectionLimitUsesConfiguredPageLimit(t *testing.T) {
	var observed int
	s, err := NewServer(Config{Enabled: true, PageLimit: 25, Listeners: []Listener{{Addr: "127.0.0.1:8788", Mode: "loopback"}}}, Provider{Sessions: func(q Query) (any, error) { observed = q.Limit; return map[string]any{"items": []any{}}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/sessions", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("default request fails for valid page_limit=25: HTTP %d %s", response.Code, response.Body.String())
	}
	if observed != 25 {
		t.Fatalf("provider received limit %d, want 25", observed)
	}
	invalid := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/sessions?limit=100", nil)
	invalid.RemoteAddr = "127.0.0.1:12345"
	response = httptest.NewRecorder()
	s.Handler().ServeHTTP(response, invalid)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("explicit limit above maximum returned HTTP %d, want 400", response.Code)
	}
}
