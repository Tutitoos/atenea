package remotedevice

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(value time.Time) {
	c.mu.Lock()
	c.now = value
	c.mu.Unlock()
}

type testIssuer struct {
	mu        sync.Mutex
	clock     *testClock
	fails     int
	calls     int
	expired   bool
	hold      func()
	notBefore time.Time
	notAfter  time.Time
}

func (i *testIssuer) Issue(_ context.Context, request CertificateIssueRequest) (CertificateMetadata, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.calls++
	if i.hold != nil {
		hold := i.hold
		i.hold = nil
		hold()
	}
	if i.fails > 0 {
		i.fails--
		return CertificateMetadata{}, errors.New("issuer unavailable")
	}
	now := i.clock.Now()
	if i.expired {
		return CertificateMetadata{ID: "cert-1", IssuerID: "issuer-1", Serial: "serial-1", Fingerprint: "fp-1", PublicKeyDigest: append([]byte(nil), request.PublicKeyDigest...), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(-time.Second)}, nil
	}
	notBefore, notAfter := now.Add(-time.Minute), now.Add(time.Hour)
	if !i.notBefore.IsZero() {
		notBefore = i.notBefore
	}
	if !i.notAfter.IsZero() {
		notAfter = i.notAfter
	}
	return CertificateMetadata{ID: "cert-1", IssuerID: "issuer-1", Serial: "serial-1", Fingerprint: "fp-1", PublicKeyDigest: append([]byte(nil), request.PublicKeyDigest...), NotBefore: notBefore, NotAfter: notAfter}, nil
}

func testKeys(t *testing.T) ([]byte, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return spki, private
}

func openTestStore(t *testing.T, clock *testClock, issuer CertificateIssuer, entropy []byte) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "registry", "devices.sqlite"), WithClock(clock.Now), WithRandomReader(bytes.NewReader(entropy)), WithCertificateIssuer(issuer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createEnrollment(t *testing.T, store *Store) CreateEnrollmentResponse {
	t.Helper()
	enrollment, err := store.CreateEnrollment(context.Background(), CreateEnrollmentRequest{ID: "enrollment-1", DeviceID: "device-1", Name: "workstation", Platform: PlatformLinux, Architecture: ArchitectureX8664, Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}})
	if err != nil {
		t.Fatal(err)
	}
	return enrollment
}

func issueChallenge(t *testing.T, store *Store, enrollment CreateEnrollmentResponse, spki []byte) Challenge {
	t.Helper()
	challenge, err := store.IssueChallenge(context.Background(), IssueChallengeRequest{EnrollmentID: enrollment.ID, DeviceID: enrollment.DeviceID, Name: enrollment.Name, Platform: enrollment.Platform, Architecture: enrollment.Architecture, Token: enrollment.Token, PublicKeySPKI: spki, Context: []byte("test-context"), IdempotencyKey: "issue-1"})
	if err != nil {
		t.Fatal(err)
	}
	return challenge
}

func signChallenge(challenge Challenge, enrollment CreateEnrollmentResponse, private ed25519.PrivateKey) []byte {
	message := canonicalChallengeMessage(challenge.ID, enrollment.ID, enrollment.DeviceID, enrollment.Name, enrollment.Platform, enrollment.Architecture, enrollment.ExpiresAt.UnixNano(), challenge.IssuedAt.UnixNano(), challenge.ExpiresAt.UnixNano(), challenge.NonceDigest, challenge.OriginalContextDigest, challenge.PublicKeySPKI, challenge.PublicKeyDigest)
	return ed25519.Sign(private, message)
}

func TestEnrollmentProofIsRealAndSecretsAreTransient(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	spki, private := testKeys(t)
	issuer := &testIssuer{clock: clock}
	entropy := bytes.Repeat([]byte{0x41}, 64)
	store := openTestStore(t, clock, issuer, entropy)
	enrollment := createEnrollment(t, store)
	challenge := issueChallenge(t, store, enrollment, spki)
	if challenge.ExpiresAt.After(now.Add(maxChallengeTTL)) || challenge.ExpiresAt.After(enrollment.ExpiresAt) {
		t.Fatalf("challenge TTL exceeded bound: %v", challenge.ExpiresAt)
	}
	signature := signChallenge(challenge, enrollment, private)
	device, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: append([]byte(nil), challenge.Nonce...), Signature: signature})
	if err != nil {
		t.Fatal(err)
	}
	if device.State != "active" || device.Certificate.ID != "cert-1" || len(device.Certificate.PublicKeyDigest) != 32 {
		t.Fatalf("unexpected device: %+v", device)
	}
	if _, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signature}); !errors.Is(err, ErrChallengeUsed) {
		t.Fatalf("replay error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	path := store.path
	tokenRaw, err := decodeToken(enrollment.Token)
	if err != nil {
		t.Fatal(err)
	}
	secrets := [][]byte{[]byte(enrollment.Token), tokenRaw, challenge.Nonce, signature, private[:8]}
	for _, filename := range []string{path, path + "-wal", path + "-shm"} {
		raw, readErr := os.ReadFile(filename)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		for index, secret := range secrets {
			if bytes.Contains(raw, secret) {
				t.Fatalf("transient secret persisted file=%s index=%d: %x", filename, index, sha256.Sum256(secret))
			}
		}
	}
}

func TestEnrollmentProofAcceptsECDSAP256(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &testIssuer{clock: clock}
	store := openTestStore(t, clock, issuer, bytes.Repeat([]byte{0x47}, 64))
	enrollment := createEnrollment(t, store)
	challenge := issueChallenge(t, store, enrollment, spki)
	message := canonicalChallengeMessage(challenge.ID, enrollment.ID, enrollment.DeviceID, enrollment.Name, enrollment.Platform, enrollment.Architecture, enrollment.ExpiresAt.UnixNano(), challenge.IssuedAt.UnixNano(), challenge.ExpiresAt.UnixNano(), challenge.NonceDigest, challenge.OriginalContextDigest, challenge.PublicKeySPKI, challenge.PublicKeyDigest)
	digest := sha256.Sum256(message)
	signature, err := ecdsa.SignASN1(rand.Reader, private, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signature}); err != nil {
		t.Fatalf("P-256 completion: %v", err)
	}
}

