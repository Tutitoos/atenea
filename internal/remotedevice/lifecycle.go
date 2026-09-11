//nolint:revive // The lifecycle API is documented at the package boundary.
package remotedevice

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const maxSignatureBytes = 512

type CompleteEnrollmentRequest struct {
	ChallengeID string
	Nonce       []byte
	Signature   []byte
	RequestID   string
}

type Device struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	Platform            Platform            `json:"platform"`
	Architecture        Architecture        `json:"architecture"`
	State               string              `json:"state"`
	Fence               int64               `json:"fence"`
	ActiveCertificateID string              `json:"active_certificate_id"`
	Certificate         CertificateMetadata `json:"certificate"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

type AuthenticateRequest struct {
	DeviceID        string
	CertificateID   string
	Fingerprint     string
	PublicKeyDigest []byte
	RequestID       string
}

// AuthenticateAndRegisterSessionRequest combines certificate authentication
// and session admission.  The registry evaluates all identity material and
// creates the session while holding the same immediate SQLite transaction;
// callers cannot authorize from a snapshot that can be renewed or revoked
// before registration.
type AuthenticateAndRegisterSessionRequest struct {
	SessionID       string
	DeviceID        string
	CertificateID   string
	Fingerprint     string
	PublicKeyDigest []byte
	Platform        Platform
	Architecture    Architecture
	Actor           Actor
	RequestID       string
}

// ErrSessionConflict means that the requested session identifier already has
// a durable history.  Session IDs are never reopened or rebound.
var ErrSessionConflict = errors.New("remote device: session already exists")

type Session struct {
	ID        string             `json:"id"`
	DeviceID  string             `json:"device_id"`
	State     string             `json:"state"`
	Fence     int64              `json:"fence"`
	Reason    SessionCloseReason `json:"reason,omitempty"`
	CreatedAt time.Time          `json:"created_at"`
	ClosedAt  time.Time          `json:"closed_at,omitempty"`
}

type RegisterSessionRequest struct {
	SessionID string
	DeviceID  string
	Actor     Actor
	RequestID string
}

type CloseSessionRequest struct {
	SessionID string
	DeviceID  string
	Actor     Actor
	Reason    SessionCloseReason
	RequestID string
}

type ClosureIntent struct {
	ID           string           `json:"id"`
	DeviceID     string           `json:"device_id"`
	SessionID    string           `json:"session_id"`
	RevocationID string           `json:"revocation_id"`
	Fence        int64            `json:"fence"`
	Reason       RevocationReason `json:"reason"`
	State        string           `json:"state"`
	CreatedAt    time.Time        `json:"created_at"`
	AppliedAt    time.Time        `json:"applied_at,omitempty"`
}

type Revocation struct {
	ID        string           `json:"id"`
	DeviceID  string           `json:"device_id"`
	Actor     Actor            `json:"actor"`
	Reason    RevocationReason `json:"reason"`
	State     string           `json:"state"`
	CreatedAt time.Time        `json:"created_at"`
}

type RevokeDeviceRequest struct {
	DeviceID  string
	Actor     Actor
	Reason    RevocationReason
	RequestID string
}

type RevokeDeviceResponse struct {
	Device     Device
	Revocation Revocation
	Intents    []ClosureIntent
	Already    bool
}

type AuditEvent struct {
	Sequence      int64      `json:"sequence"`
	ID            string     `json:"id"`
	EventKind     EventKind  `json:"event_kind"`
	AggregateType string     `json:"aggregate_type"`
	AggregateID   string     `json:"aggregate_id"`
	DeviceID      string     `json:"device_id,omitempty"`
	SessionID     string     `json:"session_id,omitempty"`
	EnrollmentID  string     `json:"enrollment_id,omitempty"`
	RequestID     string     `json:"request_id,omitempty"`
	Actor         Actor      `json:"actor,omitempty"`
	Outcome       Outcome    `json:"outcome"`
	Reason        ReasonCode `json:"reason"`
	DigestRef     string     `json:"digest_ref,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type ClosureIntentFilter struct {
	DeviceID string
	Limit    int
}

type AuditFilter struct {
	DeviceID    string
	AggregateID string
	Limit       int
}

type completionRow struct {
	challengeID, enrollmentID, deviceID, name, platform, architecture      string
	challengeState, enrollmentState                                        string
	nonceDigest, contextDigest, originalContextDigest, publicDigest        string
	publicSPKI                                                             []byte
	issuedNS, challengeExpiresNS, enrollmentExpiresNS, enrollmentUpdatedNS int64
	actorID, policyID                                                      string
}

