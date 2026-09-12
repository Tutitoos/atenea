package remotecoordinator

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Tutitoos/atenea/internal/remotedevice"
	"github.com/Tutitoos/atenea/pkg/remoteprotocol"
)

type recordingAuthenticator struct {
	mu       sync.Mutex
	calls    int
	received remotedevice.AuthenticateRequest
	device   remotedevice.Device
	err      error
	entered  chan struct{}
	release  <-chan struct{}
	closed   chan remotedevice.CloseSessionRequest
	once     sync.Once
}

func (a *recordingAuthenticator) Authenticate(_ context.Context, request remotedevice.AuthenticateRequest) (remotedevice.Device, error) {
	a.mu.Lock()
	a.calls++
	a.received = request
	a.mu.Unlock()
	if a.entered != nil {
		a.once.Do(func() { close(a.entered) })
	}
	if a.release != nil {
		<-a.release
	}
	if a.err != nil {
		return remotedevice.Device{}, a.err
	}
	return a.device, nil
}

func (a *recordingAuthenticator) AuthenticateAndRegisterSession(ctx context.Context, request remotedevice.AuthenticateAndRegisterSessionRequest) (remotedevice.Session, error) {
	device, err := a.Authenticate(ctx, remotedevice.AuthenticateRequest{DeviceID: request.DeviceID, CertificateID: request.CertificateID, Fingerprint: request.Fingerprint, PublicKeyDigest: request.PublicKeyDigest, RequestID: request.RequestID})
	if err != nil {
		return remotedevice.Session{}, err
	}
	if request.DeviceID != device.ID {
		return remotedevice.Session{}, remotedevice.ErrAuthentication
	}
	if request.Platform != "" && request.Platform != device.Platform || request.Architecture != "" && request.Architecture != device.Architecture {
		return remotedevice.Session{}, remotedevice.ErrAuthentication
	}
	return remotedevice.Session{ID: request.SessionID, DeviceID: request.DeviceID, State: "active", Fence: device.Fence, CreatedAt: time.Now().UTC()}, nil
}

func (a *recordingAuthenticator) CloseSession(_ context.Context, request remotedevice.CloseSessionRequest) (remotedevice.Session, error) {
	if a.closed != nil {
		a.closed <- request
	}
	return remotedevice.Session{ID: request.SessionID, DeviceID: request.DeviceID, State: "closed", Reason: request.Reason}, nil
}

func (a *recordingAuthenticator) snapshot() (int, remotedevice.AuthenticateRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	request := a.received
	request.PublicKeyDigest = append([]byte(nil), request.PublicKeyDigest...)
	return a.calls, request
}

type testTLSMaterial struct {
	caPool     *x509.CertPool
	caCert     *x509.Certificate
	caPrivate  ed25519.PrivateKey
	serverTLS  tls.Certificate
	clientTLS  tls.Certificate
	clientCert *x509.Certificate
}

func newTLSMaterial(t *testing.T) testTLSMaterial {
	t.Helper()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: bigInt(t, 1), Subject: pkixName("ATENEA test CA"), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	serverTLS, _ := signedTLSCertificate(t, caCert, caPrivate, 2, x509.ExtKeyUsageServerAuth, []string{"localhost"})
	clientTLS, clientCert := signedTLSCertificate(t, caCert, caPrivate, 3, x509.ExtKeyUsageClientAuth, nil)
	return testTLSMaterial{caPool: pool, caCert: caCert, caPrivate: caPrivate, serverTLS: serverTLS, clientTLS: clientTLS, clientCert: clientCert}
}

func signedTLSCertificate(t *testing.T, ca *x509.Certificate, caPrivate ed25519.PrivateKey, serial int64, usage x509.ExtKeyUsage, dnsNames []string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: bigInt(t, serial), Subject: pkixName(fmt.Sprintf("leaf-%d", serial)), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, DNSNames: dnsNames}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, public, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	tlsCert := tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: private, Leaf: cert}
	return tlsCert, cert
}

// These small helpers keep the test certificate setup independent of any
// production identity or CA package. The test proves TLS verification only.
func bigInt(t *testing.T, value int64) *big.Int {
	t.Helper()
	return big.NewInt(value)
}

func pkixName(commonName string) pkix.Name {
	return pkix.Name{CommonName: commonName}
}