func TestInvalidProofIsTerminalAndIssuerFailureCanRetry(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	spki, private := testKeys(t)
	issuer := &testIssuer{clock: clock, fails: 1}
	store := openTestStore(t, clock, issuer, bytes.Repeat([]byte{0x42}, 64))
	enrollment := createEnrollment(t, store)
	challenge := issueChallenge(t, store, enrollment, spki)
	bad := signChallenge(challenge, enrollment, private)
	bad[0] ^= 1
	if _, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: bad}); !errors.Is(err, ErrProofInvalid) {
		t.Fatalf("invalid proof error: %v", err)
	}
	var challengeState string
	if err := store.db.QueryRow(`SELECT state FROM challenges WHERE id=?`, challenge.ID).Scan(&challengeState); err != nil {
		t.Fatal(err)
	}
	if challengeState != string(ChallengeFailed) {
		t.Fatalf("challenge state = %s", challengeState)
	}

	clock2 := &testClock{now: now}
	spki2, private2 := testKeys(t)
	issuer2 := &testIssuer{clock: clock2, fails: 1}
	store2 := openTestStore(t, clock2, issuer2, bytes.Repeat([]byte{0x43}, 64))
	enrollment2 := createEnrollment(t, store2)
	challenge2 := issueChallenge(t, store2, enrollment2, spki2)
	signature := signChallenge(challenge2, enrollment2, private2)
	if _, err := store2.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge2.ID, Nonce: challenge2.Nonce, Signature: signature}); err == nil {
		t.Fatal("issuer failure unexpectedly succeeded")
	}
	if _, err := store2.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge2.ID, Nonce: challenge2.Nonce, Signature: signature}); err != nil {
		t.Fatalf("issuer retry failed: %v", err)
	}
	issuer2.mu.Lock()
	calls := issuer2.calls
	issuer2.mu.Unlock()
	if calls != 2 {
		t.Fatalf("issuer calls = %d", calls)
	}
}

func TestSPKIAndCertificateBounds(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	spki, _ := testKeys(t)
	store := openTestStore(t, clock, &testIssuer{clock: clock}, bytes.Repeat([]byte{0x44}, 64))
	if _, err := store.CreateEnrollment(context.Background(), CreateEnrollmentRequest{ID: "too-long", DeviceID: "device-1", Name: "w", Platform: PlatformLinux, Architecture: ArchitectureX8664, Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}, TTL: maxEnrollmentTTL + time.Second}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("long TTL error: %v", err)
	}
	enrollment := createEnrollment(t, store)
	if _, err := store.IssueChallenge(context.Background(), IssueChallengeRequest{EnrollmentID: enrollment.ID, DeviceID: enrollment.DeviceID, Name: enrollment.Name, Platform: enrollment.Platform, Architecture: enrollment.Architecture, Token: enrollment.Token, PublicKeySPKI: append(append([]byte(nil), spki...), bytes.Repeat([]byte{0}, maxSPKIBytes)...)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("oversized SPKI error: %v", err)
	}
	if err := validatePlatformArchitecture(Platform("solaris"), ArchitectureX8664); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("platform error: %v", err)
	}
}

