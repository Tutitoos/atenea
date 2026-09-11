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
	clock       *integrationClock
	fingerprint string
	notAfter    time.Time
}

func (i *canonicalMetadataIssuer) Issue(_ context.Context, request remotedevice.CertificateIssueRequest) (remotedevice.CertificateMetadata, error) {
	return remotedevice.CertificateMetadata{
		ID:              "cert-1",
		IssuerID:        "test-issuer",
		Serial:          "serial-1",
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
	store, err := remotedevice.Open(context.Background(), filepath.Join(databaseDirectory, "devices.sqlite"), remotedevice.WithClock(clock.Now), remotedevice.WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x41}, 64))), remotedevice.WithCertificateIssuer(issuer))
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
				readClose(t, conn, websocket.CloseNormalClosure, CloseReasonAccepted)
			} else {
				readClose(t, conn, websocket.ClosePolicyViolation, test.wantCloseReason)
			}
			assertNoDurableSession(t, store)
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
	assertNoDurableSession(t, store)
}
