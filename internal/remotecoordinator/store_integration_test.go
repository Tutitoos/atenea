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
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonDeviceRevoked)

	intents, err := store.PendingClosureIntents(context.Background(), remotedevice.ClosureIntentFilter{DeviceID: "device-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].State != "pending" {
		t.Fatalf("pending intents = %+v", intents)
	}
	events, err := store.Audit(context.Background(), remotedevice.AuditFilter{DeviceID: "device-1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventKind == remotedevice.EventClosureApplied || event.EventKind == remotedevice.EventSessionClosed {
			t.Fatalf("transport close wrote durable acknowledgement: %+v", event)
		}
	}

	reconnect := dialWithCertificate(t, server, material, material.clientTLS)
	if err := reconnect.WriteMessage(websocket.TextMessage, validOfferJSON("device-1", "session-reconnect", "linux", "x86_64")); err != nil {
		t.Fatal(err)
	}
	readClose(t, reconnect, websocket.ClosePolicyViolation, CloseReasonAuthentication)
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

	type revokeOutcome struct {
		result remotedevice.RevokeDeviceResponse
		err    error
	}
	revokeDone := make(chan revokeOutcome, 1)
	go func() {
		result, err := handler.RevokeDevice(context.Background(), remotedevice.RevokeDeviceRequest{
			DeviceID: "device-1", Actor: remotedevice.Actor{ID: "operator-1", PolicyID: "policy-1"},
			Reason: remotedevice.RevocationReasonAdministrator, RequestID: "close-join-revoke",
		})
		revokeDone <- revokeOutcome{result: result, err: err}
	}()
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("revocation did not reach the pre-close barrier")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- handler.Close() }()
	closeDoneAgain := make(chan error, 1)
	go func() { closeDoneAgain <- handler.Close() }()
	// The owner is already marked closed, but its closeOne is paused. Close must
	// join that operation instead of returning while the socket remains open.
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
	select {
	case outcome := <-revokeDone:
		if outcome.err != nil || len(outcome.result.Intents) != 1 || outcome.result.Intents[0].State != "pending" {
			t.Fatalf("revocation outcome = %+v", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revocation did not complete after owner close")
	}
	readClose(t, conn, websocket.ClosePolicyViolation, CloseReasonDeviceRevoked)
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

	// CloseSession is durable state only; the owner remains live until the
	// transport observes a terminal event. RevokeDevice must therefore still
	// close this socket, while producing no new closure intent for it.
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
	readClose(t, first, websocket.ClosePolicyViolation, CloseReasonDeviceRevoked)

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
