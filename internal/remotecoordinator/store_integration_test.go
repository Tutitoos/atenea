package remotecoordinator

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Tutitoos/atenea/internal/remotedevice"
	"github.com/Tutitoos/atenea/pkg/remoteprotocol"
)

type integrationClock struct {
	now time.Time
}

func (c *integrationClock) Now() time.Time { return c.now }

type canonicalMetadataIssuer struct {
	mu          sync.Mutex
	calls       int
	clock       *integrationClock
	fingerprint string
	notAfter    time.Time
}

type admissionBarrierStore struct {
	*remotedevice.Store
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

type applyRecordingStore struct {
	*remotedevice.Store
	entered chan struct{}
	release <-chan struct{}
	err     error
	request chan remotedevice.ApplyClosureIntentRequest
	once    sync.Once
}

func (s *applyRecordingStore) ApplyClosureIntent(ctx context.Context, request remotedevice.ApplyClosureIntentRequest) (remotedevice.ApplyClosureIntentResponse, error) {
	if s.request != nil {
		s.request <- request
	}
	if s.entered != nil {
		s.once.Do(func() { close(s.entered) })
	}
	if s.release != nil {
		<-s.release
	}
	if s.err != nil {
		return remotedevice.ApplyClosureIntentResponse{}, s.err
	}
	return s.Store.ApplyClosureIntent(ctx, request)
}

func (s *admissionBarrierStore) AuthenticateAndRegisterSession(ctx context.Context, request remotedevice.AuthenticateAndRegisterSessionRequest) (remotedevice.Session, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.Store.AuthenticateAndRegisterSession(ctx, request)
}

func (i *canonicalMetadataIssuer) Issue(_ context.Context, request remotedevice.CertificateIssueRequest) (remotedevice.CertificateMetadata, error) {
	i.mu.Lock()
	i.calls++
	certificateNumber := i.calls
	i.mu.Unlock()
	return remotedevice.CertificateMetadata{
		ID:              fmt.Sprintf("cert-%d", certificateNumber),
		IssuerID:        "test-issuer",
		Serial:          fmt.Sprintf("serial-%d", certificateNumber),
		Fingerprint:     i.fingerprint,
		PublicKeyDigest: append([]byte(nil), request.PublicKeyDigest...),
		NotBefore:       i.clock.Now().Add(-time.Minute),
		NotAfter:        i.notAfter,
	}, nil
}

func seedActiveStore(t *testing.T, material testTLSMaterial, certificateNotAfter time.Time) (*remotedevice.Store, *integrationClock, remotedevice.Device) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	clock := &integrationClock{now: now}
	fingerprint := sha256.Sum256(material.clientCert.Raw)
	issuer := &canonicalMetadataIssuer{clock: clock, fingerprint: hex.EncodeToString(fingerprint[:]), notAfter: certificateNotAfter}
	databaseDirectory := filepath.Join(t.TempDir(), "registry")
	if err := os.Mkdir(databaseDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := remotedevice.Open(context.Background(), filepath.Join(databaseDirectory, "devices.sqlite"), remotedevice.WithClock(clock.Now), remotedevice.WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x41}, 512))), remotedevice.WithCertificateIssuer(issuer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	enrollment, err := store.CreateEnrollment(context.Background(), remotedevice.CreateEnrollmentRequest{ID: "enrollment-1", DeviceID: "device-1", Name: "integration-agent", Platform: remotedevice.PlatformLinux, Architecture: remotedevice.ArchitectureX8664, Actor: remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}})
	if err != nil {
		t.Fatal(err)
	}
	clientPublic := material.clientTLS.PrivateKey.(ed25519.PrivateKey).Public().(ed25519.PublicKey)
	spki, err := x509.MarshalPKIXPublicKey(clientPublic)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := store.IssueChallenge(context.Background(), remotedevice.IssueChallengeRequest{EnrollmentID: enrollment.ID, DeviceID: enrollment.DeviceID, Name: enrollment.Name, Platform: enrollment.Platform, Architecture: enrollment.Architecture, Token: enrollment.Token, PublicKeySPKI: spki, Context: []byte("integration-context"), IdempotencyKey: "issue-1"})
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(material.clientTLS.PrivateKey.(ed25519.PrivateKey), canonicalEnrollmentProof(challenge, enrollment))
	device, err := store.CompleteEnrollment(context.Background(), remotedevice.CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signature})
	if err != nil {
		t.Fatal(err)
	}
	return store, clock, device
}

func enrollAdditionalDevice(t *testing.T, store *remotedevice.Store, material testTLSMaterial, enrollmentID, deviceID string) remotedevice.Device {
	t.Helper()
	enrollment, err := store.CreateEnrollment(context.Background(), remotedevice.CreateEnrollmentRequest{
		ID: enrollmentID, DeviceID: deviceID, Name: "integration-agent-" + deviceID,
		Platform: remotedevice.PlatformLinux, Architecture: remotedevice.ArchitectureX8664,
		Actor: remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	clientPublic := material.clientTLS.PrivateKey.(ed25519.PrivateKey).Public().(ed25519.PublicKey)
	spki, err := x509.MarshalPKIXPublicKey(clientPublic)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := store.IssueChallenge(context.Background(), remotedevice.IssueChallengeRequest{
		EnrollmentID: enrollment.ID, DeviceID: enrollment.DeviceID, Name: enrollment.Name,
		Platform: enrollment.Platform, Architecture: enrollment.Architecture, Token: enrollment.Token,
		PublicKeySPKI: spki, Context: []byte("integration-context-" + deviceID), IdempotencyKey: "issue-" + deviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(material.clientTLS.PrivateKey.(ed25519.PrivateKey), canonicalEnrollmentProof(challenge, enrollment))
	device, err := store.CompleteEnrollment(context.Background(), remotedevice.CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signature})
	if err != nil {
		t.Fatal(err)
	}
	return device
}

func canonicalEnrollmentProof(challenge remotedevice.Challenge, enrollment remotedevice.CreateEnrollmentResponse) []byte {
	fields := [][]byte{
		[]byte(remotedevice.ProtocolVersion),
		[]byte(challenge.ID), []byte(enrollment.ID), []byte(enrollment.DeviceID), []byte(enrollment.Name),
		[]byte(enrollment.Platform), []byte(enrollment.Architecture),
		[]byte(fmt.Sprint(enrollment.ExpiresAt.UnixNano())), []byte(fmt.Sprint(challenge.IssuedAt.UnixNano())), []byte(fmt.Sprint(challenge.ExpiresAt.UnixNano())),
		challenge.NonceDigest, challenge.OriginalContextDigest, challenge.PublicKeySPKI, challenge.PublicKeyDigest,
	}
	message := append([]byte("atenea.remote.enrollment-proof.v1\x00"), nil...)
	for _, field := range fields {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		message = append(message, length[:]...)
		message = append(message, field...)
	}
	return message
}

func startStoreServer(t *testing.T, material testTLSMaterial, store *remotedevice.Store, opts ...Option) *httptest.Server {
	t.Helper()
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }), opts...)
	return startTLSServer(t, handler, material)
}

func dialWithCertificate(t *testing.T, server *httptest.Server, material testTLSMaterial, clientCertificate tls.Certificate) *websocket.Conn {
	t.Helper()
	url := "wss" + strings.TrimPrefix(server.URL, "https") + ConnectPath
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: material.caPool, Certificates: []tls.Certificate{clientCertificate}, ServerName: "localhost", MinVersion: tls.VersionTLS13}}
	conn, response, err := dialer.Dial(url, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func assertNoDurableSession(t *testing.T, store *remotedevice.Store) {
	t.Helper()
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventKind == remotedevice.EventSessionRegistered {
			t.Fatalf("durable session audit exists: %+v", event)
		}
	}
}

func assertDurableSession(t *testing.T, store *remotedevice.Store, sessionID string) {
	t.Helper()
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventKind == remotedevice.EventSessionRegistered && event.SessionID == sessionID {
			return
		}
	}
	t.Fatalf("durable session audit for %q not found", sessionID)
}

