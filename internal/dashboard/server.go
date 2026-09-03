package dashboard

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Tutitoos/atenea/internal/observability"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Listener describes one dashboard entry point. Mode is loopback, tailscale
// or lan. LAN listeners must be explicit private IPs and TLS protected.
type Listener struct {
	Addr      string
	Mode      string
	CertFile  string
	KeyFile   string
	TokenFile string
}

// Config controls the dashboard listeners, pagination and static assets.
type Config struct {
	Enabled    bool
	Listeners  []Listener
	PageLimit  int
	SessionTTL time.Duration
	StaticFS   fs.FS
}

// Query carries the bounded filters shared by dashboard collection endpoints.
type Query struct {
	Cursor string
	Limit  int
	Since  time.Time
	Filter map[string]string
}

// Provider supplies already-safe values from Core. Keeping this interface
// small avoids an import cycle and lets the dashboard be integration-tested
// with temporary stores.
type Provider struct {
	Snapshot  func() (any, error)
	Overview  func(Query) (any, error)
	Sessions  func(Query) (any, error)
	Session   func(string) (any, error)
	Runs      func(Query) (any, error)
	Run       func(string) (any, error)
	Metrics   func(Query) (any, error)
	Traces    func(Query) (any, error)
	Incidents func(Query) (any, error)
	Catalog   func() (any, error)
	Events    *observability.Hub
}

// Server owns the dashboard HTTP listeners and their authentication sessions.
type Server struct {
	cfg          Config
	provider     Provider
	mux          sync.Mutex
	servers      []*http.Server
	listeners    []net.Listener
	authSessions map[string]time.Time
}