func TestBindingMismatchConsumesEnrollmentAndAudits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(IssueChallengeRequest) IssueChallengeRequest
	}{
		{name: "device", mutate: func(req IssueChallengeRequest) IssueChallengeRequest { req.DeviceID = "other-device"; return req }},
		{name: "platform", mutate: func(req IssueChallengeRequest) IssueChallengeRequest { req.Platform = PlatformWindows; return req }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := &testClock{now: time.Date(2026, 9, 11, 12, 30, 0, 0, time.UTC)}
			spki, _ := testKeys(t)
			store := openTestStore(t, clock, &testIssuer{clock: clock}, bytes.Repeat([]byte{0x62}, 64))
			enrollment := createEnrollment(t, store)
			req := IssueChallengeRequest{EnrollmentID: enrollment.ID, DeviceID: enrollment.DeviceID, Name: enrollment.Name, Platform: enrollment.Platform, Architecture: enrollment.Architecture, Token: enrollment.Token, PublicKeySPKI: spki, Context: []byte("binding")}
			if _, err := store.IssueChallenge(context.Background(), test.mutate(req)); !errors.Is(err, ErrBindingMismatch) {
				t.Fatalf("binding mismatch error: %v", err)
			}
			var state string
			if err := store.db.QueryRow(`SELECT state FROM enrollments WHERE id=?`, enrollment.ID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != string(EnrollmentFailed) {
				t.Fatalf("enrollment state = %s", state)
			}
			var eventKind, reason string
			if err := store.db.QueryRow(`SELECT event_kind,reason FROM audit_events WHERE enrollment_id=? ORDER BY sequence DESC LIMIT 1`, enrollment.ID).Scan(&eventKind, &reason); err != nil {
				t.Fatal(err)
			}
			if eventKind != string(EventEnrollmentFailed) || reason != string(ReasonBindingMismatch) {
				t.Fatalf("binding audit = %s/%s", eventKind, reason)
			}
			path := store.path
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(context.Background(), path, WithClock(clock.Now))
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if _, err := reopened.IssueChallenge(context.Background(), req); !errors.Is(err, ErrEnrollmentUsed) {
				t.Fatalf("reused token after restart: %v", err)
			}
		})
	}
}