func (s *Store) CompleteEnrollment(ctx context.Context, req CompleteEnrollmentRequest) (Device, error) {
	var result Device
	if err := s.ensureOpen(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := validateID(req.ChallengeID, "challenge id"); err != nil {
		return result, err
	}
	if len(req.Nonce) != 32 || len(req.Signature) == 0 || len(req.Signature) > maxSignatureBytes {
		return result, fmt.Errorf("%w: nonce or signature length", ErrInvalidInput)
	}
	nonce := append([]byte(nil), req.Nonce...)
	signature := append([]byte(nil), req.Signature...)
	defer wipeBytes(nonce)
	defer wipeBytes(signature)
	requestID, err := validateRequestID(req.RequestID)
	if err != nil {
		return result, err
	}
	now := s.currentTime()
	row, err := s.readCompletionRow(ctx, req.ChallengeID)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrChallengeNotFound
	}
	if err != nil {
		return result, err
	}
	if row.challengeState != string(ChallengePending) || row.enrollmentState != string(EnrollmentChallenged) {
		return result, ErrChallengeUsed
	}
	if now.UnixNano() >= row.challengeExpiresNS || now.UnixNano() >= row.enrollmentExpiresNS {
		if err := s.expireCompletion(ctx, row, now, requestID); err != nil {
			return result, err
		}
		return result, ErrEnrollmentExpired
	}
	nonceDigest := Digest("challenge-nonce", nonce)
	storedNonce, err := hex.DecodeString(row.nonceDigest)
	if err != nil || subtleCompare(storedNonce, nonceDigest[:]) != 1 {
		if failErr := s.failCompletion(ctx, row, now, requestID, ReasonBindingMismatch); failErr != nil {
			return result, failErr
		}
		return result, ErrBindingMismatch
	}
	canonicalSPKI, publicKey, err := parsePublicKey(row.publicSPKI)
	if err != nil {
		if failErr := s.failCompletion(ctx, row, now, requestID, ReasonProofInvalid); failErr != nil {
			return result, failErr
		}
		return result, ErrProofInvalid
	}
	message := canonicalChallengeMessage(row.challengeID, row.enrollmentID, row.deviceID, row.name, Platform(row.platform), Architecture(row.architecture), row.enrollmentExpiresNS, row.issuedNS, row.challengeExpiresNS, nonceDigest[:], digestBytes(row.originalContextDigest), canonicalSPKI, digestBytes(row.publicDigest))
	if !verifySignature(publicKey, message, signature) {
		if err := s.failCompletion(ctx, row, now, requestID, ReasonProofInvalid); err != nil {
			return result, err
		}
		return result, ErrProofInvalid
	}
	issuer := s.certificateIssuer
	if issuer == nil {
		return result, ErrIssuerUnavailable
	}
	issuerRequest := CertificateIssueRequest{ChallengeID: row.challengeID, EnrollmentID: row.enrollmentID, DeviceID: row.deviceID, Name: row.name, Platform: Platform(row.platform), Architecture: Architecture(row.architecture), PublicKeySPKI: append([]byte(nil), canonicalSPKI...), PublicKeyDigest: digestBytes(row.publicDigest), ContextDigest: digestBytes(row.contextDigest), Actor: Actor{ID: row.actorID, PolicyID: row.policyID}}
	metadata, err := issuer.Issue(ctx, issuerRequest)
	wipeBytes(issuerRequest.PublicKeySPKI)
	wipeBytes(issuerRequest.PublicKeyDigest)
	wipeBytes(issuerRequest.ContextDigest)
	if err != nil {
		// No durable state changed: retrying invokes the issuer again.
		return result, fmt.Errorf("remote device: certificate issuer: %w", err)
	}
	metadata, err = validateCertificateMetadata(metadata, digestBytes(row.publicDigest), now)
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Device{}, fmt.Errorf("remote device: begin completion commit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	finalNow := s.currentTime()
	var state, enrollmentState string
	var enrollmentUpdated, challengeExpires, enrollmentExpires int64
	if err := tx.QueryRowContext(ctx, `SELECT c.state,e.state,e.updated_at,c.expires_at,e.expires_at FROM challenges c JOIN enrollments e ON e.id=c.enrollment_id WHERE c.id=?`, row.challengeID).Scan(&state, &enrollmentState, &enrollmentUpdated, &challengeExpires, &enrollmentExpires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, ErrChallengeNotFound
		}
		return Device{}, fmt.Errorf("remote device: revalidate completion: %w", err)
	}
	if state != string(ChallengePending) || enrollmentState != string(EnrollmentChallenged) || enrollmentUpdated != row.enrollmentUpdatedNS {
		return Device{}, ErrChallengeUsed
	}
	if finalNow.UnixNano() >= challengeExpires || finalNow.UnixNano() >= enrollmentExpires {
		if err := expireCompletionTx(ctx, tx, row, finalNow, requestID); err != nil {
			return Device{}, err
		}
		if err := tx.Commit(); err != nil {
			return Device{}, fmt.Errorf("remote device: commit completion expiry: %w", err)
		}
		return Device{}, ErrEnrollmentExpired
	}
	metadata, err = validateCertificateMetadata(metadata, digestBytes(row.publicDigest), finalNow)
	if err != nil {
		return Device{}, err
	}
	result = Device{ID: row.deviceID, Name: row.name, Platform: Platform(row.platform), Architecture: Architecture(row.architecture), State: "active", Fence: 0, ActiveCertificateID: metadata.ID, Certificate: metadata, CreatedAt: finalNow, UpdatedAt: finalNow}
	result.Certificate.PublicKeyDigest = append([]byte(nil), metadata.PublicKeyDigest...)
	defer wipeBytes(metadata.PublicKeyDigest)
	if _, err := tx.ExecContext(ctx, `INSERT INTO devices(id,name,platform,architecture,state,fence,active_certificate_id,created_at,updated_at) VALUES (?,?,?,?, 'active',0,?,?,?)`, row.deviceID, row.name, row.platform, row.architecture, metadata.ID, finalNow.UnixNano(), finalNow.UnixNano()); err != nil {
		return Device{}, fmt.Errorf("remote device: activate device: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO certificates(id,device_id,issuer_id,serial,fingerprint,public_key_digest,state,not_before,not_after,created_at) VALUES (?,?,?,?,?,?, 'active',?,?,?)`, metadata.ID, row.deviceID, metadata.IssuerID, metadata.Serial, metadata.Fingerprint, hex.EncodeToString(metadata.PublicKeyDigest), metadata.NotBefore.UnixNano(), metadata.NotAfter.UnixNano(), finalNow.UnixNano()); err != nil {
		return Device{}, fmt.Errorf("remote device: persist certificate: %w", err)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE challenges SET state='consumed' WHERE id=? AND state='pending'`, row.challengeID)
	if err != nil {
		return Device{}, fmt.Errorf("remote device: consume challenge: %w", err)
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return Device{}, ErrChallengeUsed
	}
	updated, err = tx.ExecContext(ctx, `UPDATE enrollments SET state='active',updated_at=? WHERE id=? AND state='challenged' AND updated_at=?`, finalNow.UnixNano(), row.enrollmentID, row.enrollmentUpdatedNS)
	if err != nil {
		return Device{}, fmt.Errorf("remote device: activate enrollment: %w", err)
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return Device{}, ErrChallengeUsed
	}
	actor := Actor{ID: row.actorID, PolicyID: row.policyID}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventChallengeConsumed, AggregateType: "challenge", AggregateID: row.challengeID, DeviceID: row.deviceID, EnrollmentID: row.enrollmentID, RequestID: requestID, Actor: actor, Outcome: OutcomeAllow, Reason: ReasonProofVerified, DigestRef: row.contextDigest, CreatedAt: finalNow}); err != nil {
		return Device{}, err
	}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventEnrollmentConsumed, AggregateType: "enrollment", AggregateID: row.enrollmentID, DeviceID: row.deviceID, EnrollmentID: row.enrollmentID, RequestID: requestID, Actor: actor, Outcome: OutcomeAllow, Reason: ReasonEnrollmentConsumed, DigestRef: row.contextDigest, CreatedAt: finalNow}); err != nil {
		return Device{}, err
	}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventCertificateIssued, AggregateType: "certificate", AggregateID: metadata.ID, DeviceID: row.deviceID, EnrollmentID: row.enrollmentID, RequestID: requestID, Actor: actor, Outcome: OutcomeAllow, Reason: ReasonCertificateIssued, DigestRef: row.publicDigest, CreatedAt: finalNow}); err != nil {
		return Device{}, err
	}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventDeviceActivated, AggregateType: "device", AggregateID: row.deviceID, DeviceID: row.deviceID, EnrollmentID: row.enrollmentID, RequestID: requestID, Actor: actor, Outcome: OutcomeAllow, Reason: ReasonEnrollmentCompleted, DigestRef: row.publicDigest, CreatedAt: finalNow}); err != nil {
		return Device{}, err
	}
	if err := tx.Commit(); err != nil {
		return Device{}, fmt.Errorf("remote device: commit completion: %w", err)
	}
	return result, nil
}

func (s *Store) readCompletionRow(ctx context.Context, challengeID string) (completionRow, error) {
	var row completionRow
	err := s.db.QueryRowContext(ctx, `SELECT c.id,c.enrollment_id,c.device_id,c.name,c.platform,c.architecture,c.state,c.nonce_digest,c.context_digest,c.original_context_digest,c.public_key_spki,c.public_key_digest,c.actor_id,c.policy_id,c.issued_at,c.expires_at,e.state,e.expires_at,e.updated_at FROM challenges c JOIN enrollments e ON e.id=c.enrollment_id WHERE c.id=?`, challengeID).Scan(&row.challengeID, &row.enrollmentID, &row.deviceID, &row.name, &row.platform, &row.architecture, &row.challengeState, &row.nonceDigest, &row.contextDigest, &row.originalContextDigest, &row.publicSPKI, &row.publicDigest, &row.actorID, &row.policyID, &row.issuedNS, &row.challengeExpiresNS, &row.enrollmentState, &row.enrollmentExpiresNS, &row.enrollmentUpdatedNS)
	if errors.Is(err, sql.ErrNoRows) {
		return completionRow{}, sql.ErrNoRows
	}
	if err != nil {
		return completionRow{}, fmt.Errorf("remote device: read completion state: %w", err)
	}
	return row, nil
}

func (s *Store) expireCompletion(ctx context.Context, row completionRow, now time.Time, requestID string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := expireCompletionTx(ctx, tx, row, now, requestID); err != nil {
		return err
	}
	return tx.Commit()
}

func expireCompletionTx(ctx context.Context, tx *sql.Tx, row completionRow, now time.Time, requestID string) error {
	updated, err := tx.ExecContext(ctx, `UPDATE challenges SET state='expired' WHERE id=? AND state='pending'`, row.challengeID)
	if err != nil {
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return ErrChallengeUsed
	}
	if _, err := tx.ExecContext(ctx, `UPDATE enrollments SET state='expired',updated_at=? WHERE id=? AND state='challenged'`, now.UnixNano(), row.enrollmentID); err != nil {
		return err
	}
	actor := Actor{ID: row.actorID, PolicyID: row.policyID}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventChallengeExpired, AggregateType: "challenge", AggregateID: row.challengeID, DeviceID: row.deviceID, EnrollmentID: row.enrollmentID, RequestID: requestID, Actor: actor, Outcome: OutcomeDeny, Reason: ReasonExpired, CreatedAt: now}); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventEnrollmentExpired, AggregateType: "enrollment", AggregateID: row.enrollmentID, DeviceID: row.deviceID, EnrollmentID: row.enrollmentID, RequestID: requestID, Actor: actor, Outcome: OutcomeDeny, Reason: ReasonExpired, CreatedAt: now}); err != nil {
		return err
	}
	return nil
}

func (s *Store) failCompletion(ctx context.Context, row completionRow, now time.Time, requestID string, reason ReasonCode) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	updated, err := tx.ExecContext(ctx, `UPDATE challenges SET state='failed' WHERE id=? AND state='pending'`, row.challengeID)
	if err != nil {
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return ErrChallengeUsed
	}
	if _, err := tx.ExecContext(ctx, `UPDATE enrollments SET state='failed',updated_at=? WHERE id=? AND state='challenged'`, now.UnixNano(), row.enrollmentID); err != nil {
		return err
	}
	actor := Actor{ID: row.actorID, PolicyID: row.policyID}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventChallengeFailed, AggregateType: "challenge", AggregateID: row.challengeID, DeviceID: row.deviceID, EnrollmentID: row.enrollmentID, RequestID: requestID, Actor: actor, Outcome: OutcomeDeny, Reason: reason, CreatedAt: now}); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventEnrollmentFailed, AggregateType: "enrollment", AggregateID: row.enrollmentID, DeviceID: row.deviceID, EnrollmentID: row.enrollmentID, RequestID: requestID, Actor: actor, Outcome: OutcomeDeny, Reason: reason, CreatedAt: now}); err != nil {
		return err
	}
	return tx.Commit()
}

func parsePublicKey(input []byte) ([]byte, crypto.PublicKey, error) {
	if len(input) == 0 || len(input) > maxSPKIBytes {
		return nil, nil, fmt.Errorf("%w: public key SPKI size", ErrInvalidInput)
	}
	key, err := x509.ParsePKIXPublicKey(input)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: parse public key SPKI: %v", ErrInvalidInput, err)
	}
	canonical, err := x509.MarshalPKIXPublicKey(key)
	if err != nil || len(canonical) > maxSPKIBytes {
		return nil, nil, fmt.Errorf("%w: canonical public key SPKI", ErrInvalidInput)
	}
	switch typed := key.(type) {
	case ed25519.PublicKey:
		return canonical, typed, nil
	case *ecdsa.PublicKey:
		if typed.Curve != elliptic.P256() {
			return nil, nil, fmt.Errorf("%w: only ECDSA P-256 is supported", ErrInvalidInput)
		}
		return canonical, typed, nil
	default:
		return nil, nil, fmt.Errorf("%w: only Ed25519 and ECDSA P-256 are supported", ErrInvalidInput)
	}
}

func verifySignature(key crypto.PublicKey, message, signature []byte) bool {
	switch typed := key.(type) {
	case ed25519.PublicKey:
		return ed25519.Verify(typed, message, signature)
	case *ecdsa.PublicKey:
		digest := sha256.Sum256(message)
		return ecdsa.VerifyASN1(typed, digest[:], signature)
	default:
		return false
	}
}

func validateCertificateMetadata(metadata CertificateMetadata, publicKeyDigest []byte, now time.Time) (CertificateMetadata, error) {
	if err := validateID(metadata.ID, "certificate id"); err != nil || metadata.IssuerID == "" || len(metadata.IssuerID) > 256 || metadata.Serial == "" || len(metadata.Serial) > 256 || metadata.Fingerprint == "" || len(metadata.Fingerprint) > 256 {
		return CertificateMetadata{}, fmt.Errorf("%w: certificate fields", ErrCertificateInvalid)
	}
	if subtleCompare(metadata.PublicKeyDigest, publicKeyDigest) != 1 {
		return CertificateMetadata{}, fmt.Errorf("%w: public-key binding", ErrCertificateInvalid)
	}
	if metadata.NotBefore.IsZero() || metadata.NotAfter.IsZero() || metadata.NotBefore.After(now) || !metadata.NotAfter.After(now) || metadata.NotAfter.After(now.Add(maxCertificateTTL)) || metadata.NotAfter.Sub(metadata.NotBefore) > maxCertificateTTL {
		return CertificateMetadata{}, fmt.Errorf("%w: certificate is not currently valid or exceeds maximum lifetime", ErrCertificateInvalid)
	}
	metadata.PublicKeyDigest = append([]byte(nil), publicKeyDigest...)
	metadata.State = "active"
	metadata.CreatedAt = now
	return metadata, nil
}

// RecordCertificateRenewal records metadata issued by the configured local
// issuer. Certificate bytes and CA operations are deliberately outside this
// registry contract. The current certificate is superseded only in the same
// transaction that makes the replacement active.
type RecordCertificateRenewalRequest struct {
	DeviceID              string
	PreviousCertificateID string
	Metadata              CertificateMetadata
	Actor                 Actor
	RequestID             string
}

func (s *Store) RecordCertificateRenewal(ctx context.Context, req RecordCertificateRenewalRequest) (CertificateMetadata, error) {
	if err := s.ensureOpen(); err != nil {
		return CertificateMetadata{}, err
	}
	if err := ctx.Err(); err != nil {
		return CertificateMetadata{}, err
	}
	if err := validateID(req.DeviceID, "device id"); err != nil {
		return CertificateMetadata{}, err
	}
	if err := validateID(req.PreviousCertificateID, "previous certificate id"); err != nil {
		return CertificateMetadata{}, err
	}
	if err := validateActor(req.Actor); err != nil {
		return CertificateMetadata{}, err
	}
	requestID, err := validateRequestID(req.RequestID)
	if err != nil {
		return CertificateMetadata{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return CertificateMetadata{}, fmt.Errorf("remote device: begin certificate renewal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.currentTime()
	var deviceState, activeCertificateID string
	if err := tx.QueryRowContext(ctx, `SELECT state,COALESCE(active_certificate_id,'') FROM devices WHERE id=?`, req.DeviceID).Scan(&deviceState, &activeCertificateID); errors.Is(err, sql.ErrNoRows) {
		return CertificateMetadata{}, ErrDeviceNotFound
	} else if err != nil {
		return CertificateMetadata{}, err
	}
	if deviceState != "active" {
		return CertificateMetadata{}, ErrDeviceRevoked
	}
	if activeCertificateID == "" || activeCertificateID != req.PreviousCertificateID {
		return CertificateMetadata{}, ErrRenewalConflict
	}
	var currentState, currentDigest string
	var currentBefore, currentAfter int64
	if err := tx.QueryRowContext(ctx, `SELECT state,public_key_digest,not_before,not_after FROM certificates WHERE id=? AND device_id=?`, req.PreviousCertificateID, req.DeviceID).Scan(&currentState, &currentDigest, &currentBefore, &currentAfter); errors.Is(err, sql.ErrNoRows) {
		return CertificateMetadata{}, ErrCertificateNotFound
	} else if err != nil {
		return CertificateMetadata{}, err
	}
	if currentState != "active" || currentBefore > now.UnixNano() || currentAfter <= now.UnixNano() {
		return CertificateMetadata{}, ErrAuthentication
	}
	currentPublicKeyDigest := digestBytes(currentDigest)
	metadata, err := validateCertificateMetadata(req.Metadata, currentPublicKeyDigest, now)
	if err != nil {
		return CertificateMetadata{}, err
	}
	if metadata.ID == req.PreviousCertificateID {
		return CertificateMetadata{}, ErrRenewalConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE certificates SET state='superseded' WHERE id=? AND device_id=? AND state='active'`, req.PreviousCertificateID, req.DeviceID); err != nil {
		return CertificateMetadata{}, fmt.Errorf("remote device: supersede certificate: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO certificates(id,device_id,issuer_id,serial,fingerprint,public_key_digest,state,not_before,not_after,created_at) VALUES (?,?,?,?,?,?, 'active',?,?,?)`, metadata.ID, req.DeviceID, metadata.IssuerID, metadata.Serial, metadata.Fingerprint, hex.EncodeToString(metadata.PublicKeyDigest), metadata.NotBefore.UnixNano(), metadata.NotAfter.UnixNano(), now.UnixNano()); err != nil {
		return CertificateMetadata{}, fmt.Errorf("remote device: persist renewed certificate: %w", err)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE devices SET active_certificate_id=?,updated_at=? WHERE id=? AND state='active' AND active_certificate_id=?`, metadata.ID, now.UnixNano(), req.DeviceID, req.PreviousCertificateID)
	if err != nil {
		return CertificateMetadata{}, fmt.Errorf("remote device: update active certificate: %w", err)
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return CertificateMetadata{}, ErrRenewalConflict
	}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventCertificateRenewed, AggregateType: "certificate", AggregateID: metadata.ID, DeviceID: req.DeviceID, RequestID: requestID, Actor: req.Actor, Outcome: OutcomeAllow, Reason: ReasonCertificateRenewed, DigestRef: hex.EncodeToString(metadata.PublicKeyDigest), CreatedAt: now}); err != nil {
		return CertificateMetadata{}, err
	}
	if err := tx.Commit(); err != nil {
		return CertificateMetadata{}, fmt.Errorf("remote device: commit certificate renewal: %w", err)
	}
	metadata.PublicKeyDigest = append([]byte(nil), metadata.PublicKeyDigest...)
	return metadata, nil
}

func (s *Store) GetDevice(ctx context.Context, deviceID string) (Device, error) {
	if err := s.ensureOpen(); err != nil {
		return Device{}, err
	}
	if err := validateID(deviceID, "device id"); err != nil {
		return Device{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Device{}, err
	}
	defer func() { _ = tx.Rollback() }()
	device, err := readDevice(ctx, tx, deviceID, s.currentTime())
	if err != nil {
		return Device{}, err
	}
	if err := tx.Commit(); err != nil {
		return Device{}, err
	}
	return device, nil
}

func readDevice(ctx context.Context, tx *sql.Tx, deviceID string, now time.Time) (Device, error) {
	var d Device
	var state, certState string
	var certDigest string
	var created, updated, before, after, certCreated int64
	err := tx.QueryRowContext(ctx, `SELECT d.id,d.name,d.platform,d.architecture,d.state,d.fence,d.active_certificate_id,d.created_at,d.updated_at,c.id,c.issuer_id,c.serial,c.fingerprint,c.public_key_digest,c.state,c.not_before,c.not_after,c.created_at FROM devices d JOIN certificates c ON c.id=d.active_certificate_id WHERE d.id=?`, deviceID).Scan(&d.ID, &d.Name, &d.Platform, &d.Architecture, &state, &d.Fence, &d.ActiveCertificateID, &created, &updated, &d.Certificate.ID, &d.Certificate.IssuerID, &d.Certificate.Serial, &d.Certificate.Fingerprint, &certDigest, &certState, &before, &after, &certCreated)
	if errors.Is(err, sql.ErrNoRows) {
		var deviceState string
		if stateErr := tx.QueryRowContext(ctx, `SELECT state FROM devices WHERE id=?`, deviceID).Scan(&deviceState); errors.Is(stateErr, sql.ErrNoRows) {
			return Device{}, ErrDeviceNotFound
		}
		return Device{}, ErrDeviceRevoked
	}
	if err != nil {
		return Device{}, err
	}
	if state != "active" || certState != "active" || before > now.UnixNano() || after <= now.UnixNano() {
		return Device{}, ErrAuthentication
	}
	d.State, d.CreatedAt, d.UpdatedAt = state, time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	d.Certificate.PublicKeyDigest = digestBytes(certDigest)
	d.Certificate.State = certState
	d.Certificate.NotBefore, d.Certificate.NotAfter, d.Certificate.CreatedAt = time.Unix(0, before).UTC(), time.Unix(0, after).UTC(), time.Unix(0, certCreated).UTC()
	return d, nil
}

func (s *Store) Authenticate(ctx context.Context, req AuthenticateRequest) (Device, error) {
	if err := s.ensureOpen(); err != nil {
		return Device{}, err
	}
	if err := validateID(req.DeviceID, "device id"); err != nil {
		return Device{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Device{}, err
	}
	defer func() { _ = tx.Rollback() }()
	device, err := readDevice(ctx, tx, req.DeviceID, s.currentTime())
	if err != nil {
		return Device{}, ErrAuthentication
	}
	if req.Fingerprint == "" || subtleString(device.Certificate.Fingerprint, req.Fingerprint) != 1 || (req.CertificateID != "" && subtleString(device.Certificate.ID, req.CertificateID) != 1) || (len(req.PublicKeyDigest) > 0 && subtleCompare(device.Certificate.PublicKeyDigest, req.PublicKeyDigest) != 1) {
		return Device{}, ErrAuthentication
	}
	if err := tx.Commit(); err != nil {
		return Device{}, err
	}
	return device, nil
}

// AuthenticateAndRegisterSession authenticates the current device
// certificate and registers a new active session as one atomic transition.
// The immediate transaction is important: certificate renewal and device
// revocation cannot interleave between identity validation and session
// insertion.  An existing identifier is always rejected, including a closed
// tombstone; it is never an idempotent recovery path.
func (s *Store) AuthenticateAndRegisterSession(ctx context.Context, req AuthenticateAndRegisterSessionRequest) (Session, error) {
	if err := s.ensureOpen(); err != nil {
		return Session{}, err
	}
	if err := validateID(req.SessionID, "session id"); err != nil {
		return Session{}, err
	}
	if err := validateID(req.DeviceID, "device id"); err != nil {
		return Session{}, err
	}
	if err := validateActor(req.Actor); err != nil {
		return Session{}, err
	}
	requestID, err := validateRequestID(req.RequestID)
	if err != nil {
		return Session{}, err
	}
	// A new admission must always prove both certificate bindings.  Keeping
	// these checks at the combined boundary prevents callers from accidentally
	// selecting the permissive legacy Authenticate API for session creation.
	if req.Fingerprint == "" || len(req.PublicKeyDigest) == 0 {
		return Session{}, ErrAuthentication
	}

	identityDigest := append([]byte(nil), req.PublicKeyDigest...)
	defer wipeBytes(identityDigest)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// The SQLite DSN uses _txlock=immediate.  Take the clock only after the
	// transaction has acquired that lock so expiry and audit timestamps belong
	// to the same serialization point as renewal and revocation.
	now := s.currentTime()
	device, err := readDevice(ctx, tx, req.DeviceID, now)
	if err != nil {
		return Session{}, ErrAuthentication
	}
	if subtleString(device.Certificate.Fingerprint, req.Fingerprint) != 1 ||
		(req.CertificateID != "" && subtleString(device.Certificate.ID, req.CertificateID) != 1) ||
		subtleCompare(device.Certificate.PublicKeyDigest, identityDigest) != 1 {
		return Session{}, ErrAuthentication
	}
	if (req.Platform != "" && req.Platform != device.Platform) ||
		(req.Architecture != "" && req.Architecture != device.Architecture) {
		return Session{}, ErrAuthentication
	}

	var existingID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM sessions WHERE id=?`, req.SessionID).Scan(&existingID); err == nil {
		return Session{}, fmt.Errorf("%w: %s", ErrSessionConflict, req.SessionID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Session{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,device_id,state,fence,reason,created_at) VALUES (?,?, 'active',?,'',?)`, req.SessionID, req.DeviceID, device.Fence, now.UnixNano()); err != nil {
		return Session{}, err
	}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventSessionRegistered, AggregateType: "session", AggregateID: req.SessionID, DeviceID: req.DeviceID, SessionID: req.SessionID, RequestID: requestID, Actor: req.Actor, Outcome: OutcomeAllow, Reason: ReasonRegistered, CreatedAt: now}); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return Session{ID: req.SessionID, DeviceID: req.DeviceID, State: "active", Fence: device.Fence, CreatedAt: now}, nil
}

func (s *Store) RegisterSession(ctx context.Context, req RegisterSessionRequest) (Session, error) {
	if err := s.ensureOpen(); err != nil {
		return Session{}, err
	}
	if err := validateID(req.SessionID, "session id"); err != nil {
		return Session{}, err
	}
	if err := validateID(req.DeviceID, "device id"); err != nil {
		return Session{}, err
	}
	requestID, err := validateRequestID(req.RequestID)
	if err != nil {
		return Session{}, err
	}
	if err := validateActor(req.Actor); err != nil {
		return Session{}, err
	}
	now := s.currentTime()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var deviceState string
	var fence int64
	if err := tx.QueryRowContext(ctx, `SELECT state,fence FROM devices WHERE id=?`, req.DeviceID).Scan(&deviceState, &fence); errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrDeviceNotFound
	} else if err != nil {
		return Session{}, err
	} else if deviceState != "active" {
		return Session{}, ErrDeviceRevoked
	}
	var session Session
	var created, closed sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id,device_id,state,fence,reason,created_at,closed_at FROM sessions WHERE id=?`, req.SessionID).Scan(&session.ID, &session.DeviceID, &session.State, &session.Fence, &session.Reason, &created, &closed)
	if err == nil {
		if session.DeviceID != req.DeviceID {
			return Session{}, ErrInvalidInput
		}
		session.CreatedAt = time.Unix(0, created.Int64).UTC()
		if closed.Valid {
			session.ClosedAt = time.Unix(0, closed.Int64).UTC()
		}
		return session, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Session{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,device_id,state,fence,reason,created_at) VALUES (?,?, 'active',?,'',?)`, req.SessionID, req.DeviceID, fence, now.UnixNano()); err != nil {
		return Session{}, err
	}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventSessionRegistered, AggregateType: "session", AggregateID: req.SessionID, DeviceID: req.DeviceID, SessionID: req.SessionID, RequestID: requestID, Actor: req.Actor, Outcome: OutcomeAllow, Reason: ReasonRegistered, CreatedAt: now}); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return Session{ID: req.SessionID, DeviceID: req.DeviceID, State: "active", Fence: fence, CreatedAt: now}, nil
}

func (s *Store) CloseSession(ctx context.Context, req CloseSessionRequest) (Session, error) {
	if err := s.ensureOpen(); err != nil {
		return Session{}, err
	}
	if err := validateID(req.SessionID, "session id"); err != nil {
		return Session{}, err
	}
	if req.DeviceID != "" {
		if err := validateID(req.DeviceID, "device id"); err != nil {
			return Session{}, err
		}
	}
	if err := validateActor(req.Actor); err != nil {
		return Session{}, err
	}
	if req.Reason == "" || !validSessionCloseReason(req.Reason) {
		return Session{}, fmt.Errorf("%w: invalid close reason", ErrInvalidInput)
	}
	requestID, err := validateRequestID(req.RequestID)
	if err != nil {
		return Session{}, err
	}
	now := s.currentTime()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var session Session
	var created, closed sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id,device_id,state,fence,reason,created_at,closed_at FROM sessions WHERE id=?`, req.SessionID).Scan(&session.ID, &session.DeviceID, &session.State, &session.Fence, &session.Reason, &created, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, err
	}
	if req.DeviceID != "" && req.DeviceID != session.DeviceID {
		return Session{}, ErrInvalidInput
	}
	if session.State == "closed" {
		session.CreatedAt = time.Unix(0, created.Int64).UTC()
		if closed.Valid {
			session.ClosedAt = time.Unix(0, closed.Int64).UTC()
		}
		return session, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state='closed',reason=?,closed_at=? WHERE id=? AND state='active'`, string(req.Reason), now.UnixNano(), req.SessionID); err != nil {
		return Session{}, err
	}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventSessionClosed, AggregateType: "session", AggregateID: req.SessionID, DeviceID: session.DeviceID, SessionID: req.SessionID, RequestID: requestID, Actor: req.Actor, Outcome: OutcomeAllow, Reason: ReasonCode(req.Reason), CreatedAt: now}); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return Session{ID: req.SessionID, DeviceID: session.DeviceID, State: "closed", Fence: session.Fence, Reason: req.Reason, CreatedAt: time.Unix(0, created.Int64).UTC(), ClosedAt: now}, nil
}

func (s *Store) RevokeDevice(ctx context.Context, req RevokeDeviceRequest) (RevokeDeviceResponse, error) {
	var result RevokeDeviceResponse
	if err := s.ensureOpen(); err != nil {
		return result, err
	}
	if err := validateID(req.DeviceID, "device id"); err != nil {
		return result, err
	}
	if err := validateActor(req.Actor); err != nil {
		return result, err
	}
	if req.Reason == "" || !validRevocationReason(req.Reason) {
		return result, fmt.Errorf("%w: invalid revocation reason", ErrInvalidInput)
	}
	requestID, err := validateRequestID(req.RequestID)
	if err != nil {
		return result, err
	}
	now := s.currentTime()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	var state, name, platform, architecture, certID string
	var fence, created, updated int64
	err = tx.QueryRowContext(ctx, `SELECT state,name,platform,architecture,fence,COALESCE(active_certificate_id,''),created_at,updated_at FROM devices WHERE id=?`, req.DeviceID).Scan(&state, &name, &platform, &architecture, &fence, &certID, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrDeviceNotFound
	}
	if err != nil {
		return result, err
	}
	if state == "revoked" {
		var rev Revocation
		var actorID, policyID, reason, revState string
		var revCreated int64
		if err := tx.QueryRowContext(ctx, `SELECT id,actor_id,policy_id,reason,state,created_at FROM revocations WHERE device_id=? ORDER BY created_at,id LIMIT 1`, req.DeviceID).Scan(&rev.ID, &actorID, &policyID, &reason, &revState, &revCreated); err != nil {
			return result, ErrAuthentication
		}
		rev.DeviceID, rev.Actor, rev.Reason, rev.State, rev.CreatedAt = req.DeviceID, Actor{ID: actorID, PolicyID: policyID}, RevocationReason(reason), revState, time.Unix(0, revCreated).UTC()
		if rev.Actor != req.Actor || rev.Reason != req.Reason {
			return result, ErrRevocationConflict
		}
		intents, readErr := readAllClosureIntents(ctx, tx, req.DeviceID, maxReadLimit)
		if readErr != nil {
			return result, readErr
		}
		if err := tx.Commit(); err != nil {
			return result, err
		}
		result.Device = Device{ID: req.DeviceID, Name: name, Platform: Platform(platform), Architecture: Architecture(architecture), State: "revoked", Fence: fence, ActiveCertificateID: certID, CreatedAt: time.Unix(0, created).UTC(), UpdatedAt: time.Unix(0, updated).UTC()}
		result.Revocation, result.Intents, result.Already = rev, intents, true
		return result, nil
	}
	if state != "active" {
		return result, ErrAuthentication
	}
	newFence := fence + 1
	if _, err := tx.ExecContext(ctx, `UPDATE devices SET state='revoked',fence=?,updated_at=? WHERE id=? AND state='active'`, newFence, now.UnixNano(), req.DeviceID); err != nil {
		return result, err
	}
	revocationID := "rev-" + digestHex([]byte("device-revocation"), []byte(req.DeviceID))[:32]
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventDeviceRevoked, AggregateType: "device", AggregateID: req.DeviceID, DeviceID: req.DeviceID, RequestID: requestID, Actor: req.Actor, Outcome: OutcomeAllow, Reason: ReasonCode(req.Reason), CreatedAt: now}); err != nil {
		return result, err
	}
	certificates, err := tx.QueryContext(ctx, `SELECT id FROM certificates WHERE device_id=? AND state='active' ORDER BY id`, req.DeviceID)
	if err != nil {
		return result, err
	}
	var activeCertificateIDs []string
	for certificates.Next() {
		var activeID string
		if err := certificates.Scan(&activeID); err != nil {
			_ = certificates.Close()
			return result, err
		}
		activeCertificateIDs = append(activeCertificateIDs, activeID)
	}
	if err := certificates.Err(); err != nil {
		_ = certificates.Close()
		return result, err
	}
	_ = certificates.Close()
	if certID != "" && len(activeCertificateIDs) == 0 {
		return result, ErrAuthentication
	}
	for _, activeID := range activeCertificateIDs {
		updatedCertificate, err := tx.ExecContext(ctx, `UPDATE certificates SET state='revoked' WHERE id=? AND state='active'`, activeID)
		if err != nil {
			return result, err
		}
		if n, _ := updatedCertificate.RowsAffected(); n != 1 {
			return result, ErrAuthentication
		}
		if err := insertAudit(ctx, tx, auditEvent{Kind: EventCertificateRevoked, AggregateType: "certificate", AggregateID: activeID, DeviceID: req.DeviceID, RequestID: requestID, Actor: req.Actor, Outcome: OutcomeAllow, Reason: ReasonCertificateRevoked, CreatedAt: now}); err != nil {
			return result, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO revocations(id,device_id,actor_id,policy_id,reason,created_at,state) VALUES (?,?,?,?,?,?,'applied')`, revocationID, req.DeviceID, req.Actor.ID, req.Actor.PolicyID, string(req.Reason), now.UnixNano()); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM sessions WHERE device_id=? AND state='active' ORDER BY id`, req.DeviceID)
	if err != nil {
		return result, err
	}
	var intents []ClosureIntent
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			_ = rows.Close()
			return result, err
		}
		intent := ClosureIntent{ID: "clo-" + digestHexMany([]byte("closure-intent"), [][]byte{[]byte(req.DeviceID), []byte(sessionID), []byte(fmt.Sprint(newFence))})[:32], DeviceID: req.DeviceID, SessionID: sessionID, RevocationID: revocationID, Fence: newFence, Reason: req.Reason, State: "pending", CreatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO closure_intents(id,device_id,session_id,revocation_id,fence,reason,state,created_at) VALUES (?,?,?,?,? ,?,'pending',?)`, intent.ID, intent.DeviceID, intent.SessionID, intent.RevocationID, intent.Fence, string(intent.Reason), intent.CreatedAt.UnixNano()); err != nil {
			_ = rows.Close()
			return result, err
		}
		intents = append(intents, intent)
		if err := insertAudit(ctx, tx, auditEvent{Kind: EventClosureRequested, AggregateType: "closure_intent", AggregateID: intent.ID, DeviceID: req.DeviceID, SessionID: sessionID, RequestID: requestID, Actor: req.Actor, Outcome: OutcomeAllow, Reason: ReasonClosureRequested, CreatedAt: now}); err != nil {
			_ = rows.Close()
			return result, err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return result, err
	}
	_ = rows.Close()
	if err := tx.Commit(); err != nil {
		return result, err
	}
	result.Device = Device{ID: req.DeviceID, Name: name, Platform: Platform(platform), Architecture: Architecture(architecture), State: "revoked", Fence: newFence, ActiveCertificateID: certID, CreatedAt: time.Unix(0, created).UTC(), UpdatedAt: now}
	result.Revocation = Revocation{ID: revocationID, DeviceID: req.DeviceID, Actor: req.Actor, Reason: req.Reason, State: "applied", CreatedAt: now}
	result.Intents = intents
	return result, nil
}

func (s *Store) PendingClosureIntents(ctx context.Context, filter ClosureIntentFilter) ([]ClosureIntent, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	limit, err := validatedLimit(filter.Limit)
	if err != nil {
		return nil, err
	}
	if filter.DeviceID != "" {
		if err := validateID(filter.DeviceID, "device id"); err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	intents, err := readClosureIntents(ctx, tx, filter.DeviceID, limit)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return intents, nil
}

func readClosureIntents(ctx context.Context, tx *sql.Tx, deviceID string, limit int) ([]ClosureIntent, error) {
	return readClosureIntentsState(ctx, tx, deviceID, limit, true)
}

func readAllClosureIntents(ctx context.Context, tx *sql.Tx, deviceID string, limit int) ([]ClosureIntent, error) {
	return readClosureIntentsState(ctx, tx, deviceID, limit, false)
}

func readClosureIntentsState(ctx context.Context, tx *sql.Tx, deviceID string, limit int, pendingOnly bool) ([]ClosureIntent, error) {
	var rows *sql.Rows
	var err error
	switch {
	case pendingOnly && deviceID != "":
		rows, err = tx.QueryContext(ctx, `SELECT id,device_id,session_id,revocation_id,fence,reason,state,created_at,applied_at FROM closure_intents WHERE state='pending' AND device_id=? ORDER BY created_at,id LIMIT ?`, deviceID, limit)
	case pendingOnly:
		rows, err = tx.QueryContext(ctx, `SELECT id,device_id,session_id,revocation_id,fence,reason,state,created_at,applied_at FROM closure_intents WHERE state='pending' ORDER BY created_at,id LIMIT ?`, limit)
	case deviceID != "":
		rows, err = tx.QueryContext(ctx, `SELECT id,device_id,session_id,revocation_id,fence,reason,state,created_at,applied_at FROM closure_intents WHERE device_id=? ORDER BY created_at,id LIMIT ?`, deviceID, limit)
	default:
		rows, err = tx.QueryContext(ctx, `SELECT id,device_id,session_id,revocation_id,fence,reason,state,created_at,applied_at FROM closure_intents ORDER BY created_at,id LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []ClosureIntent
	for rows.Next() {
		var intent ClosureIntent
		var reason string
		var created, applied sql.NullInt64
		if err := rows.Scan(&intent.ID, &intent.DeviceID, &intent.SessionID, &intent.RevocationID, &intent.Fence, &reason, &intent.State, &created, &applied); err != nil {
			return nil, err
		}
		intent.Reason = RevocationReason(reason)
		intent.CreatedAt = time.Unix(0, created.Int64).UTC()
		if applied.Valid {
			intent.AppliedAt = time.Unix(0, applied.Int64).UTC()
		}
		result = append(result, intent)
	}
	return result, rows.Err()
}

func (s *Store) Audit(ctx context.Context, filter AuditFilter) ([]AuditEvent, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	limit, err := validatedLimit(filter.Limit)
	if err != nil {
		return nil, err
	}
	if filter.DeviceID != "" {
		if err := validateID(filter.DeviceID, "device id"); err != nil {
			return nil, err
		}
	}
	if filter.AggregateID != "" {
		if err := validateID(filter.AggregateID, "aggregate id"); err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	baseQuery := `SELECT sequence,id,event_kind,aggregate_type,aggregate_id,COALESCE(device_id,''),COALESCE(session_id,''),COALESCE(enrollment_id,''),request_id,COALESCE(actor_id,''),COALESCE(policy_id,''),outcome,reason,digest_ref,created_at FROM audit_events`
	var rows *sql.Rows
	switch {
	case filter.DeviceID != "" && filter.AggregateID != "":
		rows, err = tx.QueryContext(ctx, baseQuery+` WHERE device_id=? AND aggregate_id=? ORDER BY sequence LIMIT ?`, filter.DeviceID, filter.AggregateID, limit)
	case filter.DeviceID != "":
		rows, err = tx.QueryContext(ctx, baseQuery+` WHERE device_id=? ORDER BY sequence LIMIT ?`, filter.DeviceID, limit)
	case filter.AggregateID != "":
		rows, err = tx.QueryContext(ctx, baseQuery+` WHERE aggregate_id=? ORDER BY sequence LIMIT ?`, filter.AggregateID, limit)
	default:
		rows, err = tx.QueryContext(ctx, baseQuery+` ORDER BY sequence LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []AuditEvent
	for rows.Next() {
		var event AuditEvent
		var actorID, policyID, reason, digestRef string
		var created int64
		if err := rows.Scan(&event.Sequence, &event.ID, &event.EventKind, &event.AggregateType, &event.AggregateID, &event.DeviceID, &event.SessionID, &event.EnrollmentID, &event.RequestID, &actorID, &policyID, &event.Outcome, &reason, &digestRef, &created); err != nil {
			return nil, err
		}
		event.Actor, event.Reason, event.DigestRef, event.CreatedAt = Actor{ID: actorID, PolicyID: policyID}, ReasonCode(reason), digestRef, time.Unix(0, created).UTC()
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

type auditEvent struct {
	Kind                                                                     EventKind
	AggregateType, AggregateID, DeviceID, SessionID, EnrollmentID, RequestID string
	Actor                                                                    Actor
	Outcome                                                                  Outcome
	Reason                                                                   ReasonCode
	DigestRef                                                                string
	CreatedAt                                                                time.Time
}

func insertAudit(ctx context.Context, tx *sql.Tx, event auditEvent) error {
	if !validReason(event.Reason) || !validEventKind(event.Kind) || !validOutcome(event.Outcome) {
		return fmt.Errorf("%w: invalid audit reason", ErrInvalidInput)
	}
	if err := validateActor(event.Actor); err != nil {
		return err
	}
	id := "aud-" + digestHexMany([]byte("audit-event"), [][]byte{[]byte(event.Kind), []byte(event.AggregateID), []byte(event.RequestID), []byte(event.CreatedAt.Format(time.RFC3339Nano))})[:32]
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,event_kind,aggregate_type,aggregate_id,device_id,session_id,enrollment_id,request_id,actor_id,policy_id,outcome,reason,digest_ref,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, string(event.Kind), event.AggregateType, event.AggregateID, nullString(event.DeviceID), nullString(event.SessionID), nullString(event.EnrollmentID), event.RequestID, event.Actor.ID, event.Actor.PolicyID, string(event.Outcome), string(event.Reason), event.DigestRef, event.CreatedAt.UnixNano())
	return err
}

func validateActor(actor Actor) error {
	if err := validateID(actor.ID, "actor id"); err != nil {
		return err
	}
	if err := validateID(actor.PolicyID, "policy id"); err != nil {
		return err
	}
	return nil
}

func validReason(reason ReasonCode) bool {
	switch reason {
	case ReasonCreated, ReasonIssued, ReasonExpired, ReasonFailed, ReasonBindingMismatch, ReasonProofInvalid, ReasonProofVerified, ReasonEnrollmentCompleted, ReasonEnrollmentConsumed, ReasonCertificateIssued, ReasonCertificateRenewed, ReasonCertificateRevoked, ReasonClosureRequested, ReasonAdministrator, ReasonCertificate, ReasonPolicy, ReasonUnknownState, ReasonHeartbeatTimeout, ReasonProtocolError, ReasonRegistered, ReasonRevoked:
		return true
	default:
		return false
	}
}

func validRevocationReason(reason RevocationReason) bool {
	switch reason {
	case RevocationReasonAdministrator, RevocationReasonCertificate, RevocationReasonPolicy, RevocationReasonUnknownState:
		return true
	default:
		return false
	}
}

func validSessionCloseReason(reason SessionCloseReason) bool {
	switch reason {
	case SessionCloseReasonRevoked, SessionCloseReasonHeartbeatTimeout, SessionCloseReasonAdministrator, SessionCloseReasonProtocolError:
		return true
	default:
		return false
	}
}

func validEventKind(kind EventKind) bool {
	switch kind {
	case EventEnrollmentCreated, EventEnrollmentFailed, EventEnrollmentExpired, EventEnrollmentConsumed,
		EventChallengeIssued, EventChallengeFailed, EventChallengeExpired, EventChallengeConsumed,
		EventCertificateIssued, EventCertificateRenewed, EventCertificateRevoked,
		EventDeviceActivated, EventDeviceRevoked, EventSessionRegistered, EventSessionClosed,
		EventClosureRequested, EventClosureApplied:
		return true
	default:
		return false
	}
}

func validOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeAllow, OutcomeDeny, OutcomeError:
		return true
	default:
		return false
	}
}

func validatedLimit(limit int) (int, error) {
	if limit == 0 {
		return 100, nil
	}
	if limit < 1 || limit > maxReadLimit {
		return 0, fmt.Errorf("%w: read limit must be between 1 and %d", ErrInvalidInput, maxReadLimit)
	}
	return limit, nil
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func digestBytes(value string) []byte {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil
	}
	return decoded
}

func subtleCompare(left, right []byte) int {
	if len(left) != len(right) {
		return 0
	}
	return subtle.ConstantTimeCompare(left, right)
}

func subtleString(left, right string) int { return subtleCompare([]byte(left), []byte(right)) }