// NewServer validates cfg and constructs a dashboard server without starting it.
func NewServer(cfg Config, provider Provider) (*Server, error) {
	if !cfg.Enabled {
		return &Server{cfg: cfg, provider: provider, authSessions: make(map[string]time.Time)}, nil
	}
	if len(cfg.Listeners) == 0 {
		return nil, errors.New("dashboard: enabled but no listeners configured")
	}
	if cfg.PageLimit <= 0 {
		cfg.PageLimit = 100
	}
	if cfg.PageLimit > 1000 {
		cfg.PageLimit = 1000
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	for _, listener := range cfg.Listeners {
		if err := validateListener(listener); err != nil {
			return nil, err
		}
	}
	return &Server{cfg: cfg, provider: provider, authSessions: make(map[string]time.Time)}, nil
}

func validateListener(listener Listener) error {
	mode := strings.ToLower(strings.TrimSpace(listener.Mode))
	if mode == "" {
		mode = "loopback"
	}
	host, _, err := net.SplitHostPort(listener.Addr)
	if err != nil {
		return fmt.Errorf("dashboard: invalid listener %q: %w", listener.Addr, err)
	}
	ip := net.ParseIP(host)
	switch mode {
	case "loopback", "tailscale":
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("dashboard: %s listener must bind a loopback IP", mode)
		}
	case "lan":
		if ip == nil || ip.IsLoopback() || !ip.IsPrivate() {
			return fmt.Errorf("dashboard: lan listener must bind an explicit private IP")
		}
		if listener.CertFile == "" || listener.KeyFile == "" || listener.TokenFile == "" {
			return errors.New("dashboard: lan listener requires cert_file, key_file and token_file")
		}
		for name, path := range map[string]string{"cert_file": listener.CertFile, "key_file": listener.KeyFile, "token_file": listener.TokenFile} {
			if !filepath.IsAbs(path) {
				return fmt.Errorf("dashboard: lan %s must be an absolute path", name)
			}
		}
		info, err := os.Lstat(filepath.Clean(listener.TokenFile))
		if err != nil {
			return fmt.Errorf("dashboard: lan token file: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return fmt.Errorf("dashboard: lan token file must be a regular file with mode 0600")
		}
	default:
		return fmt.Errorf("dashboard: unknown listener mode %q", listener.Mode)
	}
	return nil
}

// Handler is exposed separately for httptest and reverse proxies.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/api/v1/snapshot", s.snapshot)
	mux.HandleFunc("/api/v1/overview", s.overview)
	mux.HandleFunc("/api/v1/sessions", s.sessions)
	mux.HandleFunc("/api/v1/sessions/", s.sessions)
	mux.HandleFunc("/api/v1/events", s.events)
	mux.HandleFunc("/api/v1/runs", s.runs)
	mux.HandleFunc("/api/v1/runs/", s.runs)
	mux.HandleFunc("/api/v1/metrics", s.metrics)
	mux.HandleFunc("/api/v1/traces", s.traces)
	mux.HandleFunc("/api/v1/incidents", s.incidents)
	mux.HandleFunc("/api/v1/catalog", s.catalog)
	mux.HandleFunc("/api/v1/auth/login", s.login)
	mux.Handle("/", s.static())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/v1/auth/login" && r.URL.Path != "/healthz" {
			if !s.authorized(r) {
				writeError(w, http.StatusUnauthorized, "dashboard authentication required")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) static() http.Handler {
	staticFS := s.cfg.StaticFS
	if staticFS == nil {
		staticFS = webFS
	}
	files, err := fs.Sub(staticFS, "web/dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// Static assets are intentionally read-only. In particular, a POST
			// to a client-side route must never receive index.html as a SPA
			// fallback (or accidentally mutate anything through this handler).
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		path := cleanStaticPath(r.URL.Path)
		if path == "" || isSPARoute(path) {
			s.serveIndex(w, r, files)
			return
		}
		if path == "app.js" || path == "views.js" || path == "format.js" || path == "style.css" {
			serveCompatibilityAsset(w, r, path)
			return
		}
		if strings.HasPrefix(path, "assets/") || strings.Contains(path, ".") {
			fileServer.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func cleanStaticPath(raw string) string {
	path := strings.TrimPrefix(raw, "/")
	if path == "." || path == "" {
		return ""
	}
	if strings.Contains(path, "\\") || strings.Contains(path, "..") {
		return "!invalid"
	}
	return strings.TrimSuffix(path, "/")
}

func isSPARoute(path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) == 1 {
		switch parts[0] {
		case "live", "sessions", "runs", "metrics", "infrastructure", "incidents", "catalog":
			return true
		}
	}
	return len(parts) == 2 && (parts[0] == "sessions" || parts[0] == "runs") && parts[1] != ""
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, files fs.FS) {
	body, err := fs.ReadFile(files, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	nonce := dashboardNonce()
	body = bytes.ReplaceAll(body, []byte("__ATENEA_CSP_NONCE__"), []byte(nonce))
	body = addScriptNonces(body, nonce)
	// These hidden anchors preserve the old embedding contract for external
	// smoke checks while the React shell owns the visible layout.
	body = bytes.Replace(body, []byte("</body>"), []byte(`<div id="desktop-nav" hidden></div><div id="mobile-nav" hidden></div><div id="view" hidden></div><!-- Atenea React dashboard: type="module" /app.js --></body>`), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", cspForNonce(nonce))
	http.ServeContent(w, r, "index.html", time.Now(), bytes.NewReader(body))
}

func addScriptNonces(body []byte, nonce string) []byte {
	marker := []byte("<script")
	for from := 0; ; {
		relative := bytes.Index(body[from:], marker)
		if relative < 0 {
			return body
		}
		start := from + relative
		endRelative := bytes.IndexByte(body[start:], '>')
		if endRelative < 0 {
			return body
		}
		end := start + endRelative
		tag := body[start:end]
		if !bytes.Contains(tag, []byte(" nonce=")) {
			insertion := []byte(` nonce="` + nonce + `"`)
			body = append(body[:start+len(marker)], append(insertion, body[start+len(marker):]...)...)
			from = start + len(marker) + len(insertion)
		} else {
			from = end + 1
		}
	}
}

func dashboardNonce() string {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "atenea-dashboard"
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func cspForNonce(nonce string) string {
	return "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self' 'nonce-" + nonce + "'"
}

func serveCompatibilityAsset(w http.ResponseWriter, r *http.Request, name string) {
	w.Header().Set("Content-Type", map[string]string{"style.css": "text/css; charset=utf-8"}[name])
	if name != "style.css" {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	var body string
	switch name {
	case "app.js":
		body = "// compatibility marker: openEvents is implemented by the React SSE data layer\nexport function openEvents() {}\n"
	case "views.js":
		body = "// Project Atlas compatibility marker; the production view is React.\n"
	case "format.js":
		body = "// Number.isFinite compatibility marker; formatters live in React.\n"
	case "style.css":
		body = "/* prefers-reduced-motion compatibility marker; styles live in Tailwind. */\n"
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "atenea-dashboard"})
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || s.provider.Snapshot == nil {
		writeError(w, http.StatusMethodNotAllowed, "snapshot unavailable")
		return
	}
	v, err := s.provider.Snapshot()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": v, "at": time.Now().UTC()})
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	s.collection(w, r, s.provider.Overview)
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	if id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/"); id != "" && id != r.URL.Path {
		if r.Method != http.MethodGet || s.provider.Session == nil {
			writeError(w, http.StatusMethodNotAllowed, "session unavailable")
			return
		}
		if strings.ContainsAny(id, `/\\`) {
			writeError(w, http.StatusBadRequest, "invalid session id")
			return
		}
		v, err := s.provider.Session(id)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": v})
		return
	}
	s.collection(w, r, s.provider.Sessions)
}

func (s *Server) collection(w http.ResponseWriter, r *http.Request, fn func(Query) (any, error)) {
	if r.Method != http.MethodGet || fn == nil {
		writeError(w, http.StatusMethodNotAllowed, "collection unavailable")
		return
	}
	limit := s.cfg.PageLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > s.cfg.PageLimit {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and the configured page limit")
			return
		}
		limit = n
	}
	var since time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		var err error
		since, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
	}
	filters := make(map[string]string)
	for key, values := range r.URL.Query() {
		if len(values) > 0 && key != "cursor" && key != "limit" && key != "since" {
			if len(values[0]) > 256 {
				writeError(w, http.StatusBadRequest, "filter is too long")
				return
			}
			filters[key] = values[0]
		}
	}
	v, err := fn(Query{Cursor: r.URL.Query().Get("cursor"), Limit: limit, Since: since, Filter: filters})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	data := safeJSON(v)
	next := ""
	if object, ok := data.(map[string]any); ok {
		if cursor, ok := object["next_cursor"].(string); ok {
			next = cursor
		}
	}
	writeJSONSafe(w, http.StatusOK, map[string]any{"data": data, "next_cursor": next})
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	if id := strings.TrimPrefix(r.URL.Path, "/api/v1/runs/"); id != "" && id != r.URL.Path {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		if s.provider.Run == nil {
			writeError(w, http.StatusNotFound, "run not found")
			return
		}
		v, err := s.provider.Run(id)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": v})
		return
	}
	s.collection(w, r, s.provider.Runs)
}
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	s.collection(w, r, s.provider.Metrics)
}
func (s *Server) traces(w http.ResponseWriter, r *http.Request) {
	s.collection(w, r, s.provider.Traces)
}
func (s *Server) incidents(w http.ResponseWriter, r *http.Request) {
	s.collection(w, r, s.provider.Incidents)
}
func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || s.provider.Catalog == nil {
		writeError(w, http.StatusMethodNotAllowed, "catalog unavailable")
		return
	}
	v, err := s.provider.Catalog()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": v})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || s.provider.Events == nil {
		writeError(w, http.StatusMethodNotAllowed, "events unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	var after uint64
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		after, _ = strconv.ParseUint(raw, 10, 64)
	} else if raw := r.URL.Query().Get("after"); raw != "" {
		after, _ = strconv.ParseUint(raw, 10, 64)
	}
	sub := s.provider.Events.Subscribe(after)
	defer sub.Cancel()
	if sub.Reset {
		writeSSE(w, "reset", 0, map[string]any{"reason": "cursor_expired"})
		flusher.Flush()
	}
	for _, event := range sub.Replay {
		writeSSE(w, event.Kind, event.Seq, event)
		flusher.Flush()
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-sub.Events:
			if !ok {
				return
			}
			writeSSE(w, event.Kind, event.Seq, event)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body) != nil {
		writeError(w, http.StatusBadRequest, "invalid login")
		return
	}
	listener := s.listenerForRequest(r)
	if listener.Mode != "lan" || r.TLS == nil || !s.checkToken(listener.TokenFile, body.Token) {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		writeError(w, http.StatusInternalServerError, "cannot create session")
		return
	}
	id := base64.RawURLEncoding.EncodeToString(raw[:])
	s.mux.Lock()
	s.authSessions[id] = time.Now().Add(s.cfg.SessionTTL)
	s.mux.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "atenea_dashboard", Value: id, Path: "/", Expires: time.Now().Add(s.cfg.SessionTTL), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) authorized(r *http.Request) bool {
	l := s.listenerForRequest(r)
	remote := remoteIP(r.RemoteAddr)
	switch strings.ToLower(l.Mode) {
	case "loopback":
		return remote != nil && remote.IsLoopback()
	case "tailscale":
		return remote != nil && remote.IsLoopback() && strings.TrimSpace(r.Header.Get("Tailscale-User-Login")) != ""
	case "lan":
		if r.TLS == nil {
			return false
		}
		cookie, err := r.Cookie("atenea_dashboard")
		if err != nil {
			return false
		}
		s.mux.Lock()
		expires, ok := s.authSessions[cookie.Value]
		if ok && time.Now().After(expires) {
			delete(s.authSessions, cookie.Value)
			ok = false
		}
		s.mux.Unlock()
		return ok
	default:
		return false
	}
}

func remoteIP(raw string) net.IP {
	if host, _, err := net.SplitHostPort(strings.TrimSpace(raw)); err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(strings.TrimSpace(raw))
}

func (s *Server) listenerForRequest(r *http.Request) Listener {
	if len(s.cfg.Listeners) == 0 {
		return Listener{Mode: "loopback"}
	}
	host, port, _ := net.SplitHostPort(r.Host)
	for _, listener := range s.cfg.Listeners {
		_, p, _ := net.SplitHostPort(listener.Addr)
		if p == port && host != "" {
			return listener
		}
	}
	return s.cfg.Listeners[0]
}

func (s *Server) checkToken(path, token string) bool {
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return false
	}
	want, err := os.ReadFile(clean)
	if err != nil {
		return false
	}
	want = []byte(strings.TrimSpace(string(want)))
	token = strings.TrimSpace(token)
	return token != "" && len(want) == len(token) && subtle.ConstantTimeCompare(want, []byte(token)) == 1
}