func TestChallengeTTLIsCappedByEnrollmentAndExpiredCertificateDoesNotConsume(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	spki, private := testKeys(t)
	issuer := &testIssuer{clock: clock, expired: true}
	store := openTestStore(t, clock, issuer, bytes.Repeat([]byte{0x48}, 64))
	enrollment, err := store.CreateEnrollment(context.Background(), CreateEnrollmentRequest{ID: "short-enrollment", DeviceID: "device-1", Name: "workstation", Platform: PlatformLinux, Architecture: ArchitectureX8664, Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	challenge := issueChallenge(t, store, enrollment, spki)
	if !challenge.ExpiresAt.Equal(enrollment.ExpiresAt) {
		t.Fatalf("challenge expiry %s exceeded enrollment expiry %s", challenge.ExpiresAt, enrollment.ExpiresAt)
	}
	if _, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signChallenge(challenge, enrollment, private)}); !errors.Is(err, ErrCertificateInvalid) {
		t.Fatalf("expired certificate error: %v", err)
	}
	var state string
	if err := store.db.QueryRow(`SELECT state FROM challenges WHERE id=?`, challenge.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(ChallengePending) {
		t.Fatalf("challenge was consumed after invalid certificate: %s", state)
	}
}

func TestCompletionRechecksExpiryAfterIssuerReturns(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	spki, private := testKeys(t)
	issuerEntered := make(chan struct{})
	releaseIssuer := make(chan struct{})
	issuer := &testIssuer{clock: clock, notBefore: now.Add(-time.Minute), notAfter: now.Add(time.Hour), hold: func() {
		close(issuerEntered)
		<-releaseIssuer
	}}
	store := openTestStore(t, clock, issuer, bytes.Repeat([]byte{0x49}, 64))
	enrollment := createEnrollment(t, store)
	challenge := issueChallenge(t, store, enrollment, spki)
	done := make(chan error, 1)
	go func() {
		_, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signChallenge(challenge, enrollment, private)})
		done <- err
	}()
	select {
	case <-issuerEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("issuer was not reached")
	}
	clock.Set(now.Add(maxChallengeTTL + time.Second))
	close(releaseIssuer)
	if err := <-done; !errors.Is(err, ErrEnrollmentExpired) {
		t.Fatalf("completion after expiry error = %v", err)
	}
	var challengeState, enrollmentState string
	if err := store.db.QueryRow(`SELECT state FROM challenges WHERE id=?`, challenge.ID).Scan(&challengeState); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT state FROM enrollments WHERE id=?`, enrollment.ID).Scan(&enrollmentState); err != nil {
		t.Fatal(err)
	}
	if challengeState != string(ChallengeExpired) || enrollmentState != string(EnrollmentExpired) {
		t.Fatalf("expired completion states challenge=%s enrollment=%s", challengeState, enrollmentState)
	}
	var devices int
	if err := store.db.QueryRow(`SELECT count(*) FROM devices`).Scan(&devices); err != nil {
		t.Fatal(err)
	}
	if devices != 0 {
		t.Fatalf("expired completion activated %d devices", devices)
	}
}

func TestSecureParentAndSymlinkAreRejectedWithoutChmod(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "parent")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), filepath.Join(parent, "devices.sqlite")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("insecure parent error: %v", err)
	}
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("existing parent was chmod: %o", info.Mode().Perm())
	}
	secure := filepath.Join(dir, "secure")
	if err := os.Mkdir(secure, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(secure, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), filepath.Join(link, "devices.sqlite")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("symlink parent error: %v", err)
	}
}

func TestTypedAdministrativeTransitionsAndRevocation(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	spki, private := testKeys(t)
	store := openTestStore(t, clock, &testIssuer{clock: clock}, bytes.Repeat([]byte{0x45}, 64))
	enrollment := createEnrollment(t, store)
	challenge := issueChallenge(t, store, enrollment, spki)
	device, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signChallenge(challenge, enrollment, private)})
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{ID: "operator-1", PolicyID: "policy-1"}
	if _, err := store.RegisterSession(context.Background(), RegisterSessionRequest{SessionID: "session-1", DeviceID: device.ID, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CloseSession(context.Background(), CloseSessionRequest{SessionID: "session-1", DeviceID: device.ID, Actor: actor, Reason: SessionCloseReasonAdministrator}); err != nil {
		t.Fatal(err)
	}
	result, err := store.RevokeDevice(context.Background(), RevokeDeviceRequest{DeviceID: device.ID, Actor: actor, Reason: RevocationReasonAdministrator})
	if err != nil {
		t.Fatal(err)
	}
	if result.Device.State != "revoked" || len(result.Intents) != 0 {
		t.Fatalf("unexpected revocation: %+v", result)
	}
	if _, err := store.Authenticate(context.Background(), AuthenticateRequest{DeviceID: device.ID, Fingerprint: "fp-1"}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("revoked authentication: %v", err)
	}
}

func TestSchemaAndFilesArePrivate(t *testing.T) {
	clock := &testClock{now: time.Now().UTC()}
	path := filepath.Join(t.TempDir(), "state", "devices.sqlite")
	store, err := Open(context.Background(), path, WithClock(clock.Now), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x46}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("directory mode: %v %v", info, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode: %v %v", info, err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "1" {
		t.Fatalf("schema version = %s", version)
	}
	var sqliteVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&sqliteVersion); err != nil {
		t.Fatal(err)
	}
	if sqliteVersion != SchemaVersion {
		t.Fatalf("SQLite schema version = %d", sqliteVersion)
	}
	if reopened, err := Open(context.Background(), path, WithClock(clock.Now), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x46}, 32)))); err != nil {
		t.Fatalf("schema v1 reopen: %v", err)
	} else if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaV1RejectsFutureAndMissingCriticalObjects(t *testing.T) {
	newDatabase := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "state", "devices.sqlite")
		store, err := Open(context.Background(), path, WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x61}, 32))))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}
	mutateAndReject := func(t *testing.T, mutate string) {
		t.Helper()
		path := newDatabase(t)
		dsn, err := sqliteDSN(path)
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(mutate); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if reopened, err := Open(context.Background(), path); err == nil {
			_ = reopened.Close()
			t.Fatalf("Open accepted corrupt schema after %s", mutate)
		}
	}

	mutateAndReject(t, `PRAGMA user_version = 2`)
	mutateAndReject(t, `UPDATE meta SET value='future' WHERE key='schema_version'`)
	mutateAndReject(t, `DROP TRIGGER certificates_valid_transition`)
	mutateAndReject(t, `DROP INDEX audit_by_aggregate`)

	path := newDatabase(t)
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER audit_events_no_update`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER audit_events_no_update BEFORE UPDATE ON audit_events BEGIN SELECT 1; END`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(context.Background(), path); err == nil {
		_ = reopened.Close()
		t.Fatal("Open accepted a no-op trigger with a critical name")
	}
}

func TestAuditSequenceAndActorsFollowLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 11, 13, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	spki, private := testKeys(t)
	actor := Actor{ID: "operator-1", PolicyID: "policy-1"}
	store := openTestStore(t, clock, &testIssuer{clock: clock}, bytes.Repeat([]byte{0x51}, 64))
	enrollment, err := store.CreateEnrollment(context.Background(), CreateEnrollmentRequest{ID: "enrollment-1", DeviceID: "device-1", Name: "workstation", Platform: PlatformLinux, Architecture: ArchitectureX8664, Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	challenge := issueChallenge(t, store, enrollment, spki)
	if challenge.Actor != actor {
		t.Fatalf("challenge actor = %+v", challenge.Actor)
	}
	device, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signChallenge(challenge, enrollment, private)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterSession(context.Background(), RegisterSessionRequest{SessionID: "session-1", DeviceID: device.ID, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CloseSession(context.Background(), CloseSessionRequest{SessionID: "session-1", DeviceID: device.ID, Actor: actor, Reason: SessionCloseReasonAdministrator}); err != nil {
		t.Fatal(err)
	}
	events, err := store.Audit(context.Background(), AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 8 {
		t.Fatalf("audit event count = %d", len(events))
	}
	for index := range events {
		if events[index].Sequence != int64(index+1) {
			t.Fatalf("audit sequence at %d = %d", index, events[index].Sequence)
		}
	}
	want := []EventKind{EventEnrollmentCreated, EventChallengeIssued, EventChallengeConsumed, EventEnrollmentConsumed, EventCertificateIssued, EventDeviceActivated, EventSessionRegistered, EventSessionClosed}
	for index, kind := range want {
		if events[index].EventKind != kind {
			t.Fatalf("event %d = %s, want %s", index, events[index].EventKind, kind)
		}
		if events[index].Actor != actor {
			t.Fatalf("event %s actor = %+v", kind, events[index].Actor)
		}
	}
}

func TestRenewalSupersedesCurrentCertificateAndBindsKey(t *testing.T) {
	now := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	spki, private := testKeys(t)
	actor := Actor{ID: "operator-1", PolicyID: "policy-1"}
	store := openTestStore(t, clock, &testIssuer{clock: clock}, bytes.Repeat([]byte{0x52}, 64))
	enrollment := createEnrollment(t, store)
	challenge := issueChallenge(t, store, enrollment, spki)
	device, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signChallenge(challenge, enrollment, private)})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.RecordCertificateRenewal(context.Background(), RecordCertificateRenewalRequest{DeviceID: device.ID, PreviousCertificateID: device.Certificate.ID, Actor: actor, Metadata: CertificateMetadata{ID: "cert-2", IssuerID: "issuer-2", Serial: "serial-2", Fingerprint: "fp-2", PublicKeyDigest: append([]byte(nil), device.Certificate.PublicKeyDigest...), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ID != "cert-2" || metadata.State != "active" {
		t.Fatalf("renewed metadata = %+v", metadata)
	}
	var oldState, newState, activeID string
	if err := store.db.QueryRow(`SELECT state FROM certificates WHERE id='cert-1'`).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT state FROM certificates WHERE id='cert-2'`).Scan(&newState); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT active_certificate_id FROM devices WHERE id=?`, device.ID).Scan(&activeID); err != nil {
		t.Fatal(err)
	}
	if oldState != "superseded" || newState != "active" || activeID != "cert-2" {
		t.Fatalf("certificate states old=%s new=%s active=%s", oldState, newState, activeID)
	}
	if _, err := store.Authenticate(context.Background(), AuthenticateRequest{DeviceID: device.ID, CertificateID: "cert-1", Fingerprint: "fp-1"}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("old certificate authenticated: %v", err)
	}
	if _, err := store.Authenticate(context.Background(), AuthenticateRequest{DeviceID: device.ID, CertificateID: "cert-2", Fingerprint: "fp-2", PublicKeyDigest: metadata.PublicKeyDigest}); err != nil {
		t.Fatalf("new certificate authentication: %v", err)
	}
	if _, err := store.RecordCertificateRenewal(context.Background(), RecordCertificateRenewalRequest{DeviceID: device.ID, PreviousCertificateID: "cert-1", Actor: actor, Metadata: metadata}); !errors.Is(err, ErrRenewalConflict) {
		t.Fatalf("stale renewal error: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER block_renewal_audit BEFORE INSERT ON audit_events WHEN NEW.event_kind='certificate.renewed' BEGIN SELECT RAISE(ABORT, 'renewal audit failure injection'); END`); err != nil {
		t.Fatal(err)
	}
	metadata.ID = "cert-3"
	metadata.Fingerprint = "fp-3"
	if _, err := store.RecordCertificateRenewal(context.Background(), RecordCertificateRenewalRequest{DeviceID: device.ID, PreviousCertificateID: "cert-2", Actor: actor, Metadata: metadata}); err == nil {
		t.Fatal("renewal audit failure unexpectedly succeeded")
	}
	var currentState string
	if err := store.db.QueryRow(`SELECT state FROM certificates WHERE id='cert-2'`).Scan(&currentState); err != nil {
		t.Fatal(err)
	}
	var replacementCount int
	if err := store.db.QueryRow(`SELECT count(*) FROM certificates WHERE id='cert-3'`).Scan(&replacementCount); err != nil {
		t.Fatal(err)
	}
	if currentState != "active" || replacementCount != 0 {
		t.Fatalf("renewal rollback current=%s replacement=%d", currentState, replacementCount)
	}
	if _, err := store.db.Exec(`UPDATE certificates SET state='active' WHERE id='cert-1'`); err == nil {
		t.Fatal("superseded certificate was reactivated")
	}
}