func startTLSServer(t *testing.T, handler http.Handler, material testTLSMaterial, opts ...Option) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{material.serverTLS}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: material.caPool, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func dialTLS(t *testing.T, server *httptest.Server, material testTLSMaterial, headers http.Header) *websocket.Conn {
	t.Helper()
	url := "wss" + strings.TrimPrefix(server.URL, "https") + ConnectPath
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: material.caPool, Certificates: []tls.Certificate{material.clientTLS}, ServerName: "localhost", MinVersion: tls.VersionTLS13}}
	conn, response, err := dialer.Dial(url, headers)
	if err != nil {
		if response != nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 256))
			_ = response.Body.Close()
			t.Fatalf("dial: %v (status %d body %q)", err, response.StatusCode, body)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func validOfferJSON(deviceID, sessionID, platform, architecture string) []byte {
	offer := map[string]any{
		"protocol":     remoteprotocol.Subprotocol,
		"version":      remotedevice.ProtocolVersion,
		"message_type": "negotiation",
		"session_id":   sessionID,
		"device_id":    deviceID,
		"sequence":     0,
		"sent_at":      0,
		"payload": map[string]any{
			"phase":              "offer",
			"role":               "agent",
			"agent_version":      "test-agent",
			"platform":           platform,
			"architecture":       architecture,
			"supported_versions": []string{remotedevice.ProtocolVersion},
			"modes":              []string{"background"},
			"capabilities":       []string{},
		},
	}
	raw, _ := json.Marshal(offer)
	return raw
}

func configuredHandler(t *testing.T, material testTLSMaterial, auth *recordingAuthenticator, opts ...Option) (*httptest.Server, []byte, string) {
	t.Helper()
	fingerprint := sha256.Sum256(material.clientCert.Raw)
	spki := sha256.Sum256(material.clientCert.RawSubjectPublicKeyInfo)
	auth.device = remotedevice.Device{ID: "device-1", Platform: remotedevice.PlatformLinux, Architecture: remotedevice.ArchitectureX8664, State: "active", Certificate: remotedevice.CertificateMetadata{Fingerprint: hex.EncodeToString(fingerprint[:]), PublicKeyDigest: append([]byte(nil), spki[:]...)}}
	handler := NewHandler(auth, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }), opts...)
	server := startTLSServer(t, handler, material)
	return server, validOfferJSON("device-1", "session-1", "linux", "x86_64"), hex.EncodeToString(fingerprint[:])
}