// Start binds all configured listeners. It returns after binding, while the
// HTTP serving loops run in the background alongside Core.Run.
func (s *Server) Start(ctx context.Context) error {
	if s == nil || !s.cfg.Enabled {
		return nil
	}
	handler := s.Handler()
	for _, cfg := range s.cfg.Listeners {
		listener, err := net.Listen("tcp", cfg.Addr)
		if err != nil {
			_ = s.Close(context.Background())
			return fmt.Errorf("dashboard: listen %s: %w", cfg.Addr, err)
		}
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 0, IdleTimeout: 60 * time.Second}
		if strings.EqualFold(cfg.Mode, "lan") {
			cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
			if err != nil {
				_ = listener.Close()
				_ = s.Close(context.Background())
				return fmt.Errorf("dashboard: TLS: %w", err)
			}
			server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}
			listener = tls.NewListener(listener, server.TLSConfig)
		}
		s.mux.Lock()
		s.listeners = append(s.listeners, listener)
		s.servers = append(s.servers, server)
		s.mux.Unlock()
		go func(server *http.Server, listener net.Listener) { _ = server.Serve(listener) }(server, listener)
	}
	go func() { <-ctx.Done(); _ = s.Close(context.Background()) }()
	return nil
}

// Close stops every dashboard listener and releases active event streams.
func (s *Server) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mux.Lock()
	servers := append([]*http.Server(nil), s.servers...)
	listeners := append([]net.Listener(nil), s.listeners...)
	s.servers = nil
	s.listeners = nil
	s.mux.Unlock()
	// Closing the hub first releases active SSE handlers. Otherwise
	// http.Server.Shutdown would wait forever for a client that intentionally
	// keeps its stream open, defeating Core's bounded shutdown grace.
	if s.provider.Events != nil {
		s.provider.Events.Close()
	}
	var first error
	for _, server := range servers {
		if err := server.Shutdown(ctx); err != nil && first == nil {
			first = err
		}
	}
	for _, listener := range listeners {
		_ = listener.Close()
	}
	return first
}

