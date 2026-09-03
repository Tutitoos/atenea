package dashboard

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/observability"
)

func testProvider(hub *observability.Hub) Provider {
	return Provider{
		Snapshot: func() (any, error) {
			return struct {
				State string        `json:"state"`
				Every time.Duration `json:"every"`
			}{State: "alive", Every: 1250 * time.Millisecond}, nil
		},
		Events: hub,
	}
}

func TestHandlerHealthIsPublicAndAPIsAreNoStore(t *testing.T) {
	hub := observability.New(4)
	s, err := NewServer(Config{Enabled: true, Listeners: []Listener{{Addr: "127.0.0.1:8788", Mode: "loopback"}}}, testProvider(hub))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	public := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/healthz", nil)
	public.RemoteAddr = "10.0.0.5:10"
	got := httptest.NewRecorder()
	h.ServeHTTP(got, public)
	if got.Code != http.StatusOK || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("health = %d headers=%v body=%s", got.Code, got.Header(), got.Body.String())
	}

	denied := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/snapshot", nil)
	denied.RemoteAddr = "10.0.0.5:10"
	got = httptest.NewRecorder()
	h.ServeHTTP(got, denied)
	if got.Code != http.StatusUnauthorized {
		t.Fatalf("snapshot from non-loopback = %d, want 401", got.Code)
	}

	allowed := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/snapshot", nil)
	allowed.RemoteAddr = "127.0.0.1:10"
	got = httptest.NewRecorder()
	h.ServeHTTP(got, allowed)
	if got.Code != http.StatusOK || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("snapshot = %d headers=%v", got.Code, got.Header())
	}
	if !strings.Contains(got.Body.String(), `"every":1250`) {
		t.Fatalf("duration was not converted to milliseconds: %s", got.Body.String())
	}
}

func TestRunDetailRouteIsReachable(t *testing.T) {
	hub := observability.New(4)
	s, err := NewServer(Config{Enabled: true, Listeners: []Listener{{Addr: "127.0.0.1:8788", Mode: "loopback"}}}, Provider{
		Run:    func(id string) (any, error) { return map[string]any{"id": id}, nil },
		Events: hub,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/runs/r-test", nil)
	req.RemoteAddr = "127.0.0.1:1"
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":"r-test"`) {
		t.Fatalf("run detail = %d body=%s", res.Code, res.Body.String())
	}
}

func TestSessionAndOverviewRoutesUseVersionedAPI(t *testing.T) {
	hub := observability.New(4)
	s, err := NewServer(Config{Enabled: true, Listeners: []Listener{{Addr: "127.0.0.1:8788", Mode: "loopback"}}}, Provider{
		Overview: func(Query) (any, error) { return map[string]any{"stats": map[string]any{"tokens": 3}}, nil },
		Sessions: func(Query) (any, error) { return map[string]any{"items": []any{map[string]any{"id": "chat-1"}}}, nil },
		Session: func(id string) (any, error) {
			return map[string]any{"id": id, "graph": map[string]any{"nodes": []any{}}}, nil
		},
		Events: hub,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/overview?range=24h", "/api/v1/sessions", "/api/v1/sessions/chat-1"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788"+path, nil)
		req.RemoteAddr = "127.0.0.1:1"
		res := httptest.NewRecorder()
		s.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s = %d body=%s", path, res.Code, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), `"data"`) {
			t.Fatalf("%s did not wrap data: %s", path, res.Body.String())
		}
	}
}

func TestEmbeddedDashboardContainsV3ApplicationShell(t *testing.T) {
	hub := observability.New(2)
	s, err := NewServer(Config{Enabled: true, Listeners: []Listener{{Addr: "127.0.0.1:8788", Mode: "loopback"}}}, testProvider(hub))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/", nil)
	req.RemoteAddr = "127.0.0.1:1"
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("index = %d", res.Code)
	}
	for _, marker := range []string{"Atenea", `id="desktop-nav"`, `id="mobile-nav"`, `id="view"`, `type="module"`, "/app.js"} {
		if !strings.Contains(res.Body.String(), marker) {
			t.Fatalf("index missing %q", marker)
		}
	}
	if strings.Contains(res.Body.String(), "extra.css") {
		t.Fatal("legacy extra.css is still linked")
	}
	for path, marker := range map[string]string{"/app.js": "openEvents", "/views.js": "Project Atlas", "/format.js": "Number.isFinite", "/style.css": "prefers-reduced-motion"} {
		assetReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788"+path, nil)
		assetReq.RemoteAddr = "127.0.0.1:1"
		assetRes := httptest.NewRecorder()
		s.Handler().ServeHTTP(assetRes, assetReq)
		if assetRes.Code != http.StatusOK || !strings.Contains(assetRes.Body.String(), marker) {
			t.Fatalf("asset %s = %d, missing %q", path, assetRes.Code, marker)
		}
	}
}