func TestRenewalClockIsReadAfterImmediateLock(t *testing.T) {
	oldNow := time.Date(2026, 9, 11, 14, 30, 0, 0, time.UTC)
	newNow := oldNow.Add(2 * time.Hour)
	setupClock := &testClock{now: oldNow}
	spki, private := testKeys(t)
	path := filepath.Join(t.TempDir(), "registry", "devices.sqlite")
	issuer := &testIssuer{clock: setupClock, notBefore: oldNow.Add(-time.Minute), notAfter: newNow.Add(time.Hour)}
	store1, err := Open(context.Background(), path, WithClock(setupClock.Now), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x63}, 64))), WithCertificateIssuer(issuer))
	if err != nil {
		t.Fatal(err)
	}
	defer store1.Close()
	var lockHeld atomic.Bool
	lockHeld.Store(true)
	store2Clock := func() time.Time {
		if lockHeld.Load() {
			return oldNow
		}
		return newNow
	}
	store2, err := Open(context.Background(), path, WithClock(store2Clock), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x64}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	enrollment := createEnrollment(t, store1)
	challenge := issueChallenge(t, store1, enrollment, spki)
	device, err := store1.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signChallenge(challenge, enrollment, private)})
	if err != nil {
		t.Fatal(err)
	}
	lockTx, err := store1.db.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(`UPDATE devices SET fence=fence WHERE id=?`, device.ID); err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}
	renewalDone := make(chan error, 1)
	go func() {
		_, renewalErr := store2.RecordCertificateRenewal(context.Background(), RecordCertificateRenewalRequest{
			DeviceID:              device.ID,
			PreviousCertificateID: device.Certificate.ID,
			Actor:                 Actor{ID: "operator-1", PolicyID: "policy-1"},
			Metadata: CertificateMetadata{
				ID:              "cert-after-lock",
				IssuerID:        "issuer-2",
				Serial:          "serial-after-lock",
				Fingerprint:     "fp-after-lock",
				PublicKeyDigest: append([]byte(nil), device.Certificate.PublicKeyDigest...),
				NotBefore:       newNow.Add(-time.Minute),
				NotAfter:        newNow.Add(time.Hour),
			},
		})
		renewalDone <- renewalErr
	}()
	// Let the second handle reach BEGIN IMMEDIATE while the first handle owns
	// the write lock. The clock changes before the lock is released.
	time.Sleep(100 * time.Millisecond)
	lockHeld.Store(false)
	if err := lockTx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-renewalDone:
		if err != nil {
			t.Fatalf("renewal after lock wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("renewal remained blocked after lock release")
	}
}