func readClose(t *testing.T, conn *websocket.Conn, wantCode int, wantReason string) {
	t.Helper()
	_, _, err := conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != wantCode || closeErr.Text != wantReason {
		t.Fatalf("close = %v, want code=%d reason=%q", err, wantCode, wantReason)
	}
}

type revokedEventInfo struct {
	Protocol, Version, MessageType, SessionID, DeviceID string
	Sequence                                            uint64
	SentAt                                              int64
	EventID, EventType                                  string
	OccurredAt                                          int64
	Kind, Scope, RevocationID, Reason                   string
	EffectiveAt, Fence                                  int64
}

func readRevokedEvent(t *testing.T, conn *websocket.Conn) revokedEventInfo {
	t.Helper()
	messageType, raw, err := conn.ReadMessage()
	if err != nil || messageType != websocket.TextMessage {
		t.Fatalf("revoked event read: type=%d err=%v", messageType, err)
	}
	if err := remoteprotocol.New().Validate(raw); err != nil {
		t.Fatalf("revoked event validation: %v", err)
	}
	var event struct {
		Protocol  string `json:"protocol"`
		Version   string `json:"version"`
		Type      string `json:"message_type"`
		SessionID string `json:"session_id"`
		DeviceID  string `json:"device_id"`
		Sequence  uint64 `json:"sequence"`
		SentAt    int64  `json:"sent_at"`
		Payload   struct {
			EventID    string `json:"event_id"`
			EventType  string `json:"event_type"`
			OccurredAt int64  `json:"occurred_at"`
			Data       struct {
				Kind         string `json:"kind"`
				Scope        string `json:"scope"`
				RevocationID string `json:"revocation_id"`
				Reason       string `json:"reason"`
				EffectiveAt  int64  `json:"effective_at"`
				Fence        int64  `json:"fence"`
			} `json:"data"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if event.Protocol != remoteprotocol.Subprotocol || event.Version != remotedevice.ProtocolVersion || event.Type != "event" || event.SessionID == "" || event.DeviceID == "" || event.Payload.EventID == "" || event.Payload.EventType != "revoked" || event.Payload.OccurredAt < 0 || event.Payload.Data.Kind != "revoked" || event.Payload.Data.Scope != "session" || event.Payload.Data.RevocationID == "" || event.Payload.Data.Reason == "" || event.Payload.Data.EffectiveAt < 0 || event.Payload.Data.Fence < 1 {
		t.Fatalf("revoked event fields are not exact: %+v", event)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if _, ok := envelope["request_id"]; ok {
		t.Fatal("revoked event unexpectedly contains request_id")
	}
	return revokedEventInfo{Protocol: event.Protocol, Version: event.Version, MessageType: event.Type, SessionID: event.SessionID, DeviceID: event.DeviceID, Sequence: event.Sequence, SentAt: event.SentAt, EventID: event.Payload.EventID, EventType: event.Payload.EventType, OccurredAt: event.Payload.OccurredAt, Kind: event.Payload.Data.Kind, Scope: event.Payload.Data.Scope, RevocationID: event.Payload.Data.RevocationID, Reason: event.Payload.Data.Reason, EffectiveAt: event.Payload.Data.EffectiveAt, Fence: event.Payload.Data.Fence}
}

func revokedEventAckJSON(sessionID, deviceID, eventID string, sequence uint64, fence int64) []byte {
	ack := map[string]any{
		"protocol": remoteprotocol.Subprotocol, "version": remotedevice.ProtocolVersion,
		"message_type": "event_ack", "session_id": sessionID, "device_id": deviceID,
		"sequence": sequence, "sent_at": 1,
		"payload": map[string]any{"kind": "event_ack", "event_type": "revoked", "event_id": eventID, "fence": fence, "acknowledged_at": 1},
	}
	raw, _ := json.Marshal(ack)
	return raw
}

func TestStoreBackedWSSAcceptAndDenialsHaveNoDurableSession(t *testing.T) {
	tests := []struct {
		name            string
		setup           func(*testing.T, testTLSMaterial, *remotedevice.Store, *integrationClock, remotedevice.Device) tls.Certificate
		mutateOffer     func([]byte) []byte
		wantCloseReason string
	}{
		{name: "accept", wantCloseReason: CloseReasonAccepted},
		{name: "unknown certificate", setup: func(t *testing.T, material testTLSMaterial, _ *remotedevice.Store, _ *integrationClock, _ remotedevice.Device) tls.Certificate {
			unknown, _ := signedTLSCertificate(t, material.caCert, material.caPrivate, 9, x509.ExtKeyUsageClientAuth, nil)
			return unknown
		}, wantCloseReason: CloseReasonAuthentication},
		{name: "expired certificate metadata", setup: func(_ *testing.T, _ testTLSMaterial, _ *remotedevice.Store, clock *integrationClock, _ remotedevice.Device) tls.Certificate {
			clock.now = clock.now.Add(2 * time.Minute)
			return tls.Certificate{}
		}, wantCloseReason: CloseReasonAuthentication},
		{name: "mismatched device", mutateOffer: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"device_id":"device-1"`), []byte(`"device_id":"device-other"`), 1)
		}, wantCloseReason: CloseReasonAuthentication},
		{name: "revoked", setup: func(t *testing.T, material testTLSMaterial, store *remotedevice.Store, _ *integrationClock, _ remotedevice.Device) tls.Certificate {
			if _, err := store.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: remotedevice.RevocationReasonAdministrator}); err != nil {
				t.Fatal(err)
			}
			return material.clientTLS
		}, wantCloseReason: CloseReasonAuthentication},
		{name: "superseded", setup: func(t *testing.T, material testTLSMaterial, store *remotedevice.Store, clock *integrationClock, device remotedevice.Device) tls.Certificate {
			_, renewedCert := signedTLSCertificate(t, material.caCert, material.caPrivate, 10, x509.ExtKeyUsageClientAuth, nil)
			fingerprint := sha256.Sum256(renewedCert.Raw)
			_, err := store.RecordCertificateRenewal(context.Background(), remotedevice.RecordCertificateRenewalRequest{DeviceID: device.ID, PreviousCertificateID: device.Certificate.ID, Metadata: remotedevice.CertificateMetadata{ID: "cert-2", IssuerID: "test-issuer", Serial: "serial-2", Fingerprint: hex.EncodeToString(fingerprint[:]), PublicKeyDigest: append([]byte(nil), device.Certificate.PublicKeyDigest...), NotBefore: clock.Now().Add(-time.Minute), NotAfter: clock.Now().Add(time.Hour)}, Actor: remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}})
			if err != nil {
				t.Fatal(err)
			}
			return material.clientTLS
		}, wantCloseReason: CloseReasonAuthentication},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			material := newTLSMaterial(t)
			certificateNotAfter := time.Now().UTC().Add(time.Hour)
			if test.name == "expired certificate metadata" {
				certificateNotAfter = time.Now().UTC().Add(time.Minute)
			}
			store, clock, device := seedActiveStore(t, material, certificateNotAfter)
			server := startStoreServer(t, material, store)
			clientCertificate := material.clientTLS
			if test.setup != nil {
				if candidate := test.setup(t, material, store, clock, device); len(candidate.Certificate) > 0 {
					clientCertificate = candidate
				}
			}
			conn := dialWithCertificate(t, server, material, clientCertificate)
			offer := validOfferJSON("device-1", "session-1", "linux", "x86_64")
			if test.mutateOffer != nil {
				offer = test.mutateOffer(offer)
			}
			if err := conn.WriteMessage(websocket.TextMessage, offer); err != nil {
				t.Fatal(err)
			}
			if test.name == "accept" {
				messageType, raw, err := conn.ReadMessage()
				if err != nil || messageType != websocket.TextMessage {
					t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
				}
				if err := remoteprotocol.New().Validate(raw); err != nil {
					t.Fatalf("acceptance validation: %v", err)
				}
				// The coordinator keeps an admitted owner until the peer closes or
				// a revocation intent closes it. End this fixture explicitly.
				if err := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				assertDurableSession(t, store, "session-1")
			} else {
				readClose(t, conn, websocket.ClosePolicyViolation, test.wantCloseReason)
			}
			if test.name != "accept" {
				assertNoDurableSession(t, store)
			}
		})
	}
}