func TestAuthenticatedWSSNegotiationUsesVerifiedLeafIdentity(t *testing.T) {
	material := newTLSMaterial(t)
	release := make(chan struct{})
	auth := &recordingAuthenticator{entered: make(chan struct{}), release: release}
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	server, offer, fingerprint := configuredHandler(t, material, auth)
	conn := dialTLS(t, server, material, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}, "X-Forwarded-For": []string{"198.51.100.7"}})
	if got := conn.Subprotocol(); got != remoteprotocol.Subprotocol {
		t.Fatalf("subprotocol = %q, want %q", got, remoteprotocol.Subprotocol)
	}
	if err := conn.WriteMessage(websocket.TextMessage, offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-auth.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("authenticator did not reach first offer")
	}
	// The authenticator barrier makes the causal order explicit: the first
	// offer is being processed while the second pipeline message is written.
	if err := conn.WriteMessage(websocket.TextMessage, offer); err != nil {
		t.Fatalf("second pipeline message: %v", err)
	}
	close(release)
	released = true
	messageType, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read acceptance: %v", err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("acceptance message type = %d, want text", messageType)
	}
	if err := remoteprotocol.New().Validate(raw); err != nil {
		t.Fatalf("acceptance is not schema-valid: %v", err)
	}
	var acceptance struct {
		Sequence uint64 `json:"sequence"`
		Payload  struct {
			Phase     string         `json:"phase"`
			Role      string         `json:"role"`
			Version   string         `json:"accepted_version"`
			Mode      string         `json:"accepted_mode"`
			Grants    map[string]any `json:"grants"`
			Heartbeat int            `json:"heartbeat_interval_ms"`
			Liveness  int            `json:"liveness_deadline_ms"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &acceptance); err != nil {
		t.Fatal(err)
	}
	if acceptance.Sequence != 0 || acceptance.Payload.Phase != "accept" || acceptance.Payload.Role != "coordinator" || acceptance.Payload.Version != remotedevice.ProtocolVersion || acceptance.Payload.Mode != "background" || len(acceptance.Payload.Grants) != 0 || acceptance.Payload.Heartbeat != HeartbeatIntervalMillis || acceptance.Payload.Liveness != LivenessDeadlineMillis {
		t.Fatalf("unexpected acceptance: %s", raw)
	}
	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.ClosePolicyViolation || closeErr.Text != CloseReasonPolicy {
		t.Fatalf("close = %v, want policy %q", err, CloseReasonPolicy)
	}
	calls, request := auth.snapshot()
	spkiDigest := sha256.Sum256(material.clientCert.RawSubjectPublicKeyInfo)
	if calls != 1 || request.DeviceID != "device-1" || request.Fingerprint != fingerprint || !bytes.Equal(request.PublicKeyDigest, spkiDigest[:]) {
		t.Fatalf("auth request = %+v, calls=%d", request, calls)
	}
}

func TestTransportGuardsRunBeforeAuthentication(t *testing.T) {
	tests := []struct {
		name    string
		request func(*testing.T, *httptest.Server, testTLSMaterial)
	}{
		{name: "plain http", request: func(t *testing.T, server *httptest.Server, _ testTLSMaterial) {
			response, err := http.Get(server.URL + ConnectPath)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
			}
		}},
		{name: "origin", request: func(t *testing.T, server *httptest.Server, material testTLSMaterial) {
			dialRejected(t, server, material, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}, "Origin": []string{"https://browser.invalid"}})
		}},
		{name: "wrong protocol", request: func(t *testing.T, server *httptest.Server, material testTLSMaterial) {
			dialRejected(t, server, material, http.Header{"Sec-WebSocket-Protocol": []string{"atenea.remote.v0"}})
		}},
		{name: "extra protocol", request: func(t *testing.T, server *httptest.Server, material testTLSMaterial) {
			dialRejected(t, server, material, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol + ", other"}})
		}},
		{name: "missing protocol", request: func(t *testing.T, server *httptest.Server, material testTLSMaterial) {
			dialRejected(t, server, material, nil)
		}},
		{name: "wrong path", request: func(t *testing.T, server *httptest.Server, material testTLSMaterial) {
			url := "wss" + strings.TrimPrefix(server.URL, "https") + "/remote/v1/other"
			dialer := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: material.caPool, Certificates: []tls.Certificate{material.clientTLS}, ServerName: "localhost", MinVersion: tls.VersionTLS13}}
			_, response, err := dialer.Dial(url, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
			if err == nil || response == nil || response.StatusCode != http.StatusNotFound {
				t.Fatalf("wrong path response = %#v, error = %v", response, err)
			}
			_ = response.Body.Close()
		}},
		{name: "wrong method", request: func(t *testing.T, server *httptest.Server, material testTLSMaterial) {
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: material.caPool, Certificates: []tls.Certificate{material.clientTLS}, ServerName: "localhost", MinVersion: tls.VersionTLS13}}
			client := &http.Client{Transport: transport}
			request, err := http.NewRequest(http.MethodPost, server.URL+ConnectPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Sec-WebSocket-Protocol", remoteprotocol.Subprotocol)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("wrong method status = %d, want %d", response.StatusCode, http.StatusNotFound)
			}
		}},
	}
	material := newTLSMaterial(t)
	auth := &recordingAuthenticator{}
	server := startTLSServer(t, NewHandler(auth, PeerGateFunc(func(_ context.Context, _ string) bool { return true })), material)
	plainServer := httptest.NewServer(NewHandler(auth, PeerGateFunc(func(_ context.Context, _ string) bool { return true })))
	t.Cleanup(plainServer.Close)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestServer := server
			if test.name == "plain http" {
				requestServer = plainServer
			}
			test.request(t, requestServer, material)
		})
	}
	if calls, _ := auth.snapshot(); calls != 0 {
		t.Fatalf("authentication calls = %d, want zero", calls)
	}
}

func TestMissingAndUnverifiedClientCertificatesAreRejected(t *testing.T) {
	material := newTLSMaterial(t)
	auth := &recordingAuthenticator{}
	server := httptest.NewUnstartedServer(NewHandler(auth, PeerGateFunc(func(_ context.Context, _ string) bool { return true })))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{material.serverTLS}, ClientAuth: tls.RequestClientCert, ClientCAs: material.caPool, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	url := "wss" + strings.TrimPrefix(server.URL, "https") + ConnectPath

	for _, test := range []struct {
		name         string
		certificates []tls.Certificate
	}{
		{name: "missing", certificates: nil},
		{name: "unverified", certificates: []tls.Certificate{newTLSMaterial(t).clientTLS}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dialer := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: material.caPool, Certificates: test.certificates, ServerName: "localhost", MinVersion: tls.VersionTLS13}}
			_, response, err := dialer.Dial(url, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
			if err == nil {
				t.Fatal("certificate rejection unexpectedly upgraded")
			}
			if response == nil || response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("response = %#v, error = %v", response, err)
			}
			_ = response.Body.Close()
		})
	}
	if calls, _ := auth.snapshot(); calls != 0 {
		t.Fatalf("authentication calls = %d, want zero", calls)
	}
}

func dialRejected(t *testing.T, server *httptest.Server, material testTLSMaterial, headers http.Header) {
	t.Helper()
	url := "wss" + strings.TrimPrefix(server.URL, "https") + ConnectPath
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: material.caPool, Certificates: []tls.Certificate{material.clientTLS}, ServerName: "localhost", MinVersion: tls.VersionTLS13}}
	conn, response, err := dialer.Dial(url, headers)
	if err == nil {
		_ = conn.Close()
		t.Fatal("rejected transport unexpectedly upgraded")
	}
	if response == nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("response = %#v, error = %v", response, err)
	}
	_ = response.Body.Close()
}

func TestOfferValidationAndAuthenticationFailuresAreSanitized(t *testing.T) {
	material := newTLSMaterial(t)
	tests := []struct {
		name   string
		mutate func([]byte) []byte
		code   int
		reason string
		calls  int
	}{
		{name: "binary", mutate: func([]byte) []byte { return []byte{1, 2, 3} }, code: websocket.CloseUnsupportedData, reason: CloseReasonBinary},
		{name: "invalid utf8", mutate: func([]byte) []byte { return []byte{0xc3, 0x28} }, code: websocket.CloseInvalidFramePayloadData, reason: CloseReasonInvalidUTF8},
		{name: "malformed", mutate: func([]byte) []byte { return []byte(`{"protocol":`) }, code: websocket.ClosePolicyViolation, reason: CloseReasonEnvelope},
		{name: "duplicate", mutate: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"protocol":"atenea.remote.v1"`), []byte(`"protocol":"atenea.remote.v1","protocol":"atenea.remote.v1"`), 1)
		}, code: websocket.ClosePolicyViolation, reason: CloseReasonEnvelope},
		{name: "trailing", mutate: func(raw []byte) []byte { return append(raw, []byte(" trailing")...) }, code: websocket.ClosePolicyViolation, reason: CloseReasonEnvelope},
		{name: "wrong version", mutate: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"version":"1.1.0"`), []byte(`"version":"1.0.0"`), 1)
		}, code: websocket.ClosePolicyViolation, reason: CloseReasonEnvelope},
		{name: "wrong sequence", mutate: func(raw []byte) []byte { return bytes.Replace(raw, []byte(`"sequence":0`), []byte(`"sequence":1`), 1) }, code: websocket.ClosePolicyViolation, reason: CloseReasonPolicy},
		{name: "wrong platform", mutate: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"platform":"linux"`), []byte(`"platform":"macos"`), 1)
		}, code: websocket.ClosePolicyViolation, reason: CloseReasonAuthentication, calls: 1},
		{name: "wrong architecture", mutate: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"architecture":"x86_64"`), []byte(`"architecture":"arm64"`), 1)
		}, code: websocket.ClosePolicyViolation, reason: CloseReasonAuthentication, calls: 1},
		{name: "wrong mode", mutate: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"modes":["background"]`), []byte(`"modes":["attended"]`), 1)
		}, code: websocket.ClosePolicyViolation, reason: CloseReasonPolicy},
		{name: "capability", mutate: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"capabilities":[]`), []byte(`"capabilities":["remote.desktop.targets"]`), 1)
		}, code: websocket.ClosePolicyViolation, reason: CloseReasonPolicy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auth := &recordingAuthenticator{}
			server, offer, _ := configuredHandler(t, material, auth)
			conn := dialTLS(t, server, material, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
			if test.name == "binary" {
				if err := conn.WriteMessage(websocket.BinaryMessage, test.mutate(offer)); err != nil {
					t.Fatal(err)
				}
			} else if err := conn.WriteMessage(websocket.TextMessage, test.mutate(offer)); err != nil {
				t.Fatal(err)
			}
			_, _, err := conn.ReadMessage()
			var closeErr *websocket.CloseError
			if !errors.As(err, &closeErr) || closeErr.Code != test.code || closeErr.Text != test.reason {
				t.Fatalf("close = %v, want code=%d reason=%q", err, test.code, test.reason)
			}
			if calls, _ := auth.snapshot(); calls != test.calls {
				t.Fatalf("authentication calls = %d, want %d", calls, test.calls)
			}
		})
	}
}

func TestAuthenticationDenialAndOfferBindingDoNotCreateSession(t *testing.T) {
	material := newTLSMaterial(t)
	tests := []struct {
		name   string
		device string
		err    error
		offer  func([]byte) []byte
	}{
		{name: "registry denial", device: "device-1", err: remotedevice.ErrAuthentication},
		{name: "device mismatch", device: "device-2", offer: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"device_id":"device-1"`), []byte(`"device_id":"device-2"`), 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auth := &recordingAuthenticator{err: test.err}
			server, offer, _ := configuredHandler(t, material, auth)
			if test.offer != nil {
				offer = test.offer(offer)
			}
			conn := dialTLS(t, server, material, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
			if err := conn.WriteMessage(websocket.TextMessage, offer); err != nil {
				t.Fatal(err)
			}
			_, _, err := conn.ReadMessage()
			var closeErr *websocket.CloseError
			if !errors.As(err, &closeErr) || closeErr.Code != websocket.ClosePolicyViolation || closeErr.Text != CloseReasonAuthentication {
				t.Fatalf("close = %v, want code=%d reason=%q", err, websocket.ClosePolicyViolation, CloseReasonAuthentication)
			}
			if calls, _ := auth.snapshot(); calls != 1 {
				t.Fatalf("authentication calls = %d, want one", calls)
			}
		})
	}
}