func TestEmbeddedReactDashboardSupportsDeepLinksWithNonceCSP(t *testing.T) {
	s, err := NewServer(Config{Enabled: true, Listeners: []Listener{{Addr: "127.0.0.1:8788", Mode: "loopback"}}}, testProvider(observability.New(2)))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/live", "/sessions/session-1", "/runs/run-1", "/metrics"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788"+path, nil)
		req.RemoteAddr = "127.0.0.1:1"
		res := httptest.NewRecorder()
		s.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "__reactRouterContext") {
			t.Fatalf("deep link %s = %d", path, res.Code)
		}
		csp := res.Header().Get("Content-Security-Policy")
		if strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "'nonce-") {
			t.Fatalf("deep link %s has weak CSP: %s", path, csp)
		}
		for _, tag := range strings.Split(res.Body.String(), "<script")[1:] {
			if end := strings.IndexByte(tag, '>'); end >= 0 && !strings.Contains(tag[:end], " nonce=") {
				t.Fatalf("script without nonce on %s: %s", path, tag[:end])
			}
		}
	}
	unknown := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/not-a-dashboard-route", nil)
	req.RemoteAddr = "127.0.0.1:1"
	s.Handler().ServeHTTP(unknown, req)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown route = %d, want 404", unknown.Code)
	}
	post := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8788/live", nil)
	req.RemoteAddr = "127.0.0.1:1"
	s.Handler().ServeHTTP(post, req)
	if post.Code != http.StatusMethodNotAllowed || strings.Contains(post.Body.String(), "__reactRouterContext") {
		t.Fatalf("POST deep link = %d body=%q, want 405 without SPA fallback", post.Code, post.Body.String())
	}
}

func TestSafeJSONDropsSensitiveProviderFieldsRecursively(t *testing.T) {
	value := map[string]any{
		"state":    "ok",
		"raw":      "provider response",
		"path":     "/Users/private/project",
		"dir":      "/Users/private/backups",
		"endpoint": "http://127.0.0.1:9911",
		"step": map[string]any{
			"result":      map[string]any{"secret": "answer"},
			"payload":     map[string]any{"token": "abc"},
			"discoveries": []string{"file contents"},
		},
	}
	encoded, err := json.Marshal(safeJSON(value))
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	if strings.Contains(body, "provider response") || strings.Contains(body, "answer") || strings.Contains(body, "abc") || strings.Contains(body, "file contents") || strings.Contains(body, "/Users/private") || strings.Contains(body, "9911") {
		t.Fatalf("sensitive dashboard fields survived: %s", body)
	}
}

func TestTailscaleRequiresLoopbackAndIdentityHeader(t *testing.T) {
	hub := observability.New(4)
	s, err := NewServer(Config{Enabled: true, Listeners: []Listener{{Addr: "127.0.0.1:8788", Mode: "tailscale"}}}, testProvider(hub))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for name, tc := range map[string]struct {
		remote string
		login  string
		code   int
	}{
		"missing identity": {remote: "127.0.0.1:1", code: http.StatusUnauthorized},
		"spoofed from LAN": {remote: "192.168.1.4:1", login: "user@example.com", code: http.StatusUnauthorized},
		"serve identity":   {remote: "127.0.0.1:1", login: "user@example.com", code: http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/snapshot", nil)
			req.RemoteAddr = tc.remote
			if tc.login != "" {
				req.Header.Set("Tailscale-User-Login", tc.login)
			}
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			if res.Code != tc.code {
				t.Fatalf("status = %d, want %d", res.Code, tc.code)
			}
		})
	}
}

func TestCollectionRejectsMalformedRangeAndOversizedFilter(t *testing.T) {
	hub := observability.New(2)
	s, err := NewServer(Config{Enabled: true, PageLimit: 10, Listeners: []Listener{{Addr: "127.0.0.1:8788", Mode: "loopback"}}}, Provider{
		Metrics: func(Query) (any, error) { return []string{"ok"}, nil }, Events: hub,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/metrics?limit=11", "/api/v1/metrics?since=nope", "/api/v1/metrics?capability=" + strings.Repeat("x", 257)} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788"+path, nil)
		req.RemoteAddr = "127.0.0.1:1"
		res := httptest.NewRecorder()
		s.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", path, res.Code)
		}
	}
}