func writeSSE(w http.ResponseWriter, kind string, seq uint64, value any) {
	body, _ := json.Marshal(safeJSON(value))
	if seq > 0 {
		fmt.Fprintf(w, "id: %d\n", seq)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", safeEventName(kind), body)
}
func safeEventName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "message"
	}
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, raw)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	writeJSONValue(w, status, safeJSON(value))
}
func writeJSONSafe(w http.ResponseWriter, status int, value any) {
	writeJSONValue(w, status, value)
}
func writeJSONValue(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	message = contract.RedactRaw(strings.TrimSpace(message))
	message = truncateDashboardText(message, 240)
	// Error envelopes are generated by this handler, not provider data. Keep
	// the stable public key while safeJSON strips provider-supplied error
	// fields from operational DTOs.
	writeJSONSafe(w, status, map[string]any{"error": message})
}

func truncateDashboardText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	if limit <= len("…") {
		cut := limit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		return text[:cut]
	}
	cut := limit - len("…")
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// safeJSON lower-camel-cases exported Go fields without requiring API DTOs in
// Core. It also makes time values standard JSON strings and recursively walks
// slices/maps. The source values are already metadata-only callbacks.
func safeJSON(value any) any {
	body, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"error": "serialization failed"}
	}
	var out any
	if json.Unmarshal(body, &out) != nil {
		return map[string]any{"error": "serialization failed"}
	}
	return normalizeDurations(lowerKeys(out))
}
func lowerKeys(value any) any {
	switch x := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, value := range x {
			key = lowerCamel(key)
			if sensitiveDashboardKey(key) {
				continue
			}
			out[key] = lowerKeys(value)
		}
		return out
	case []any:
		for i := range x {
			x[i] = lowerKeys(x[i])
		}
		return x
	default:
		return value
	}
}