func TestReadTimeoutAndCompressionRemainBounded(t *testing.T) {
	material := newTLSMaterial(t)
	auth := &recordingAuthenticator{}
	server, _, _ := configuredHandler(t, material, auth, WithReadTimeout(20*time.Millisecond))
	url := "wss" + strings.TrimPrefix(server.URL, "https") + ConnectPath
	dialer := websocket.Dialer{EnableCompression: true, TLSClientConfig: &tls.Config{RootCAs: material.caPool, Certificates: []tls.Certificate{material.clientTLS}, ServerName: "localhost", MinVersion: tls.VersionTLS13}}
	conn, response, err := dialer.Dial(url, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.Subprotocol() != remoteprotocol.Subprotocol {
		t.Fatalf("subprotocol = %q", conn.Subprotocol())
	}
	if extensions := response.Header.Get("Sec-WebSocket-Extensions"); extensions != "" {
		t.Fatalf("compression extension = %q, want disabled", extensions)
	}
	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.ClosePolicyViolation || closeErr.Text != CloseReasonTimeout {
		t.Fatalf("timeout close = %v, want code=%d reason=%q", err, websocket.ClosePolicyViolation, CloseReasonTimeout)
	}
	if calls, _ := auth.snapshot(); calls != 0 {
		t.Fatalf("authentication calls = %d, want zero", calls)
	}
}

func TestIncompleteFragmentedBodyUsesTimeoutClose(t *testing.T) {
	material := newTLSMaterial(t)
	auth := &recordingAuthenticator{}
	server, _, _ := configuredHandler(t, material, auth, WithReadTimeout(30*time.Millisecond))
	url := "wss" + strings.TrimPrefix(server.URL, "https") + ConnectPath
	dialer := websocket.Dialer{WriteBufferSize: 1024, TLSClientConfig: &tls.Config{RootCAs: material.caPool, Certificates: []tls.Certificate{material.clientTLS}, ServerName: "localhost", MinVersion: tls.VersionTLS13}}
	conn, response, err := dialer.Dial(url, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	writer, err := conn.NextWriter(websocket.TextMessage)
	if err != nil {
		t.Fatal(err)
	}
	// The small writer buffer forces a non-final first fragment. Leaving the
	// writer open makes the server exercise its read deadline, without a sleep.
	if _, err := writer.Write(bytes.Repeat([]byte("x"), 2048)); err != nil {
		t.Fatal(err)
	}
	readCloseWithCode(t, conn, websocket.ClosePolicyViolation, CloseReasonTimeout)
	if calls, _ := auth.snapshot(); calls != 0 {
		t.Fatalf("authentication calls = %d, want zero", calls)
	}
}

func TestPeerGateReceivesOnlyRemoteAddr(t *testing.T) {
	material := newTLSMaterial(t)
	var received string
	server := startTLSServer(t, NewHandler(&recordingAuthenticator{}, PeerGateFunc(func(_ context.Context, remoteAddr string) bool {
		received = remoteAddr
		return false
	})), material)
	url := "wss" + strings.TrimPrefix(server.URL, "https") + ConnectPath
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: material.caPool, Certificates: []tls.Certificate{material.clientTLS}, ServerName: "localhost", MinVersion: tls.VersionTLS13}}
	_, response, err := dialer.Dial(url, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}, "X-Forwarded-For": []string{"203.0.113.1"}})
	if err == nil {
		t.Fatal("peer gate denial unexpectedly upgraded")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("response = %#v, error = %v", response, err)
	}
	if received == "" || strings.Contains(received, "203.0.113.1") {
		t.Fatalf("peer gate received %q", received)
	}
	_ = response.Body.Close()
}

