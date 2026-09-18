package dashboard

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDefaultCollectionLimitUsesConfiguredPageLimit(t *testing.T) {
	for _, limit := range []int{1, 25, 100} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			var observed int
			s, err := NewServer(Config{Enabled: true, PageLimit: limit, Listeners: []Listener{{Addr: "127.0.0.1:8788", Mode: "loopback"}}}, Provider{Sessions: func(q Query) (any, error) { observed = q.Limit; return map[string]any{"items": []any{}}, nil }})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/sessions", nil)
			req.RemoteAddr = "127.0.0.1:12345"
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("default request fails for page_limit=%d: HTTP %d %s", limit, response.Code, response.Body.String())
			}
			if observed != limit {
				t.Fatalf("provider received limit %d, want %d", observed, limit)
			}
			invalid := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/sessions?limit=101", nil)
			invalid.RemoteAddr = "127.0.0.1:12345"
			response = httptest.NewRecorder()
			s.Handler().ServeHTTP(response, invalid)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("explicit limit above maximum returned HTTP %d, want 400", response.Code)
			}
		})
	}
}