func TestStoreBackedConcurrentWSSHandshakes(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	server := startStoreServer(t, material, store)
	const clients = 12
	errCh := make(chan error, clients)
	for index := 0; index < clients; index++ {
		go func() {
			conn, response, err := (&websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: material.caPool, Certificates: []tls.Certificate{material.clientTLS}, ServerName: "localhost", MinVersion: tls.VersionTLS13}}).Dial("wss"+strings.TrimPrefix(server.URL, "https")+ConnectPath, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
			if err != nil {
				if response != nil {
					_ = response.Body.Close()
				}
				errCh <- err
				return
			}
			defer conn.Close()
			if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", fmt.Sprintf("session-%d", index), "linux", "x86_64")); err != nil {
				errCh <- err
				return
			}
			messageType, raw, err := conn.ReadMessage()
			if err != nil || messageType != websocket.TextMessage {
				errCh <- fmt.Errorf("acceptance: type=%d err=%v", messageType, err)
				return
			}
			errCh <- remoteprotocol.New().Validate(raw)
		}()
	}
	for index := 0; index < clients; index++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent handshake %d: %v", index, err)
		}
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	registered := 0
	for _, event := range events {
		if event.EventKind == remotedevice.EventSessionRegistered {
			registered++
		}
	}
	if registered != clients {
		t.Fatalf("session registrations = %d, want %d", registered, clients)
	}
}