func TestRevocationLinksIntentsAndExactRetry(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	spki, private := testKeys(t)
	actor := Actor{ID: "operator-1", PolicyID: "policy-1"}
	store := openTestStore(t, clock, &testIssuer{clock: clock}, bytes.Repeat([]byte{0x53}, 64))
	enrollment := createEnrollment(t, store)
	challenge := issueChallenge(t, store, enrollment, spki)
	device, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signChallenge(challenge, enrollment, private)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterSession(context.Background(), RegisterSessionRequest{SessionID: "session-1", DeviceID: device.ID, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	req := RevokeDeviceRequest{DeviceID: device.ID, Actor: actor, Reason: RevocationReasonAdministrator, RequestID: "revoke-1"}
	first, err := store.RevokeDevice(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Intents) != 1 || first.Intents[0].RevocationID != first.Revocation.ID || first.Revocation.State != "applied" {
		t.Fatalf("revocation linkage = %+v", first)
	}
	second, err := store.RevokeDevice(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Already || second.Revocation.ID != first.Revocation.ID || len(second.Intents) != len(first.Intents) || second.Intents[0].ID != first.Intents[0].ID {
		t.Fatalf("repeat revocation = %+v", second)
	}
	if _, err := store.RevokeDevice(context.Background(), RevokeDeviceRequest{DeviceID: device.ID, Actor: Actor{ID: "other", PolicyID: "policy-1"}, Reason: RevocationReasonAdministrator}); !errors.Is(err, ErrRevocationConflict) {
		t.Fatalf("actor conflict = %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM revocations WHERE device_id=?`, device.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("revocations = %d", count)
	}
}

func TestRestartAndConcurrentStoresHaveSingleWinners(t *testing.T) {
	now := time.Date(2026, 9, 11, 16, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	spki, private := testKeys(t)
	path := filepath.Join(t.TempDir(), "registry", "devices.sqlite")
	issuer1, issuer2 := &testIssuer{clock: clock}, &testIssuer{clock: clock}
	store1, err := Open(context.Background(), path, WithClock(clock.Now), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x54}, 64))), WithCertificateIssuer(issuer1))
	if err != nil {
		t.Fatal(err)
	}
	store2, err := Open(context.Background(), path, WithClock(clock.Now), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x55}, 64))), WithCertificateIssuer(issuer2))
	if err != nil {
		_ = store1.Close()
		t.Fatal(err)
	}
	defer store1.Close()
	defer store2.Close()
	enrollment, err := store1.CreateEnrollment(context.Background(), CreateEnrollmentRequest{ID: "enrollment-1", DeviceID: "device-1", Name: "workstation", Platform: PlatformLinux, Architecture: ArchitectureX8664, Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	challenges := make(chan Challenge, 2)
	errs := make(chan error, 2)
	issue := func(store *Store) {
		defer wg.Done()
		challenge, issueErr := store.IssueChallenge(context.Background(), IssueChallengeRequest{EnrollmentID: enrollment.ID, DeviceID: enrollment.DeviceID, Name: enrollment.Name, Platform: enrollment.Platform, Architecture: enrollment.Architecture, Token: enrollment.Token, PublicKeySPKI: spki, Context: []byte("test-context"), IdempotencyKey: "issue-1"})
		if issueErr == nil {
			challenges <- challenge
		} else {
			errs <- issueErr
		}
	}
	wg.Add(2)
	go issue(store1)
	go issue(store2)
	wg.Wait()
	close(challenges)
	close(errs)
	var challenge Challenge
	for candidate := range challenges {
		challenge = candidate
	}
	if challenge.ID == "" {
		t.Fatalf("no successful challenge")
	}
	if len(errs) != 1 || !errors.Is(<-errs, ErrEnrollmentUsed) {
		t.Fatalf("concurrent challenge errors = %v", len(errs))
	}
	signature := signChallenge(challenge, enrollment, private)
	devices := make(chan Device, 2)
	completionErrors := make(chan error, 2)
	complete := func(store *Store) {
		defer wg.Done()
		device, completeErr := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signature})
		if completeErr == nil {
			devices <- device
		} else {
			completionErrors <- completeErr
		}
	}
	wg.Add(2)
	go complete(store1)
	go complete(store2)
	wg.Wait()
	close(devices)
	close(completionErrors)
	var device Device
	for candidate := range devices {
		device = candidate
	}
	if device.ID == "" || len(completionErrors) != 1 {
		t.Fatalf("concurrent completion device=%+v errors=%d", device, len(completionErrors))
	}
	if err := store1.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store2.Close(); err != nil {
		t.Fatal(err)
	}
	store3, err := Open(context.Background(), path, WithClock(clock.Now), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x56}, 64))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store3.GetDevice(context.Background(), device.ID); err != nil {
		t.Fatalf("restart activation: %v", err)
	}
	if _, err := store3.IssueChallenge(context.Background(), IssueChallengeRequest{EnrollmentID: enrollment.ID, DeviceID: enrollment.DeviceID, Name: enrollment.Name, Platform: enrollment.Platform, Architecture: enrollment.Architecture, Token: enrollment.Token, PublicKeySPKI: spki, IdempotencyKey: "issue-2"}); !errors.Is(err, ErrEnrollmentUsed) {
		t.Fatalf("restart token replay: %v", err)
	}
	if _, err := store3.RegisterSession(context.Background(), RegisterSessionRequest{SessionID: "session-1", DeviceID: device.ID, Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}}); err != nil {
		_ = store3.Close()
		t.Fatal(err)
	}
	store4, err := Open(context.Background(), path, WithClock(clock.Now), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x57}, 32))))
	if err != nil {
		_ = store3.Close()
		t.Fatal(err)
	}
	defer store4.Close()
	defer store3.Close()
	var revokeWG sync.WaitGroup
	revokeWG.Add(2)
	results := make(chan RevokeDeviceResponse, 2)
	revokeErrors := make(chan error, 2)
	revoke := func(store *Store) {
		defer revokeWG.Done()
		result, revokeErr := store.RevokeDevice(context.Background(), RevokeDeviceRequest{DeviceID: device.ID, Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: RevocationReasonAdministrator})
		if revokeErr != nil {
			revokeErrors <- revokeErr
		} else {
			results <- result
		}
	}
	go revoke(store3)
	go revoke(store4)
	revokeWG.Wait()
	close(results)
	close(revokeErrors)
	if len(revokeErrors) != 0 {
		t.Fatalf("concurrent revocation errors = %d", len(revokeErrors))
	}
	var revocations int
	if err := store3.db.QueryRow(`SELECT count(*) FROM revocations WHERE device_id=?`, device.ID).Scan(&revocations); err != nil {
		t.Fatal(err)
	}
	if revocations != 1 {
		t.Fatalf("concurrent revocations = %d", revocations)
	}
	if err := store3.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store4.Close(); err != nil {
		t.Fatal(err)
	}
	store5, err := Open(context.Background(), path, WithClock(clock.Now), WithRandomReader(bytes.NewReader(bytes.Repeat([]byte{0x58}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	defer store5.Close()
	if result, err := store5.RevokeDevice(context.Background(), RevokeDeviceRequest{DeviceID: device.ID, Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: RevocationReasonAdministrator}); err != nil || !result.Already {
		t.Fatalf("restart revocation retry result=%+v err=%v", result, err)
	}
	if _, err := store5.Authenticate(context.Background(), AuthenticateRequest{DeviceID: device.ID, Fingerprint: "fp-1"}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("restart revoked authentication: %v", err)
	}
}

func TestAuditFailureRollsBackEnrollment(t *testing.T) {
	now := time.Date(2026, 9, 11, 17, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	store := openTestStore(t, clock, &testIssuer{clock: clock}, bytes.Repeat([]byte{0x58}, 32))
	if _, err := store.db.Exec(`CREATE TRIGGER block_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'audit failure injection'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := store.CreateEnrollment(context.Background(), CreateEnrollmentRequest{ID: "enrollment-1", DeviceID: "device-1", Name: "workstation", Platform: PlatformLinux, Architecture: ArchitectureX8664, Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}})
	if err == nil {
		t.Fatal("audit failure unexpectedly succeeded")
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM enrollments`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("enrollments after audit failure = %d", count)
	}
}

func TestCertificateFailureRollsBackActivation(t *testing.T) {
	now := time.Date(2026, 9, 11, 18, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	spki, private := testKeys(t)
	store := openTestStore(t, clock, &testIssuer{clock: clock}, bytes.Repeat([]byte{0x59}, 64))
	enrollment := createEnrollment(t, store)
	challenge := issueChallenge(t, store, enrollment, spki)
	if _, err := store.db.Exec(`CREATE TRIGGER block_certificate BEFORE INSERT ON certificates BEGIN SELECT RAISE(ABORT, 'certificate failure injection'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signChallenge(challenge, enrollment, private)}); err == nil {
		t.Fatal("certificate failure unexpectedly succeeded")
	}
	var devices, challenges, enrollments int
	if err := store.db.QueryRow(`SELECT count(*) FROM devices`).Scan(&devices); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM challenges WHERE state='pending'`).Scan(&challenges); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM enrollments WHERE state='challenged'`).Scan(&enrollments); err != nil {
		t.Fatal(err)
	}
	if devices != 0 || challenges != 1 || enrollments != 1 {
		t.Fatalf("certificate rollback devices=%d challenges=%d enrollments=%d", devices, challenges, enrollments)
	}
}

func TestClosureIntentFailureRollsBackRevocation(t *testing.T) {
	now := time.Date(2026, 9, 11, 19, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	spki, private := testKeys(t)
	actor := Actor{ID: "operator-1", PolicyID: "policy-1"}
	store := openTestStore(t, clock, &testIssuer{clock: clock}, bytes.Repeat([]byte{0x5a}, 64))
	enrollment := createEnrollment(t, store)
	challenge := issueChallenge(t, store, enrollment, spki)
	device, err := store.CompleteEnrollment(context.Background(), CompleteEnrollmentRequest{ChallengeID: challenge.ID, Nonce: challenge.Nonce, Signature: signChallenge(challenge, enrollment, private)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterSession(context.Background(), RegisterSessionRequest{SessionID: "session-1", DeviceID: device.ID, Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER block_closure BEFORE INSERT ON closure_intents BEGIN SELECT RAISE(ABORT, 'closure failure injection'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeDevice(context.Background(), RevokeDeviceRequest{DeviceID: device.ID, Actor: actor, Reason: RevocationReasonAdministrator}); err == nil {
		t.Fatal("closure failure unexpectedly succeeded")
	}
	var state string
	if err := store.db.QueryRow(`SELECT state FROM devices WHERE id=?`, device.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	var revocations, intents int
	if err := store.db.QueryRow(`SELECT count(*) FROM revocations`).Scan(&revocations); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM closure_intents`).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if state != "active" || revocations != 0 || intents != 0 {
		t.Fatalf("closure rollback state=%s revocations=%d intents=%d", state, revocations, intents)
	}
}

func TestEntropyAndAdministrativeVocabularyRejects(t *testing.T) {
	now := time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	store := openTestStore(t, clock, &testIssuer{clock: clock}, bytes.Repeat([]byte{0x5b}, 31))
	if _, err := store.CreateEnrollment(context.Background(), CreateEnrollmentRequest{ID: "enrollment-1", DeviceID: "device-1", Name: "workstation", Platform: PlatformLinux, Architecture: ArchitectureX8664, Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}}); err == nil {
		t.Fatal("short entropy unexpectedly succeeded")
	}
	if _, err := store.CreateEnrollment(context.Background(), CreateEnrollmentRequest{ID: "enrollment-1", DeviceID: "device-1", Name: "workstation", Platform: PlatformLinux, Architecture: ArchitectureX8664}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing actor = %v", err)
	}
	if !validEventKind(EventChallengeIssued) || validEventKind(EventKind("arbitrary")) || !validOutcome(OutcomeAllow) || validOutcome(Outcome("arbitrary")) || !validReason(ReasonAdministrator) || validReason(ReasonCode("secret-bearing reason")) {
		t.Fatal("closed vocabularies are not enforced")
	}
	if _, err := store.db.Exec(`INSERT INTO audit_events(id,event_kind,aggregate_type,aggregate_id,outcome,reason,created_at) VALUES ('aud-raw','enrollment.created','enrollment','enrollment-raw','allow','created',?)`, now.UnixNano()); err == nil {
		t.Fatal("unattributed administrative audit unexpectedly inserted")
	}
	if _, err := store.RegisterSession(context.Background(), RegisterSessionRequest{SessionID: "session-1", DeviceID: "device-1"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing session actor = %v", err)
	}
	if _, err := store.CloseSession(context.Background(), CloseSessionRequest{SessionID: "session-1", Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: SessionCloseReason("certificate")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-domain session reason = %v", err)
	}
	if _, err := store.CloseSession(context.Background(), CloseSessionRequest{SessionID: "session-1", Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: SessionCloseReason("")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero session reason = %v", err)
	}
	if _, err := store.CloseSession(context.Background(), CloseSessionRequest{SessionID: "session-1", Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: SessionCloseReason("not-allowlisted")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown session reason = %v", err)
	}
	if _, err := store.RevokeDevice(context.Background(), RevokeDeviceRequest{DeviceID: "device-1", Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: RevocationReason("heartbeat_timeout")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-domain revocation reason = %v", err)
	}
	if _, err := store.RevokeDevice(context.Background(), RevokeDeviceRequest{DeviceID: "device-1", Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: RevocationReason("")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero revocation reason = %v", err)
	}
	if _, err := store.RevokeDevice(context.Background(), RevokeDeviceRequest{DeviceID: "device-1", Actor: Actor{ID: "operator-1", PolicyID: "policy-1"}, Reason: RevocationReason("not-allowlisted")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown revocation reason = %v", err)
	}
}