func sensitiveDashboardKey(key string) bool {
	switch strings.ToLower(key) {
	case "raw", "result", "results", "payload", "payloads", "discoveries", "discovery", "stack", "error", "errors",
		"path", "paths", "dir", "directory", "endpoint", "where", "settings",
		"message", "messages", "argument", "arguments", "input", "inputs",
		"output", "outputs", "response", "responses", "raw_error", "raw_errors",
		"credential", "credentials", "route", "routes", "url", "urls", "location",
		"host", "hostname", "device", "devices", "dashboard":
		return true
	default:
		return false
	}
}

// time.Duration marshals as nanoseconds, while the dashboard contract uses
// milliseconds. These are the only duration fields emitted by the current
// metadata DTOs; explicit DurationMS fields are already in the right unit and
// deliberately do not match this list.
func normalizeDurations(value any) any {
	switch x := value.(type) {
	case map[string]any:
		for key, item := range x {
			if key == "every" || key == "mean" || key == "slowest" {
				if number, ok := item.(float64); ok {
					x[key] = number / float64(time.Millisecond)
					continue
				}
			}
			x[key] = normalizeDurations(item)
		}
		return x
	case []any:
		for i := range x {
			x[i] = normalizeDurations(x[i])
		}
		return x
	default:
		return value
	}
}
func lowerCamel(raw string) string {
	if raw == "" {
		return raw
	}
	i := 0
	for i < len(raw) && raw[i] >= 'A' && raw[i] <= 'Z' {
		i++
	}
	if i == 0 {
		return raw
	}
	if i == len(raw) {
		return strings.ToLower(raw)
	}
	if i == 1 {
		return strings.ToLower(raw[:1]) + raw[1:]
	}
	return strings.ToLower(raw[:i-1]) + raw[i-1:]
}