func TestStoreBackedConcurrentDuplicateSessionIDHasOneAcceptance(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	admissionEntered := make(chan struct{})
	releaseAdmission := make(chan struct{})
	cleanupDone := make(chan error, 1)
	authenticator := &admissionBarrierStore{Store: store, entered: admissionEntered, release: releaseAdmission}
	handler := NewHandler(authenticator, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }), WithSessionCleanupHook(func(_ remotedevice.Session, cleanupErr error) {
		cleanupDone <- cleanupErr
	}))
	server := startTLSServer(t, handler, material)
	const attempts = 2
	type result struct {
		accepted bool
		err      error
	}
	results := make(chan result, attempts)
	dialReady := make(chan struct{}, attempts)
	releaseOffers := make(chan struct{})
	var releaseOffersOnce sync.Once
	releaseOffersNow := func() { releaseOffersOnce.Do(func() { close(releaseOffers) }) }
	defer releaseOffersNow()
	var releaseAdmissionOnce sync.Once
	releaseAdmissionNow := func() { releaseAdmissionOnce.Do(func() { close(releaseAdmission) }) }
	defer releaseAdmissionNow()
	winnerAccepted := make(chan struct{})
	loserRejected := make(chan struct{})
	for index := 0; index < attempts; index++ {
		go func() {
			url := "wss" + strings.TrimPrefix(server.URL, "https") + ConnectPath
			dialer := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: material.caPool, Certificates: []tls.Certificate{material.clientTLS}, ServerName: "localhost", MinVersion: tls.VersionTLS13}}
			conn, response, err := dialer.Dial(url, http.Header{"Sec-WebSocket-Protocol": []string{remoteprotocol.Subprotocol}})
			if err != nil {
				if response != nil {
					_ = response.Body.Close()
				}
				results <- result{err: err}
				return
			}
			defer conn.Close()
			dialReady <- struct{}{}
			<-releaseOffers
			if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-duplicate", "linux", "x86_64")); err != nil {
				results <- result{err: err}
				return
			}
			messageType, raw, readErr := conn.ReadMessage()
			if readErr == nil && messageType == websocket.TextMessage {
				if validateErr := remoteprotocol.New().Validate(raw); validateErr != nil {
					results <- result{err: validateErr}
					return
				}
				close(winnerAccepted)
				<-loserRejected
				if err := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
					results <- result{err: err}
					return
				}
				results <- result{accepted: true}
				return
			}
			var closeErr *websocket.CloseError
			if !errors.As(readErr, &closeErr) || closeErr.Code != websocket.ClosePolicyViolation || closeErr.Text != CloseReasonPolicy {
				results <- result{err: fmt.Errorf("loser close = %v", readErr)}
				return
			}
			close(loserRejected)
			results <- result{}
		}()
	}
	for index := 0; index < attempts; index++ {
		select {
		case <-dialReady:
		case <-time.After(2 * time.Second):
			t.Fatal("duplicate connections did not reach the dial barrier")
		}
	}
	releaseOffersNow()
	select {
	case <-admissionEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("winner did not reach the admission barrier")
	}
	select {
	case <-loserRejected:
	case <-time.After(2 * time.Second):
		t.Fatal("simultaneous loser did not receive policy close")
	}
	releaseAdmissionNow()
	select {
	case <-winnerAccepted:
	case <-time.After(2 * time.Second):
		t.Fatal("winner did not receive acceptance")
	}
	accepted := 0
	for index := 0; index < attempts; index++ {
		outcome := <-results
		if outcome.err != nil {
			t.Fatalf("duplicate connection %d: %v", index, outcome.err)
		}
		if outcome.accepted {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted connections = %d, want exactly one", accepted)
	}
	select {
	case cleanupErr := <-cleanupDone:
		if cleanupErr != nil {
			t.Fatalf("winner cleanup: %v", cleanupErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("winner cleanup did not complete")
	}
	replay := dialWithCertificate(t, server, material, material.clientTLS)
	if err := replay.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-duplicate", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	readClose(t, replay, websocket.ClosePolicyViolation, CloseReasonAuthentication)
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	registrations := 0
	for _, event := range events {
		if event.EventKind == remotedevice.EventSessionRegistered && event.SessionID == "session-duplicate" {
			registrations++
		}
	}
	if registrations != 1 {
		t.Fatalf("session.registered events = %d, want one", registrations)
	}
}

func TestStoreBackedRevocationClosesOnlyLinkedOwner(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-revocation", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	messageType, raw, err := conn.ReadMessage()
	if err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	if err := remoteprotocol.New().Validate(raw); err != nil {
		t.Fatalf("acceptance validation: %v", err)
	}
	result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-session"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Intents) != 1 || result.Intents[0].SessionID != "session-revocation" || result.Intents[0].State != "pending" {
		t.Fatalf("revocation intents = %+v", result.Intents)
	}
	event := readRevokedEvent(t, conn)
	if event.Sequence != 1 || event.EventID != result.Intents[0].ID || event.DeviceID != "device-1" || event.SessionID != "session-revocation" || event.RevocationID != result.Revocation.ID || event.Reason != string(result.Intents[0].Reason) || event.Fence != result.Intents[0].Fence || event.EffectiveAt != event.OccurredAt {
		t.Fatalf("revoked event = %+v", event)
	}
	if err := conn.WriteMessage(websocket.TextMessage, revokedEventAckJSON("session-revocation", "device-1", event.EventID, 1, event.Fence)); err != nil {
		t.Fatal(err)
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonDeviceRevoked)

	intents, err := store.PendingClosureIntents(context.Background(), remotedevice.ClosureIntentFilter{DeviceID: "device-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 0 {
		t.Fatalf("pending intents = %+v", intents)
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var applied, closed int
	for _, event := range events {
		if event.EventKind == remotedevice.EventClosureApplied {
			applied++
		}
		if event.EventKind == remotedevice.EventSessionClosed && event.SessionID == "session-revocation" && event.Reason == remotedevice.ReasonRevoked {
			closed++
		}
	}
	if applied != 1 || closed != 1 {
		t.Fatalf("durable acknowledgement evidence applied=%d closed=%d", applied, closed)
	}

	reconnect := dialWithCertificate(t, server, material, material.clientTLS)
	if err := reconnect.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-reconnect", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	readClose(t, reconnect, websocket.ClosePolicyViolation, CloseReasonAuthentication)
}

func TestStoreBackedImmediateAcknowledgementWaitsForDeliveryPublication(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	writeEntered := make(chan struct{})
	releaseWrite := make(chan struct{})
	var writeOnce sync.Once
	applyEntered := make(chan struct{})
	authenticator := &applyRecordingStore{Store: store, entered: applyEntered}
	handler := NewHandler(authenticator, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
	handler.options.afterRevocationWrite = func() {
		writeOnce.Do(func() { close(writeEntered) })
		<-releaseWrite
	}
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-immediate-ack", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	revokeDone := make(chan error, 1)
	go func() {
		_, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-immediate-ack"})
		revokeDone <- err
	}()
	select {
	case <-writeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("revocation did not reach in-flight write barrier")
	}
	event := readRevokedEvent(t, conn)
	if err := conn.WriteMessage(websocket.TextMessage, revokedEventAckJSON("session-immediate-ack", "device-1", event.EventID, 1, event.Fence)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-applyEntered:
		t.Fatal("ACK applied before delivery publication")
	default:
	}
	close(releaseWrite)
	select {
	case err := <-revokeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revocation did not finish after write publication")
	}
	select {
	case <-applyEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("legitimate immediate ACK did not reach apply")
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonDeviceRevoked)
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreBackedRevocationWaitsForAcceptancePublication(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	acceptEntered := make(chan struct{})
	releaseAccept := make(chan struct{})
	var acceptOnce sync.Once
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
	handler.options.beforeAcceptanceWrite = func() {
		acceptOnce.Do(func() { close(acceptEntered) })
		<-releaseAccept
	}
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-accept-order", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-acceptEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("acceptance did not reach deterministic barrier")
	}
	owner := func() *sessionOwner {
		handler.owners.mu.Lock()
		defer handler.owners.mu.Unlock()
		return handler.owners.owners["session-accept-order"]
	}()
	if owner == nil {
		t.Fatal("owner was not reserved before acceptance")
	}
	handler.owners.mu.Lock()
	acceptedBeforeWrite := owner.accepted
	queuedBeforeWrite := owner.queued
	handler.owners.mu.Unlock()
	if acceptedBeforeWrite || queuedBeforeWrite != nil {
		t.Fatalf("owner state before accept accepted=%v queued=%+v", acceptedBeforeWrite, queuedBeforeWrite)
	}
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	revokeDone := make(chan remotedevice.RevokeDeviceResponse, 1)
	revokeErr := make(chan error, 1)
	go func() {
		result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-accept-order"})
		revokeDone <- result
		revokeErr <- err
	}()
	var result remotedevice.RevokeDeviceResponse
	select {
	case result = <-revokeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("revocation did not complete while acceptance was paused")
	}
	if err := <-revokeErr; err != nil || len(result.Intents) != 1 {
		t.Fatalf("revocation result = %+v err=%v", result, err)
	}
	handler.owners.mu.Lock()
	acceptedAfterRevoke := owner.accepted
	queuedAfterRevoke := owner.queued
	pendingAfterRevoke := owner.pending
	handler.owners.mu.Unlock()
	if acceptedAfterRevoke || queuedAfterRevoke == nil || pendingAfterRevoke != nil {
		t.Fatalf("owner state while accept paused accepted=%v queued=%+v pending=%+v", acceptedAfterRevoke, queuedAfterRevoke, pendingAfterRevoke)
	}
	close(releaseAccept)
	messageType, raw, err := conn.ReadMessage()
	if err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	var acceptance struct {
		Sequence uint64 `json:"sequence"`
	}
	if err := json.Unmarshal(raw, &acceptance); err != nil {
		t.Fatal(err)
	}
	if acceptance.Sequence != 0 {
		t.Fatalf("acceptance sequence = %d, want 0", acceptance.Sequence)
	}
	event := readRevokedEvent(t, conn)
	if event.Sequence != 1 || event.EventID != result.Intents[0].ID || event.SessionID != "session-accept-order" || event.DeviceID != "device-1" {
		t.Fatalf("post-accept revoked event = %+v", event)
	}
	if err := conn.WriteMessage(websocket.TextMessage, revokedEventAckJSON("session-accept-order", "device-1", event.EventID, 1, event.Fence)); err != nil {
		t.Fatal(err)
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonDeviceRevoked)
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreBackedAcceptanceWriteFailureNeverPublishesOwner(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	acceptEntered := make(chan struct{})
	releaseAccept := make(chan struct{})
	var acceptOnce sync.Once
	var releaseOnce sync.Once
	cleanup := make(chan remotedevice.Session, 1)
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }), WithSessionCleanupHook(func(session remotedevice.Session, cleanupErr error) {
		if cleanupErr == nil {
			cleanup <- session
		}
	}))
	handler.options.beforeAcceptanceWrite = func() {
		acceptOnce.Do(func() { close(acceptEntered) })
		<-releaseAccept
	}
	defer func() { releaseOnce.Do(func() { close(releaseAccept) }) }()
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-accept-write-failure", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-acceptEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("acceptance did not reach deterministic barrier")
	}
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	revokeDone := make(chan struct {
		result remotedevice.RevokeDeviceResponse
		err    error
	}, 1)
	go func() {
		result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-accept-write-failure"})
		revokeDone <- struct {
			result remotedevice.RevokeDeviceResponse
			err    error
		}{result: result, err: err}
	}()
	var revoke struct {
		result remotedevice.RevokeDeviceResponse
		err    error
	}
	select {
	case revoke = <-revokeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("revocation did not complete while acceptance was paused")
	}
	if revoke.err != nil || len(revoke.result.Intents) != 1 {
		t.Fatalf("revocation result = %+v err=%v", revoke.result, revoke.err)
	}
	handler.owners.mu.Lock()
	owner := handler.owners.owners["session-accept-write-failure"]
	accepted := owner != nil && owner.accepted
	queued := owner != nil && owner.queued != nil
	handler.owners.mu.Unlock()
	if owner == nil || accepted || !queued {
		t.Fatalf("owner before failed accept = present=%v accepted=%v queued=%v", owner != nil, accepted, queued)
	}
	// Closing the server-side transport makes the subsequent accept
	// WriteMessage fail deterministically instead of allowing the kernel to
	// buffer a frame after the peer has gone away.
	if err := owner.conn.Close(); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(releaseAccept) })
	select {
	case session := <-cleanup:
		if session.ID != "session-accept-write-failure" {
			t.Fatalf("cleaned session = %+v", session)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed acceptance did not terminate and clean up")
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	intents, err := store.PendingClosureIntents(context.Background(), remotedevice.ClosureIntentFilter{DeviceID: "device-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].State != "pending" {
		t.Fatalf("failed acceptance changed intent: %+v", intents)
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range events {
		if audit.EventKind == remotedevice.EventClosureApplied {
			t.Fatalf("failed acceptance produced closure.applied audit: %+v", audit)
		}
	}
}

func TestStoreBackedRevocationAcknowledgementPreservesSessionClosedBetweenEventAndAck(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-close-between", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-close-between"})
	if err != nil || len(result.Intents) != 1 {
		t.Fatalf("revoke result = %+v err=%v", result, err)
	}
	event := readRevokedEvent(t, conn)
	closed, err := store.CloseSession(context.Background(), remotedevice.CloseSessionRequest{
		SessionID: "session-close-between", DeviceID: "device-1", Actor: actor,
		Reason: remotedevice.SessionCloseReasonAdministrator, RequestID: "close-between",
	})
	if err != nil || closed.State != "closed" || closed.Reason != remotedevice.SessionCloseReasonAdministrator || closed.ClosedAt.IsZero() {
		t.Fatalf("session closed between event and ack = %+v err=%v", closed, err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, revokedEventAckJSON("session-close-between", "device-1", event.EventID, 1, event.Fence)); err != nil {
		t.Fatal(err)
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonDeviceRevoked)
	intents, err := store.PendingClosureIntents(context.Background(), remotedevice.ClosureIntentFilter{DeviceID: "device-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 0 {
		t.Fatalf("closed-between intent remains pending: %+v", intents)
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var administratorClosed, revokedClosed, applied int
	for _, audit := range events {
		if audit.EventKind == remotedevice.EventSessionClosed && audit.SessionID == "session-close-between" {
			switch audit.Reason {
			case remotedevice.ReasonAdministrator:
				administratorClosed++
			case remotedevice.ReasonRevoked:
				revokedClosed++
			}
			if audit.Actor != actor {
				t.Fatalf("session.closed actor = %+v, want original actor %+v", audit.Actor, actor)
			}
		}
		if audit.EventKind == remotedevice.EventClosureApplied && audit.SessionID == "session-close-between" {
			if audit.Actor != actor {
				t.Fatalf("closure.applied actor = %+v, want durable actor %+v", audit.Actor, actor)
			}
			applied++
		}
	}
	if administratorClosed != 1 || revokedClosed != 0 || applied != 1 {
		t.Fatalf("closed-between evidence administrator=%d revoked=%d applied=%d", administratorClosed, revokedClosed, applied)
	}
}

func TestStoreBackedRevocationAcknowledgementRejectsInvalidBindings(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func([]byte, uint64, string, int64) (int, []byte)
		wantCode   int
		wantReason string
	}{
		{name: "foreign event", wantCode: websocket.ClosePolicyViolation, wantReason: CloseReasonPolicy, mutate: func(_ []byte, sequence uint64, _ string, fence int64) (int, []byte) {
			return websocket.TextMessage, revokedEventAckJSON("session-revocation-invalid", "device-1", "closure-foreign", sequence, fence)
		}},
		{name: "foreign session", wantCode: websocket.ClosePolicyViolation, wantReason: CloseReasonPolicy, mutate: func(_ []byte, sequence uint64, eventID string, fence int64) (int, []byte) {
			return websocket.TextMessage, revokedEventAckJSON("session-foreign", "device-1", eventID, sequence, fence)
		}},
		{name: "foreign device", wantCode: websocket.ClosePolicyViolation, wantReason: CloseReasonPolicy, mutate: func(_ []byte, sequence uint64, eventID string, fence int64) (int, []byte) {
			return websocket.TextMessage, revokedEventAckJSON("session-revocation-invalid", "device-foreign", eventID, sequence, fence)
		}},
		{name: "out of order", wantCode: websocket.ClosePolicyViolation, wantReason: CloseReasonPolicy, mutate: func(_ []byte, _ uint64, eventID string, fence int64) (int, []byte) {
			return websocket.TextMessage, revokedEventAckJSON("session-revocation-invalid", "device-1", eventID, 2, fence)
		}},
		{name: "malformed", wantCode: websocket.ClosePolicyViolation, wantReason: CloseReasonEnvelope, mutate: func(_ []byte, _ uint64, _ string, _ int64) (int, []byte) {
			return websocket.TextMessage, []byte(`{"protocol":`)
		}},
		{name: "binary", wantCode: websocket.CloseUnsupportedData, wantReason: CloseReasonBinary, mutate: func(raw []byte, _ uint64, _ string, _ int64) (int, []byte) {
			return websocket.BinaryMessage, raw
		}},
		{name: "invalid utf8", wantCode: websocket.CloseInvalidFramePayloadData, wantReason: CloseReasonInvalidUTF8, mutate: func(_ []byte, _ uint64, _ string, _ int64) (int, []byte) {
			return websocket.TextMessage, []byte{0xc3, 0x28}
		}},
		{name: "oversized", wantCode: websocket.CloseMessageTooBig, wantReason: CloseReasonOversize, mutate: func(_ []byte, _ uint64, _ string, _ int64) (int, []byte) {
			return websocket.TextMessage, bytes.Repeat([]byte("x"), remoteprotocol.MaxControlMessageSize+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			material := newTLSMaterial(t)
			store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
			handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
			server := startTLSServer(t, handler, material)
			conn := dialWithCertificate(t, server, material, material.clientTLS)
			if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-revocation-invalid", "linux", "x86_64")); err != nil {
				t.Fatal(err)
			}
			if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
				t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
			}
			result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-invalid-" + strings.ReplaceAll(test.name, " ", "-")})
			if err != nil || len(result.Intents) != 1 {
				t.Fatalf("revoke result = %+v err=%v", result, err)
			}
			event := readRevokedEvent(t, conn)
			messageType, payload := test.mutate(revokedEventAckJSON("session-revocation-invalid", "device-1", event.EventID, 1, event.Fence), event.Sequence, event.EventID, event.Fence)
			if err := conn.WriteMessage(messageType, payload); err != nil {
				t.Fatal(err)
			}
			_, _, closeErr := conn.ReadMessage()
			var websocketClose *websocket.CloseError
			if !errors.As(closeErr, &websocketClose) {
				t.Fatalf("invalid acknowledgement close = %v", closeErr)
			}
			if websocketClose.Code != test.wantCode || websocketClose.Text != test.wantReason {
				t.Fatalf("%s close = code=%d reason=%q, want code=%d reason=%q", test.name, websocketClose.Code, websocketClose.Text, test.wantCode, test.wantReason)
			}
			intents, err := store.PendingClosureIntents(context.Background(), remotedevice.ClosureIntentFilter{DeviceID: "device-1", Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if len(intents) != 1 || intents[0].State != "pending" {
				t.Fatalf("invalid acknowledgement changed intents: %+v", intents)
			}
			events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.EventKind == remotedevice.EventClosureApplied || event.EventKind == remotedevice.EventSessionClosed {
					t.Fatalf("invalid acknowledgement wrote durable closure evidence: %+v", event)
				}
			}
		})
	}
}

func TestStoreBackedRevocationAcknowledgementTimeoutClosesIncompleteFrameAndStaysPending(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }), WithAcknowledgementTimeout(20*time.Millisecond))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-ack-timeout", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-ack-timeout"})
	if err != nil || len(result.Intents) != 1 {
		t.Fatalf("revoke result = %+v err=%v", result, err)
	}
	_ = readRevokedEvent(t, conn)
	// A non-FIN text frame gives Gorilla a message reader, then deliberately
	// withholds its continuation. The event deadline must bound ReadAll rather
	// than waiting indefinitely for the fragmented body.
	mask := [4]byte{0x11, 0x22, 0x33, 0x44}
	payload := byte('x') ^ mask[0]
	frame := []byte{0x01, 0x81, mask[0], mask[1], mask[2], mask[3], payload}
	if _, err := conn.UnderlyingConn().Write(frame); err != nil {
		t.Fatal(err)
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonTimeout)
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	intents, err := store.PendingClosureIntents(context.Background(), remotedevice.ClosureIntentFilter{DeviceID: "device-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].State != "pending" {
		t.Fatalf("timeout changed durable intent: %+v", intents)
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range events {
		if audit.EventKind == remotedevice.EventClosureApplied || audit.EventKind == remotedevice.EventSessionClosed {
			t.Fatalf("timeout wrote durable closure evidence: %+v", audit)
		}
	}
}

func TestStoreBackedRevocationAcknowledgementApplyUsesDurableActorAndBoundedRequest(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	requests := make(chan remotedevice.ApplyClosureIntentRequest, 1)
	authenticator := &applyRecordingStore{Store: store, request: requests}
	handler := NewHandler(authenticator, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }), WithSessionActor(remotedevice.Actor{ID: "different-coordinator", PolicyID: "different-policy"}))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-durable-actor", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-durable-actor"})
	if err != nil || len(result.Intents) != 1 {
		t.Fatalf("revoke result = %+v err=%v", result, err)
	}
	event := readRevokedEvent(t, conn)
	if err := conn.WriteMessage(websocket.TextMessage, revokedEventAckJSON("session-durable-actor", "device-1", event.EventID, 1, event.Fence)); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-requests:
		if request.Actor != actor {
			t.Fatalf("apply actor = %+v, want durable actor %+v", request.Actor, actor)
		}
		if len(request.RequestID) == 0 || len(request.RequestID) > 128 || strings.Contains(request.RequestID, "session-durable-actor") {
			t.Fatalf("internal request id = %q", request.RequestID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("apply was not invoked")
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonDeviceRevoked)
}