func TestLANLoginSetsSecureStrictCookieAndAuthorizes(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "dashboard.token")
	if err := os.WriteFile(tokenPath, []byte("correct-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hub := observability.New(4)
	s, err := NewServer(Config{Enabled: true, Listeners: []Listener{{Addr: "192.168.50.10:8789", Mode: "lan", TokenFile: tokenPath, CertFile: filepath.Join(dir, "cert.pem"), KeyFile: filepath.Join(dir, "key.pem")}}}, testProvider(hub))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	login := httptest.NewRequest(http.MethodPost, "https://192.168.50.10:8789/api/v1/auth/login", strings.NewReader(`{"token":"correct-token"}`))
	login.Host = "192.168.50.10:8789"
	login.RemoteAddr = "192.168.50.20:10"
	login.TLS = &tls.ConnectionState{}
	login.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, login)
	if res.Code != http.StatusOK {
		t.Fatalf("login = %d body=%s", res.Code, res.Body.String())
	}
	cookie := res.Result().Cookies()
	if len(cookie) != 1 || cookie[0].Secure != true || cookie[0].HttpOnly != true || cookie[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie = %+v", cookie)
	}

	request := httptest.NewRequest(http.MethodGet, "https://192.168.50.10:8789/api/v1/snapshot", nil)
	request.Host = "192.168.50.10:8789"
	request.RemoteAddr = "192.168.50.20:10"
	request.TLS = &tls.ConnectionState{}
	request.AddCookie(cookie[0])
	res = httptest.NewRecorder()
	h.ServeHTTP(res, request)
	if res.Code != http.StatusOK {
		t.Fatalf("authorized LAN request = %d body=%s", res.Code, res.Body.String())
	}
	request.TLS = nil
	res = httptest.NewRecorder()
	h.ServeHTTP(res, request)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("plain HTTP LAN request = %d, want 401", res.Code)
	}
}

func TestLANRejectsInsecureTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "dashboard.token")
	if err := os.WriteFile(tokenPath, []byte("token"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewServer(Config{Enabled: true, Listeners: []Listener{{Addr: "192.168.50.10:8789", Mode: "lan", TokenFile: tokenPath, CertFile: filepath.Join(dir, "cert.pem"), KeyFile: filepath.Join(dir, "key.pem")}}}, Provider{})
	if err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("NewServer error = %v, want 0600 refusal", err)
	}
}

func TestEventsSSEReplaysAndResetsExpiredCursor(t *testing.T) {
	t.Run("replay", func(t *testing.T) {
		hub := observability.New(4)
		hub.Publish(observability.Event{Kind: "run.started", RunID: "r1"})
		s, err := NewServer(Config{Enabled: true, Listeners: []Listener{{Addr: "127.0.0.1:8788", Mode: "loopback"}}}, Provider{Events: hub})
		if err != nil {
			t.Fatal(err)
		}
		w := &streamWriter{changed: make(chan struct{}, 8)}
		ctx, cancel := contextWithCancel()
		defer cancel()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/events?after=0", nil).WithContext(ctx)
		req.RemoteAddr = "127.0.0.1:1"
		done := make(chan struct{})
		go func() { s.Handler().ServeHTTP(w, req); close(done) }()
		waitStream(t, w.changed)
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("SSE handler did not stop")
		}
		body := w.String()
		if !strings.Contains(body, "id: 1\n") || !strings.Contains(body, "event: run.started") || !strings.Contains(body, `"run_id":"r1"`) {
			t.Fatalf("SSE replay = %s", body)
		}
	})

	t.Run("reset", func(t *testing.T) {
		hub := observability.New(2)
		hub.Publish(observability.Event{Kind: "one"})
		hub.Publish(observability.Event{Kind: "two"})
		hub.Publish(observability.Event{Kind: "three"})
		hub.Publish(observability.Event{Kind: "four"})
		s, err := NewServer(Config{Enabled: true, Listeners: []Listener{{Addr: "127.0.0.1:8788", Mode: "loopback"}}}, Provider{Events: hub})
		if err != nil {
			t.Fatal(err)
		}
		w := &streamWriter{changed: make(chan struct{}, 8)}
		ctx, cancel := contextWithCancel()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8788/api/v1/events", nil).WithContext(ctx)
		req.RemoteAddr = "127.0.0.1:1"
		req.Header.Set("Last-Event-ID", "1")
		done := make(chan struct{})
		go func() { s.Handler().ServeHTTP(w, req); close(done) }()
		waitStream(t, w.changed)
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("SSE handler did not stop")
		}
		if !strings.Contains(w.String(), "event: reset") || !strings.Contains(w.String(), "cursor_expired") {
			t.Fatalf("SSE reset = %s", w.String())
		}
	})
}

// streamWriter lets the SSE handler run until the request context is closed,
// while still giving the test a race-free signal that at least one flush was
// written.
type streamWriter struct {
	mu      sync.Mutex
	header  http.Header
	buf     bytes.Buffer
	changed chan struct{}
}

func (w *streamWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *streamWriter) WriteHeader(int) {}
func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	_, _ = w.buf.Write(p)
	w.mu.Unlock()
	select {
	case w.changed <- struct{}{}:
	default:
	}
	return len(p), nil
}
func (w *streamWriter) Flush() {}
func (w *streamWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func waitStream(t *testing.T, changed <-chan struct{}) {
	t.Helper()
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("SSE produced no data")
	}
}

// Keep this tiny wrapper local to avoid making production code depend on a
// test-only context helper.
func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