func TestExactControlMessageLimit(t *testing.T) {
	material := newTLSMaterial(t)
	auth := &recordingAuthenticator{}
	server, _, _ := configuredHandler(t, material, auth)
	conn := dialTLS(t, server, material, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
	oversized := bytes.Repeat([]byte("a"), remoteprotocol.MaxControlMessageSize+1)
	if err := conn.WriteMessage(websocket.TextMessage, oversized); err != nil {
		t.Fatal(err)
	}
	_, _, err := conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseMessageTooBig || closeErr.Text != CloseReasonOversize {
		t.Fatalf("oversized close = %v, want code=%d reason=%q", err, websocket.CloseMessageTooBig, CloseReasonOversize)
	}
	if calls, _ := auth.snapshot(); calls != 0 {
		t.Fatalf("authentication calls = %d, want zero", calls)
	}
}

func TestExactControlMessageLimitIsAccepted(t *testing.T) {
	material := newTLSMaterial(t)
	auth := &recordingAuthenticator{}
	server, offer, _ := configuredHandler(t, material, auth)
	conn := dialTLS(t, server, material, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
	if len(offer) >= remoteprotocol.MaxControlMessageSize {
		t.Fatalf("offer unexpectedly exceeds test boundary: %d", len(offer))
	}
	exact := append(append([]byte(nil), offer...), bytes.Repeat([]byte(" "), remoteprotocol.MaxControlMessageSize-len(offer))...)
	if err := conn.WriteMessage(websocket.TextMessage, exact); err != nil {
		t.Fatal(err)
	}
	messageType, raw, err := conn.ReadMessage()
	if err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	if err := remoteprotocol.New().Validate(raw); err != nil {
		t.Fatalf("exact-limit acceptance validation: %v", err)
	}
	if err := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if calls, _ := auth.snapshot(); calls != 1 {
		t.Fatalf("authentication calls = %d, want one", calls)
	}
}

func TestFragmentedOversizeMessageUsesSameBoundedClose(t *testing.T) {
	material := newTLSMaterial(t)
	auth := &recordingAuthenticator{}
	server, _, _ := configuredHandler(t, material, auth)
	url := "wss" + strings.TrimPrefix(server.URL, "https") + ConnectPath
	dialer := websocket.Dialer{WriteBufferSize: 1024, TLSClientConfig: &tls.Config{RootCAs: material.caPool, Certificates: []tls.Certificate{material.clientTLS}, ServerName: "localhost", MinVersion: tls.VersionTLS13}}
	conn, response, err := dialer.Dial(url, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	writer, err := conn.NextWriter(websocket.TextMessage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(bytes.Repeat([]byte("x"), remoteprotocol.MaxControlMessageSize+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	readCloseWithCode(t, conn, websocket.CloseMessageTooBig, CloseReasonOversize)
	if calls, _ := auth.snapshot(); calls != 0 {
		t.Fatalf("authentication calls = %d, want zero", calls)
	}
}

func readCloseWithCode(t *testing.T, conn *websocket.Conn, wantCode int, wantReason string) {
	t.Helper()
	_, _, err := conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != wantCode || closeErr.Text != wantReason {
		t.Fatalf("close = %v, want code=%d reason=%q", err, wantCode, wantReason)
	}
}

func TestSessionOwnersCloseIsTerminalAndDeviceScoped(t *testing.T) {
	owners := newSessionOwners()
	first, ok := owners.reserve("session-one", "device-one", nil)
	if !ok {
		t.Fatal("first reservation failed")
	}
	second, ok := owners.reserve("session-two", "device-two", nil)
	if !ok {
		t.Fatal("second reservation failed")
	}
	owners.activate(first, remotedevice.Session{ID: first.sessionID, DeviceID: first.deviceID, Fence: 0, State: "active"})
	owners.activate(second, remotedevice.Session{ID: second.sessionID, DeviceID: second.deviceID, Fence: 0, State: "active"})
	if _, ok := owners.markAccepted(first); !ok {
		t.Fatal("first owner acceptance publication failed")
	}
	if _, ok := owners.markAccepted(second); !ok {
		t.Fatal("second owner acceptance publication failed")
	}
	intent := remotedevice.ClosureIntent{ID: "closure-one", DeviceID: "device-one", SessionID: "session-one", Fence: 1, State: "pending"}
	if got := owners.forIntent(intent); got != first {
		t.Fatalf("intent owner = %p, want %p", got, first)
	}
	if second.closed {
		t.Fatal("revoking one device closed another device owner")
	}
	if first.closed {
		t.Fatal("owner selection mutated first owner before delivery")
	}
	otherIntent := remotedevice.ClosureIntent{ID: "closure-two", DeviceID: "device-two", SessionID: "session-two", Fence: 1, State: "pending"}
	if got := owners.forIntent(otherIntent); got != second {
		t.Fatalf("exact owner selection = %p, want %p", got, second)
	}
}

func TestSessionOwnersClosePreventsLaterReservationsAndIsIdempotent(t *testing.T) {
	owners := newSessionOwners()
	owner, ok := owners.reserve("session-one", "device-one", nil)
	if !ok {
		t.Fatal("reservation failed")
	}
	handler := NewHandler(&recordingAuthenticator{}, PeerGateFunc(func(context.Context, string) bool { return true }))
	var wg sync.WaitGroup
	reserved := make(chan bool, 1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		owners.closeAll(handler, CloseReasonTransport)
	}()
	go func() {
		defer wg.Done()
		candidate, candidateOK := owners.reserve("session-race", "device-one", nil)
		if candidateOK && candidate == nil {
			t.Errorf("successful reservation returned nil owner")
		}
		reserved <- candidateOK
	}()
	wg.Wait()
	select {
	case candidateOK := <-reserved:
		if candidateOK {
			if _, exists := owners.owners["session-race"]; !exists {
				t.Fatal("race reservation reported success but owner was absent")
			}
		}
	default:
		t.Fatal("reservation race produced no outcome")
	}
	if _, ok := owners.reserve("session-after-close", "device-one", nil); ok {
		t.Fatal("reservation succeeded after terminal close")
	}
	if !owners.isClosed(owner) {
		t.Fatal("existing owner not marked closed")
	}
	owners.closeAll(handler, CloseReasonTransport)
}

func TestSessionOwnerRevocationSequenceNeverWraps(t *testing.T) {
	owners := newSessionOwners()
	owner, ok := owners.reserve("session-sequence", "device-sequence", nil)
	if !ok {
		t.Fatal("reservation failed")
	}
	owners.activate(owner, remotedevice.Session{ID: owner.sessionID, DeviceID: owner.deviceID, Fence: 0, State: "active"})
	if _, ok := owners.markAccepted(owner); !ok {
		t.Fatal("owner acceptance publication failed")
	}
	intent := remotedevice.ClosureIntent{ID: "closure-sequence", DeviceID: owner.deviceID, SessionID: owner.sessionID, Fence: 1, State: "pending"}
	owner.outboundSequence = maxWireSequence
	if _, status := owners.reserveRevocationDelivery(owner, intent, remotedevice.Actor{ID: "operator", PolicyID: "policy"}); status == revocationDeliveryReserved {
		t.Fatal("outbound sequence wrapped at protocol maximum")
	}
	owner.outboundSequence = 0
	delivery, status := owners.reserveRevocationDelivery(owner, intent, remotedevice.Actor{ID: "operator", PolicyID: "policy"})
	if status != revocationDeliveryReserved || delivery.sequence != 1 {
		t.Fatalf("first outbound delivery = %+v status=%d", delivery, status)
	}
	if _, ok := owners.beginRevocationAcknowledgement(owner, 1, intent.ID, intent.Fence); ok {
		t.Fatal("premature acknowledgement became eligible before write publication")
	}
	if !owners.markRevocationDelivered(owner, delivery) {
		t.Fatal("successful write was not published to owner state")
	}
	owner.inboundSequence = maxWireSequence
	if _, ok := owners.beginRevocationAcknowledgement(owner, maxWireSequence, intent.ID, intent.Fence); ok {
		t.Fatal("inbound sequence wrapped at protocol maximum")
	}
}

func TestRevocationWriteFailureNeverPublishesPrematureAcknowledgement(t *testing.T) {
	serverConnections := make(chan *websocket.Conn, 1)
	releaseServer := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnections <- conn
		<-releaseServer
		_ = conn.Close()
	}))
	defer func() {
		close(releaseServer)
		server.Close()
	}()
	client, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	serverConn := <-serverConnections

	handler := NewHandler(&recordingAuthenticator{}, PeerGateFunc(func(context.Context, string) bool { return true }))
	owner, ok := handler.owners.reserve("session-write-failure", "device-write-failure", serverConn)
	if !ok {
		t.Fatal("owner reservation failed")
	}
	handler.owners.activate(owner, remotedevice.Session{ID: owner.sessionID, DeviceID: owner.deviceID, Fence: 0, State: "active"})
	if _, ok := handler.owners.markAccepted(owner); !ok {
		t.Fatal("owner acceptance publication failed")
	}
	if err := serverConn.Close(); err != nil {
		t.Fatal(err)
	}
	intent := remotedevice.ClosureIntent{ID: "closure-write-failure", DeviceID: owner.deviceID, SessionID: owner.sessionID, Fence: 1, State: "pending", RevocationID: "revoke-write-failure", Reason: remotedevice.RevocationReasonAdministrator, CreatedAt: time.UnixMilli(1)}
	if outcome := handler.deliverRevocation(owner, intent, remotedevice.Actor{ID: "operator", PolicyID: "policy"}); outcome.status != revocationDeliveryFailed {
		t.Fatal("closed socket write unexpectedly succeeded")
	}
	handler.failRevocationOwner(owner, CloseReasonTransport)
	handler.owners.mu.Lock()
	pending := owner.pending
	closed := owner.closed
	terminal := owner.terminal
	handler.owners.mu.Unlock()
	if pending != nil || !closed || terminal != ownerTerminalRevocationPending {
		t.Fatalf("failed write state = pending=%v closed=%v terminal=%d", pending != nil, closed, terminal)
	}
	if _, ok := handler.owners.beginRevocationAcknowledgement(owner, 1, intent.ID, intent.Fence); ok {
		t.Fatal("acknowledgement became eligible after failed write")
	}
}

func TestHandlerCloseRacingAdmissionClosesAndCleansReservedOwner(t *testing.T) {
	material := newTLSMaterial(t)
	release := make(chan struct{})
	auth := &recordingAuthenticator{entered: make(chan struct{}), release: release, closed: make(chan remotedevice.CloseSessionRequest, 1)}
	server, offer, _ := configuredHandler(t, material, auth)
	conn := dialTLS(t, server, material, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
	if err := conn.WriteMessage(websocket.TextMessage, offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-auth.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("admission did not reach its barrier")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- server.Config.Handler.(*Handler).Close() }()
	readCloseWithCode(t, conn, websocket.ClosePolicyViolation, CloseReasonTransport)
	closeReturned := false
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
		closeReturned = true
	case <-time.After(2 * time.Second):
		t.Fatal("Handler.Close did not close the reserved owner")
	}
	close(release)
	if !closeReturned {
		select {
		case err := <-closeDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Handler.Close did not complete after admission release")
		}
	}
	select {
	case request := <-auth.closed:
		if request.SessionID != "session-1" || request.Reason != remotedevice.SessionCloseReasonAdministrator {
			t.Fatalf("cleanup request = %+v", request)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reserved owner was not durably cleaned")
	}
}