func TestStoreBackedPendingRevocationRedeliveryAdvancesOutboundOnly(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-redelivery", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	first, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-redelivery"})
	if err != nil || len(first.Intents) != 1 {
		t.Fatalf("first revoke = %+v err=%v", first, err)
	}
	firstEvent := readRevokedEvent(t, conn)
	second, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-redelivery-retry"})
	if err != nil || len(second.Intents) != 1 || second.Intents[0].State != "pending" {
		t.Fatalf("pending retry = %+v err=%v", second, err)
	}
	secondEvent := readRevokedEvent(t, conn)
	if firstEvent.Sequence != 1 || secondEvent.Sequence != 2 || firstEvent.EventID != secondEvent.EventID || firstEvent.Fence != secondEvent.Fence {
		t.Fatalf("redelivery sequence/event = (%+v), (%+v)", firstEvent, secondEvent)
	}
	// Inbound sequencing is independent from outbound redelivery: the first
	// acknowledgement slot remains sequence 1 even for outbound event 2.
	if err := conn.WriteMessage(websocket.TextMessage, revokedEventAckJSON("session-redelivery", "device-1", firstEvent.EventID, 1, firstEvent.Fence)); err != nil {
		t.Fatal(err)
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonDeviceRevoked)
	intents, err := store.PendingClosureIntents(context.Background(), remotedevice.ClosureIntentFilter{DeviceID: "device-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 0 {
		t.Fatalf("applied redelivery remains pending: %+v", intents)
	}
	historical, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-redelivery-history"})
	if err != nil || len(historical.Intents) != 1 || historical.Intents[0].State != "applied" {
		t.Fatalf("historical retry = %+v err=%v", historical, err)
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var applied, closed int
	for _, event := range events {
		if event.EventKind == remotedevice.EventClosureApplied {
			applied++
		}
		if event.EventKind == remotedevice.EventSessionClosed && event.SessionID == "session-redelivery" {
			closed++
		}
	}
	if applied != 1 || closed != 1 {
		t.Fatalf("historical retry duplicated evidence applied=%d closed=%d", applied, closed)
	}
}

func TestStoreBackedDuplicateAcknowledgementAppliesAndClosesOnce(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	entered := make(chan struct{})
	release := make(chan struct{})
	requests := make(chan remotedevice.ApplyClosureIntentRequest, 2)
	authenticator := &applyRecordingStore{Store: store, entered: entered, release: release, request: requests}
	handler := NewHandler(authenticator, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-duplicate-ack", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-duplicate-ack"})
	if err != nil || len(result.Intents) != 1 {
		t.Fatalf("revoke result = %+v err=%v", result, err)
	}
	event := readRevokedEvent(t, conn)
	ack := revokedEventAckJSON("session-duplicate-ack", "device-1", event.EventID, 1, event.Fence)
	if err := conn.WriteMessage(websocket.TextMessage, ack); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first acknowledgement did not reach apply")
	}
	// The durable apply is blocked, so this duplicate is admitted to the peer
	// socket before the first acknowledgement can close it. It must never start
	// a second apply or produce a second closure.
	if err := conn.WriteMessage(websocket.TextMessage, ack); err != nil {
		t.Fatal(err)
	}
	close(release)
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonDeviceRevoked)
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requests:
	default:
		t.Fatal("first apply request was not recorded")
	}
	select {
	case request := <-requests:
		t.Fatalf("duplicate acknowledgement started second apply: %+v", request)
	default:
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var applied, closed int
	for _, event := range events {
		if event.EventKind == remotedevice.EventClosureApplied && event.SessionID == "session-duplicate-ack" {
			applied++
		}
		if event.EventKind == remotedevice.EventSessionClosed && event.SessionID == "session-duplicate-ack" && event.Reason == remotedevice.ReasonRevoked {
			closed++
		}
	}
	if applied != 1 || closed != 1 {
		t.Fatalf("duplicate acknowledgement evidence applied=%d closed=%d", applied, closed)
	}
}

func TestStoreBackedRevocationReplaySkipsOwnerApplyingBeforeLifecycle(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	requests := make(chan remotedevice.ApplyClosureIntentRequest, 2)
	authenticator := &applyRecordingStore{Store: store, request: requests}
	handler := NewHandler(authenticator, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-replay-applying", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-replay-applying"})
	if err != nil || len(result.Intents) != 1 {
		t.Fatalf("revoke result = %+v err=%v", result, err)
	}
	event := readRevokedEvent(t, conn)
	handler.owners.mu.Lock()
	owner := handler.owners.owners["session-replay-applying"]
	handler.owners.mu.Unlock()
	if owner == nil {
		t.Fatal("replay owner not found")
	}
	delivery, ok := handler.owners.beginRevocationAcknowledgement(owner, 1, event.EventID, event.Fence)
	if !ok {
		t.Fatal("acknowledgement was not marked applying")
	}
	applyStarted := make(chan struct{})
	releaseApply := make(chan struct{})
	applyDone := make(chan bool, 1)
	go func() {
		close(applyStarted)
		<-releaseApply
		applyDone <- handler.applyRevocationAcknowledgement(context.Background(), owner, delivery)
	}()
	select {
	case <-applyStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("apply gate did not start")
	}
	replayDone := make(chan error, 1)
	go func() {
		_, replayErr := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-replay-applying-again"})
		replayDone <- replayErr
	}()
	select {
	case replayErr := <-replayDone:
		if replayErr != nil {
			t.Fatalf("idempotent replay: %v", replayErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idempotent replay blocked behind an apply that had not acquired lifecycleMu")
	}
	handler.owners.mu.Lock()
	stillPending := owner.pending != nil && owner.pending.intent.ID == delivery.intent.ID && owner.pending.sequence == delivery.sequence && owner.pending.delivered
	outboundSequence := owner.outboundSequence
	handler.owners.mu.Unlock()
	if !stillPending || outboundSequence != event.Sequence {
		t.Fatalf("replay changed applying delivery pending=%v outbound=%d want sequence=%d", stillPending, outboundSequence, event.Sequence)
	}
	close(releaseApply)
	select {
	case applied := <-applyDone:
		if !applied {
			t.Fatal("durable apply failed after replay")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("durable apply did not terminate")
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonDeviceRevoked)
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requests:
	default:
		t.Fatal("durable apply request was not recorded")
	}
	select {
	case request := <-requests:
		t.Fatalf("replay caused a second apply: %+v", request)
	default:
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var appliedAudits, closedAudits int
	for _, audit := range events {
		if audit.EventKind == remotedevice.EventClosureApplied && audit.SessionID == "session-replay-applying" {
			appliedAudits++
		}
		if audit.EventKind == remotedevice.EventSessionClosed && audit.SessionID == "session-replay-applying" && audit.Reason == remotedevice.ReasonRevoked {
			closedAudits++
		}
	}
	if appliedAudits != 1 || closedAudits != 1 {
		t.Fatalf("replay applying evidence applied=%d closed=%d", appliedAudits, closedAudits)
	}
}

func TestStoreBackedBlockedApplyKeepsSocketOpenAndJoinsHandlerClose(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	entered := make(chan struct{})
	release := make(chan struct{})
	authenticator := &applyRecordingStore{Store: store, entered: entered, release: release}
	handler := NewHandler(authenticator, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-blocked-apply", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-blocked-apply"})
	if err != nil || len(result.Intents) != 1 {
		t.Fatalf("revoke result = %+v err=%v", result, err)
	}
	event := readRevokedEvent(t, conn)
	if err := conn.WriteMessage(websocket.TextMessage, revokedEventAckJSON("session-blocked-apply", "device-1", event.EventID, 1, event.Fence)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("apply did not reach blocking barrier")
	}
	readResult := make(chan error, 1)
	go func() {
		_, _, readErr := conn.ReadMessage()
		readResult <- readErr
	}()
	select {
	case err := <-readResult:
		t.Fatalf("socket closed while durable apply was blocked: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- handler.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Handler.Close returned before blocked apply completed: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Handler.Close did not join durable apply")
	}
	select {
	case err := <-readResult:
		var closeErr *websocket.CloseError
		if !errors.As(err, &closeErr) || closeErr.Code != websocket.ClosePolicyViolation || closeErr.Text != CloseReasonDeviceRevoked {
			t.Fatalf("post-apply close = %v, want device_revoked", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("socket did not close after durable apply")
	}
	intents, err := store.PendingClosureIntents(context.Background(), remotedevice.ClosureIntentFilter{DeviceID: "device-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 0 {
		t.Fatalf("blocked apply remained pending after release: %+v", intents)
	}
}

func TestStoreBackedForeignSocketCannotAcknowledgeRevocation(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	deviceTwo := enrollAdditionalDevice(t, store, material, "enrollment-foreign", "device-foreign")
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
	server := startTLSServer(t, handler, material)
	first := dialWithCertificate(t, server, material, material.clientTLS)
	if err := first.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-owner", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := first.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("owner acceptance: type=%d err=%v", messageType, err)
	}
	foreign := dialWithCertificate(t, server, material, material.clientTLS)
	if err := foreign.WriteMessage(websocket.TextMessage, validOfferJSON(deviceTwo.ID, "session-foreign", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := foreign.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("foreign acceptance: type=%d err=%v", messageType, err)
	}
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-foreign-owner"})
	if err != nil || len(result.Intents) != 1 {
		t.Fatalf("revoke result = %+v err=%v", result, err)
	}
	event := readRevokedEvent(t, first)
	if err := foreign.WriteMessage(websocket.TextMessage, revokedEventAckJSON("session-owner", "device-1", event.EventID, 1, event.Fence)); err != nil {
		t.Fatal(err)
	}
	readClose(t, foreign, websocket.ClosePolicyViolation, CloseReasonPolicy)
	intents, err := store.PendingClosureIntents(context.Background(), remotedevice.ClosureIntentFilter{DeviceID: "device-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].State != "pending" {
		t.Fatalf("foreign ACK changed owner intent: %+v", intents)
	}
	if err := first.WriteMessage(websocket.TextMessage, revokedEventAckJSON("session-owner", "device-1", event.EventID, 1, event.Fence)); err != nil {
		t.Fatal(err)
	}
	readClose(t, first, websocket.ClosePolicyViolation, CloseReasonDeviceRevoked)
}

func TestStoreBackedRevocationAcknowledgementLeavesPendingOnApplyFailureOrDisconnect(t *testing.T) {
	tests := []struct {
		name string
		fail bool
	}{
		{name: "apply failure", fail: true},
		{name: "disconnect", fail: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			material := newTLSMaterial(t)
			store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
			var authenticator Authenticator = store
			if test.fail {
				authenticator = &applyRecordingStore{Store: store, err: errors.New("apply failure")}
			}
			handler := NewHandler(authenticator, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
			server := startTLSServer(t, handler, material)
			conn := dialWithCertificate(t, server, material, material.clientTLS)
			if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-pending-failure", "linux", "x86_64")); err != nil {
				t.Fatal(err)
			}
			if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
				t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
			}
			result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-pending-failure-" + strings.ReplaceAll(test.name, " ", "-")})
			if err != nil || len(result.Intents) != 1 {
				t.Fatalf("revoke result = %+v err=%v", result, err)
			}
			event := readRevokedEvent(t, conn)
			if test.fail {
				if err := conn.WriteMessage(websocket.TextMessage, revokedEventAckJSON("session-pending-failure", "device-1", event.EventID, 1, event.Fence)); err != nil {
					t.Fatal(err)
				}
				readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonPolicy)
			} else {
				if err := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			intents, err := store.PendingClosureIntents(context.Background(), remotedevice.ClosureIntentFilter{DeviceID: "device-1", Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if len(intents) != 1 || intents[0].State != "pending" {
				t.Fatalf("failure changed intent = %+v", intents)
			}
			events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.EventKind == remotedevice.EventClosureApplied || event.EventKind == remotedevice.EventSessionClosed {
					t.Fatalf("failure wrote durable closure evidence: %+v", event)
				}
			}
		})
	}
}

func TestStoreBackedCanceledRevocationAcknowledgementFailsClosedAndStaysPending(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	authenticator := &applyRecordingStore{Store: store}
	handler := NewHandler(authenticator, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-canceled-ack", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "revoke-canceled-ack"})
	if err != nil || len(result.Intents) != 1 {
		t.Fatalf("revoke result = %+v err=%v", result, err)
	}
	event := readRevokedEvent(t, conn)
	handler.owners.mu.Lock()
	owner := handler.owners.owners["session-canceled-ack"]
	handler.owners.mu.Unlock()
	if owner == nil {
		t.Fatal("canceled acknowledgement owner not found")
	}
	delivery, ok := handler.owners.beginRevocationAcknowledgement(owner, 1, event.EventID, event.Fence)
	if !ok {
		t.Fatal("canceled acknowledgement was not admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if handler.applyRevocationAcknowledgement(ctx, owner, delivery) {
		t.Fatal("canceled acknowledgement unexpectedly applied")
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonPolicy)
	intents, err := store.PendingClosureIntents(context.Background(), remotedevice.ClosureIntentFilter{DeviceID: "device-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].State != "pending" {
		t.Fatalf("canceled acknowledgement changed intent: %+v", intents)
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range events {
		if audit.EventKind == remotedevice.EventClosureApplied || audit.EventKind == remotedevice.EventSessionClosed {
			t.Fatalf("canceled acknowledgement wrote durable closure evidence: %+v", audit)
		}
	}
}

func TestHandlerCloseJoinsOwnerClosePausedAfterMarkingClosed(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-close-join", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}

	reached := make(chan struct{})
	release := make(chan struct{})
	handler.owners.mu.Lock()
	owner := handler.owners.owners["session-close-join"]
	if owner == nil {
		handler.owners.mu.Unlock()
		t.Fatal("accepted owner was not registered")
	}
	owner.closePause = &ownerClosePause{reached: reached, release: release}
	handler.owners.mu.Unlock()

	closeDone := make(chan error, 1)
	go func() { closeDone <- handler.Close() }()
	closeDoneAgain := make(chan error, 1)
	go func() { closeDoneAgain <- handler.Close() }()
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("Handler.Close did not reach the pre-close barrier")
	}
	// The owner is already marked closed, but its closeOne is paused. A second
	// Close must join that operation instead of returning while the socket is
	// still open.
	select {
	case err := <-closeDone:
		t.Fatalf("Handler.Close returned before owner close completed: %v", err)
	default:
	}
	select {
	case err := <-closeDoneAgain:
		t.Fatalf("second Handler.Close returned before owner close completed: %v", err)
	default:
	}
	close(release)
	for _, item := range []struct {
		name string
		done chan error
	}{{name: "first close", done: closeDone}, {name: "second close", done: closeDoneAgain}} {
		select {
		case err := <-item.done:
			if err != nil {
				t.Fatalf("%s: %v", item.name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not join owner close", item.name)
		}
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonTransport)
}

func TestStoreBackedRevocationClosesClosedOwnerAndPreservesOtherDevice(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	deviceTwo := enrollAdditionalDevice(t, store, material, "enrollment-2", "device-2")
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }))
	server := startTLSServer(t, handler, material)

	first := dialWithCertificate(t, server, material, material.clientTLS)
	if err := first.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-closed-owner", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := first.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("first acceptance: type=%d err=%v", messageType, err)
	}
	second := dialWithCertificate(t, server, material, material.clientTLS)
	if err := second.WriteMessage(websocket.TextMessage, validOfferJSON(deviceTwo.ID, "session-other-device", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := second.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("second acceptance: type=%d err=%v", messageType, err)
	}

	// A session closed before revocation produces no closure intent. Its live
	// owner is not a target for the device-revocation event; the exact pending
	// intent routing below remains scoped to the other device.
	actor := remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}
	if _, err := store.CloseSession(context.Background(), remotedevice.CloseSessionRequest{
		SessionID: "session-closed-owner", DeviceID: "device-1", Actor: actor,
		Reason: remotedevice.SessionCloseReasonAdministrator, RequestID: "close-before-revoke",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{
		DeviceID: "device-1", Actor: actor, Reason: remotedevice.RevocationReasonAdministrator,
		RequestID: "revoke-device-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Intents) != 0 {
		t.Fatalf("closed session unexpectedly produced intents: %+v", result.Intents)
	}
	if err := first.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	// A pong is an explicit protocol-level proof that the unrelated owner was
	// not closed. The read goroutine is joined before test exit to avoid leaks.
	pong := make(chan struct{})
	var pongOnce sync.Once
	second.SetPongHandler(func(string) error {
		pongOnce.Do(func() { close(pong) })
		return nil
	})
	readerDone := make(chan error, 1)
	go func() {
		_, _, readErr := second.ReadMessage()
		readerDone <- readErr
	}()
	if err := second.WriteControl(websocket.PingMessage, []byte("isolation"), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pong:
	case <-time.After(2 * time.Second):
		t.Fatal("unaffected device did not answer ping")
	}
	if err := second.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-readerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("unaffected owner reader did not terminate")
	}

	intents, err := store.PendingClosureIntents(context.Background(), remotedevice.ClosureIntentFilter{DeviceID: "device-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 0 {
		t.Fatalf("pending intents for closed session = %+v", intents)
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventKind == remotedevice.EventClosureApplied {
			t.Fatalf("revocation transport close acknowledged intent: %+v", event)
		}
	}
}

func TestStoreBackedPeerDisconnectClosesDurableSession(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	cleaned := make(chan remotedevice.Session, 1)
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }), WithSessionCleanupHook(func(session remotedevice.Session, cleanupErr error) {
		if cleanupErr != nil {
			return
		}
		cleaned <- session
	}))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-disconnect", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	if err := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case session := <-cleaned:
		if session.ID != "session-disconnect" {
			t.Fatalf("cleaned session = %+v", session)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("durable session cleanup did not complete")
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	closed := 0
	for _, event := range events {
		if event.EventKind == remotedevice.EventSessionClosed && event.SessionID == "session-disconnect" && event.Reason == remotedevice.ReasonAdministrator {
			if event.Actor != (remotedevice.Actor{ID: "coordinator", PolicyID: "remote"}) {
				t.Fatalf("session.closed actor = %+v, want coordinator actor", event.Actor)
			}
			closed++
		}
	}
	if closed != 1 {
		t.Fatalf("session.closed events = %d, want one administrator cleanup", closed)
	}
}

func TestStoreBackedPostAdmissionDataUsesProtocolCleanup(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	cleaned := make(chan error, 1)
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }), WithSessionCleanupHook(func(_ remotedevice.Session, cleanupErr error) {
		cleaned <- cleanupErr
	}))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-protocol", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-protocol", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonPolicy)
	select {
	case err := <-cleaned:
		if err != nil {
			t.Fatalf("protocol cleanup: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("protocol cleanup did not complete")
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	closed := 0
	for _, event := range events {
		if event.EventKind == remotedevice.EventSessionClosed && event.SessionID == "session-protocol" && event.Reason == remotedevice.ReasonProtocolError {
			if event.Actor != (remotedevice.Actor{ID: "coordinator", PolicyID: "remote"}) {
				t.Fatalf("session.closed actor = %+v, want coordinator actor", event.Actor)
			}
			closed++
		}
	}
	if closed != 1 {
		t.Fatalf("session.closed protocol events = %d, want one", closed)
	}
}

func TestStoreBackedIncompletePostNegotiationFrameClosesImmediately(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	cleaned := make(chan error, 1)
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }), WithSessionCleanupHook(func(_ remotedevice.Session, cleanupErr error) {
		cleaned <- cleanupErr
	}))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-incomplete-post", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Send a masked, non-FIN text frame directly. There is deliberately no
	// continuation frame, so the server must reject at the frame boundary
	// without draining the returned reader.
	mask := [4]byte{0x11, 0x22, 0x33, 0x44}
	payload := byte('x') ^ mask[0]
	frame := []byte{0x01, 0x81, mask[0], mask[1], mask[2], mask[3], payload}
	if _, err := conn.UnderlyingConn().Write(frame); err != nil {
		t.Fatal(err)
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonPolicy)
	select {
	case err := <-cleaned:
		if err != nil {
			t.Fatalf("incomplete post-negotiation cleanup: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("incomplete post-negotiation cleanup did not complete")
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var protocolClosed int
	for _, event := range events {
		if event.EventKind == remotedevice.EventSessionClosed && event.SessionID == "session-incomplete-post" {
			if event.Reason != remotedevice.ReasonProtocolError || event.Actor != (remotedevice.Actor{ID: "coordinator", PolicyID: "remote"}) {
				t.Fatalf("incomplete post-negotiation session.closed = %+v", event)
			}
			protocolClosed++
		}
	}
	if protocolClosed != 1 {
		t.Fatalf("incomplete post-negotiation session.closed events = %d, want one", protocolClosed)
	}
}

func TestHandlerCloseIsTerminalAndCleansOwnedSession(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	cleaned := make(chan remotedevice.Session, 1)
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }), WithSessionCleanupHook(func(session remotedevice.Session, cleanupErr error) {
		if cleanupErr == nil {
			cleaned <- session
		}
	}))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-shutdown", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonTransport)
	select {
	case session := <-cleaned:
		if session.ID != "session-shutdown" {
			t.Fatalf("cleaned session = %+v", session)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown cleanup did not complete")
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var shutdownClosed int
	for _, event := range events {
		if event.EventKind == remotedevice.EventSessionClosed && event.SessionID == "session-shutdown" {
			if event.Reason != remotedevice.ReasonAdministrator || event.Actor != (remotedevice.Actor{ID: "coordinator", PolicyID: "remote"}) {
				t.Fatalf("shutdown session.closed = %+v, want administrator/coordinator", event)
			}
			shutdownClosed++
		}
	}
	if shutdownClosed != 1 {
		t.Fatalf("shutdown session.closed events = %d, want one", shutdownClosed)
	}

	reconnect := dialWithCertificate(t, server, material, material.clientTLS)
	if err := reconnect.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-after-shutdown", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	readClose(t, reconnect, websocket.ClosePolicyViolation, CloseReasonPolicy)
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionCleanupHookIsReentrantAfterLifecycleUnlock(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	cleanupDone := make(chan error, 1)
	var handler *Handler
	handler = NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }), WithSessionCleanupHook(func(_ remotedevice.Session, _ error) {
		_, revokeErr := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{DeviceID: "device-1", Actor: remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: remotedevice.RevocationReasonAdministrator, RequestID: "reentrant-revoke"})
		cleanupDone <- revokeErr
	}))
	server := startTLSServer(t, handler, material)
	conn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := conn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-reentrant", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := conn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("acceptance read: type=%d err=%v", messageType, err)
	}
	if err := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-cleanupDone:
		if err != nil {
			t.Fatalf("reentrant revocation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reentrant cleanup hook deadlocked")
	}
}

func TestSlowCleanupHookDoesNotBlockNextAdmission(t *testing.T) {
	material := newTLSMaterial(t)
	store, _, _ := seedActiveStore(t, material, time.Now().UTC().Add(time.Hour))
	hookStarted := make(chan struct{})
	releaseHook := make(chan struct{})
	var first sync.Once
	handler := NewHandler(store, PeerGateFunc(func(_ context.Context, remoteAddr string) bool { return strings.HasPrefix(remoteAddr, "127.0.0.1:") }), WithSessionCleanupHook(func(_ remotedevice.Session, _ error) {
		first.Do(func() {
			close(hookStarted)
			<-releaseHook
		})
	}))
	server := startTLSServer(t, handler, material)
	firstConn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := firstConn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-hook-one", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := firstConn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("first acceptance: type=%d err=%v", messageType, err)
	}
	if err := firstConn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hookStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup hook did not start")
	}
	secondConn := dialWithCertificate(t, server, material, material.clientTLS)
	if err := secondConn.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-hook-two", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	if messageType, _, err := secondConn.ReadMessage(); err != nil || messageType != websocket.TextMessage {
		t.Fatalf("admission blocked by cleanup hook: type=%d err=%v", messageType, err)
	}
	close(releaseHook)
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
}
