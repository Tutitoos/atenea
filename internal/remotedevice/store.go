// Package remotedevice contains the durable identity and enrollment registry.
// Secret presentations are accepted only at the API boundary and are never
// written to SQLite.
//
//nolint:revive // This package exposes the explicit registry contract as a cohesive internal API.
package remotedevice

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	ProtocolVersion      = "1.1.0"
	defaultEnrollmentTTL = 10 * time.Minute
	maxEnrollmentTTL     = 15 * time.Minute
	maxChallengeTTL      = 2 * time.Minute
	maxCertificateTTL    = 365 * 24 * time.Hour
	maxSPKIBytes         = 1024
	maxName              = 256
	maxRequestID         = 128
	maxReadLimit         = 1000
)

type EnrollmentState string

const (
	EnrollmentPending    EnrollmentState = "pending"
	EnrollmentChallenged EnrollmentState = "challenged"
	EnrollmentExpired    EnrollmentState = "expired"
	EnrollmentFailed     EnrollmentState = "failed"
	EnrollmentActive     EnrollmentState = "active"
)

type ChallengeState string

const (
	ChallengePending  ChallengeState = "pending"
	ChallengeConsumed ChallengeState = "consumed"
	ChallengeFailed   ChallengeState = "failed"
	ChallengeExpired  ChallengeState = "expired"
)

type Platform string

const (
	PlatformWindows Platform = "windows"
	PlatformMacOS   Platform = "macos"
	PlatformLinux   Platform = "linux"
)

type Architecture string

const (
	ArchitectureX8664 Architecture = "x86_64"
	ArchitectureArm64 Architecture = "arm64"
	ArchitectureArmv7 Architecture = "armv7"
)

// ReasonCode is intentionally closed. New reasons require a schema and audit
// contract update rather than accepting arbitrary operator text.
type ReasonCode string

const (
	ReasonCreated             ReasonCode = "created"
	ReasonIssued              ReasonCode = "issued"
	ReasonExpired             ReasonCode = "expired"
	ReasonFailed              ReasonCode = "failed"
	ReasonBindingMismatch     ReasonCode = "binding_mismatch"
	ReasonProofInvalid        ReasonCode = "proof_invalid"
	ReasonProofVerified       ReasonCode = "proof_verified"
	ReasonEnrollmentCompleted ReasonCode = "enrollment_completed"
	ReasonEnrollmentConsumed  ReasonCode = "enrollment_consumed"
	ReasonCertificateIssued   ReasonCode = "certificate_issued"
	ReasonCertificateRenewed  ReasonCode = "certificate_renewed"
	ReasonCertificateRevoked  ReasonCode = "certificate_revoked"
	ReasonClosureRequested    ReasonCode = "closure_requested"
	ReasonAdministrator       ReasonCode = "administrator"
	ReasonCertificate         ReasonCode = "certificate"
	ReasonPolicy              ReasonCode = "policy"
	ReasonUnknownState        ReasonCode = "unknown_state"
	ReasonHeartbeatTimeout    ReasonCode = "heartbeat_timeout"
	ReasonProtocolError       ReasonCode = "protocol_error"
	ReasonRegistered          ReasonCode = "registered"
	ReasonRevoked             ReasonCode = "revoked"
)

// RevocationReason is the closed reason vocabulary for device revocation.
type RevocationReason string

const (
	RevocationReasonAdministrator RevocationReason = "administrator"
	RevocationReasonCertificate   RevocationReason = "certificate"
	RevocationReasonPolicy        RevocationReason = "policy"
	RevocationReasonUnknownState  RevocationReason = "unknown_state"
)

// SessionCloseReason is the closed reason vocabulary for session closure.
type SessionCloseReason string

const (
	SessionCloseReasonRevoked          SessionCloseReason = "revoked"
	SessionCloseReasonHeartbeatTimeout SessionCloseReason = "heartbeat_timeout"
	SessionCloseReasonAdministrator    SessionCloseReason = "administrator"
	SessionCloseReasonProtocolError    SessionCloseReason = "protocol_error"
)

type EventKind string

const (
	EventEnrollmentCreated  EventKind = "enrollment.created"
	EventEnrollmentFailed   EventKind = "enrollment.failed"
	EventEnrollmentExpired  EventKind = "enrollment.expired"
	EventEnrollmentConsumed EventKind = "enrollment.consumed"
	EventChallengeIssued    EventKind = "challenge.issued"
	EventChallengeFailed    EventKind = "challenge.failed"
	EventChallengeExpired   EventKind = "challenge.expired"
	EventChallengeConsumed  EventKind = "challenge.consumed"
	EventCertificateIssued  EventKind = "certificate.issued"
	EventCertificateRenewed EventKind = "certificate.renewed"
	EventCertificateRevoked EventKind = "certificate.revoked"
	EventDeviceActivated    EventKind = "device.activated"
	EventDeviceRevoked      EventKind = "device.revoked"
	EventSessionRegistered  EventKind = "session.registered"
	EventSessionClosed      EventKind = "session.closed"
	EventClosureRequested   EventKind = "closure.requested"
	EventClosureApplied     EventKind = "closure.applied"
)

type Outcome string

const (
	OutcomeAllow Outcome = "allow"
	OutcomeDeny  Outcome = "deny"
	OutcomeError Outcome = "error"
)

var (
	ErrInvalidInput        = errors.New("remote device: invalid input")
	ErrInvalidToken        = errors.New("remote device: invalid enrollment token")
	ErrEnrollmentNotFound  = errors.New("remote device: enrollment not found")
	ErrEnrollmentUsed      = errors.New("remote device: enrollment token already used")
	ErrEnrollmentExpired   = errors.New("remote device: enrollment expired")
	ErrBindingMismatch     = errors.New("remote device: enrollment binding mismatch")
	ErrProofInvalid        = errors.New("remote device: invalid enrollment proof")
	ErrIssuerUnavailable   = errors.New("remote device: certificate issuer unavailable")
	ErrCertificateInvalid  = errors.New("remote device: invalid certificate metadata")
	ErrDeviceNotFound      = errors.New("remote device: device not found")
	ErrDeviceRevoked       = errors.New("remote device: device revoked")
	ErrAuthentication      = errors.New("remote device: authentication denied")
	ErrSessionNotFound     = errors.New("remote device: session not found")
	ErrRevocationConflict  = errors.New("remote device: incompatible device revocation")
	ErrStoreClosed         = errors.New("remote device: store closed")
	ErrChallengeNotFound   = errors.New("remote device: challenge not found")
	ErrChallengeUsed       = errors.New("remote device: challenge already used")
	ErrCertificateNotFound = errors.New("remote device: certificate not found")
	ErrRenewalConflict     = errors.New("remote device: incompatible certificate renewal")
)

type Actor struct {
	ID       string `json:"id"`
	PolicyID string `json:"policy_id"`
}

// CertificateIssueRequest contains only bounded public material and metadata.
// The issuer must not persist or return certificate bytes through this API.
type CertificateIssueRequest struct {
	ChallengeID     string
	EnrollmentID    string
	DeviceID        string
	Name            string
	Platform        Platform
	Architecture    Architecture
	PublicKeySPKI   []byte
	PublicKeyDigest []byte
	ContextDigest   []byte
	Actor           Actor
}

type CertificateMetadata struct {
	ID              string
	IssuerID        string
	Serial          string
	Fingerprint     string
	PublicKeyDigest []byte
	State           string
	NotBefore       time.Time
	NotAfter        time.Time
	CreatedAt       time.Time
}

type CertificateIssuer interface {
	Issue(context.Context, CertificateIssueRequest) (CertificateMetadata, error)
}

type openConfig struct {
	clock             func() time.Time
	random            io.Reader
	certificateIssuer CertificateIssuer
}

type OpenOption func(*openConfig)

func WithClock(clock func() time.Time) OpenOption {
	return func(config *openConfig) { config.clock = clock }
}

func WithRandomReader(reader io.Reader) OpenOption {
	return func(config *openConfig) { config.random = reader }
}

func WithCertificateIssuer(issuer CertificateIssuer) OpenOption {
	return func(config *openConfig) { config.certificateIssuer = issuer }
}

type Enrollment struct {
	ID           string          `json:"id"`
	DeviceID     string          `json:"device_id"`
	Name         string          `json:"name"`
	Platform     Platform        `json:"platform"`
	Architecture Architecture    `json:"architecture"`
	State        EnrollmentState `json:"state"`
	Actor        Actor           `json:"actor"`
	ExpiresAt    time.Time       `json:"expires_at"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type CreateEnrollmentRequest struct {
	ID           string
	DeviceID     string
	Name         string
	Platform     Platform
	Architecture Architecture
	Actor        Actor
	ExpiresAt    time.Time
	TTL          time.Duration
	RequestID    string
}

type CreateEnrollmentResponse struct {
	Enrollment
	Token string `json:"token"`
}

type IssueChallengeRequest struct {
	EnrollmentID   string
	DeviceID       string
	Name           string
	Platform       Platform
	Architecture   Architecture
	Token          string
	PublicKeySPKI  []byte
	Context        []byte
	IdempotencyKey string
	RequestID      string
}

type Challenge struct {
	ID                    string         `json:"id"`
	EnrollmentID          string         `json:"enrollment_id"`
	DeviceID              string         `json:"device_id"`
	Name                  string         `json:"name"`
	Platform              Platform       `json:"platform"`
	Architecture          Architecture   `json:"architecture"`
	State                 ChallengeState `json:"state"`
	Actor                 Actor          `json:"actor"`
	IssuedAt              time.Time      `json:"issued_at"`
	ExpiresAt             time.Time      `json:"expires_at"`
	Nonce                 []byte         `json:"-"`
	NonceDigest           []byte         `json:"-"`
	ContextDigest         []byte         `json:"-"`
	OriginalContextDigest []byte         `json:"-"`
	PublicKeySPKI         []byte         `json:"-"`
	PublicKeyDigest       []byte         `json:"-"`
	AlreadyIssued         bool           `json:"already_issued,omitempty"`
}

type Store struct {
	db                *sql.DB
	path              string
	now               func() time.Time
	random            io.Reader
	certificateIssuer CertificateIssuer
	closeMu           sync.Mutex
	closed            bool
}

// Open opens a dedicated registry database. Options are deliberately typed;
// callers cannot inject arbitrary values or dynamic compatibility parsers.
func Open(ctx context.Context, path string, options ...OpenOption) (*Store, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config := openConfig{clock: time.Now, random: rand.Reader}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("%w: nil open option", ErrInvalidInput)
		}
		option(&config)
	}
	if config.clock == nil || config.random == nil {
		return nil, fmt.Errorf("%w: clock and random reader are required", ErrInvalidInput)
	}
	resolved, err := securePath(path)
	if err != nil {
		return nil, err
	}
	if err := secureDatabaseFiles(resolved); err != nil {
		return nil, err
	}
	dsn, err := sqliteDSN(resolved)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("remote device: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("remote device: %s: %w", pragma, err)
		}
	}
	// SQLite may create WAL sidecars while opening. Harden only these files,
	// never an existing parent directory, before returning the store.
	if err := secureDatabaseFiles(resolved); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &Store{db: db, path: resolved, now: config.clock, random: config.random, certificateIssuer: config.certificateIssuer}
	if err := store.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

func (s *Store) ensureOpen() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	return nil
}

func (s *Store) currentTime() time.Time { return s.now().UTC() }

func (s *Store) CreateEnrollment(ctx context.Context, req CreateEnrollmentRequest) (CreateEnrollmentResponse, error) {
	var response CreateEnrollmentResponse
	if err := s.ensureOpen(); err != nil {
		return response, err
	}
	if err := ctx.Err(); err != nil {
		return response, err
	}
	if err := validateID(req.DeviceID, "device id"); err != nil {
		return response, err
	}
	if req.Name == "" || len(req.Name) > maxName {
		return response, fmt.Errorf("%w: name is required and bounded", ErrInvalidInput)
	}
	if err := validatePlatformArchitecture(req.Platform, req.Architecture); err != nil {
		return response, err
	}
	if err := validateActor(req.Actor); err != nil {
		return response, err
	}
	if req.ID == "" {
		req.ID = "enr-" + randomIdentifier(req.DeviceID, s.currentTime())
	}
	if err := validateID(req.ID, "enrollment id"); err != nil {
		return response, err
	}
	requestID, err := validateRequestID(req.RequestID)
	if err != nil {
		return response, err
	}
	now := s.currentTime()
	expires, err := enrollmentExpiry(now, req.ExpiresAt, req.TTL)
	if err != nil {
		return response, err
	}
	tokenRaw := make([]byte, 32)
	if _, err := io.ReadFull(s.random, tokenRaw); err != nil {
		return response, fmt.Errorf("remote device: generate enrollment token: %w", err)
	}
	tokenDigest := digestHex([]byte("enrollment-token"), tokenRaw)
	response = CreateEnrollmentResponse{Enrollment: Enrollment{ID: req.ID, DeviceID: req.DeviceID, Name: req.Name, Platform: req.Platform, Architecture: req.Architecture, State: EnrollmentPending, Actor: req.Actor, ExpiresAt: expires, CreatedAt: now, UpdatedAt: now}, Token: base64.RawURLEncoding.EncodeToString(tokenRaw)}
	defer wipeBytes(tokenRaw)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return CreateEnrollmentResponse{}, fmt.Errorf("remote device: begin enrollment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO enrollments(id,device_id,name,platform,architecture,state,token_digest,expires_at,created_at,updated_at,actor_id,policy_id) VALUES (?,?,?,?,?,'pending',?,?,?,?,?,?)`, req.ID, req.DeviceID, req.Name, string(req.Platform), string(req.Architecture), tokenDigest, expires.UnixNano(), now.UnixNano(), now.UnixNano(), req.Actor.ID, req.Actor.PolicyID)
	if err != nil {
		return CreateEnrollmentResponse{}, fmt.Errorf("remote device: create enrollment: %w", err)
	}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventEnrollmentCreated, AggregateType: "enrollment", AggregateID: req.ID, EnrollmentID: req.ID, RequestID: requestID, Actor: req.Actor, Outcome: OutcomeAllow, Reason: ReasonCreated, CreatedAt: now}); err != nil {
		return CreateEnrollmentResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreateEnrollmentResponse{}, fmt.Errorf("remote device: commit enrollment: %w", err)
	}
	return response, nil
}

func (s *Store) IssueChallenge(ctx context.Context, req IssueChallengeRequest) (Challenge, error) {
	var response Challenge
	if err := s.ensureOpen(); err != nil {
		return response, err
	}
	if err := ctx.Err(); err != nil {
		return response, err
	}
	if err := validateID(req.EnrollmentID, "enrollment id"); err != nil {
		return response, err
	}
	if err := validateID(req.DeviceID, "device id"); err != nil {
		return response, err
	}
	if req.Name == "" || len(req.Name) > maxName {
		return response, fmt.Errorf("%w: name is required and bounded", ErrInvalidInput)
	}
	if err := validatePlatformArchitecture(req.Platform, req.Architecture); err != nil {
		return response, err
	}
	tokenRaw, err := decodeToken(req.Token)
	if err != nil {
		return response, err
	}
	defer wipeBytes(tokenRaw)
	if len(req.PublicKeySPKI) == 0 || len(req.PublicKeySPKI) > maxSPKIBytes {
		return response, fmt.Errorf("%w: public key SPKI must be between 1 and %d bytes", ErrInvalidInput, maxSPKIBytes)
	}
	canonicalSPKI, publicKey, err := parsePublicKey(req.PublicKeySPKI)
	if err != nil {
		return response, err
	}
	_ = publicKey
	publicDigest := sha256.Sum256(canonicalSPKI)
	publicDigestBytes := append([]byte(nil), publicDigest[:]...)
	requestID, err := validateRequestID(req.RequestID)
	if err != nil {
		return response, err
	}
	issueKey := strings.TrimSpace(req.IdempotencyKey)
	if issueKey == "" {
		issueKey = requestID
	}
	if issueKey == "" {
		issueKey = "default"
	}
	if _, err := validateRequestID(issueKey); err != nil {
		return response, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return response, fmt.Errorf("remote device: begin challenge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.currentTime()
	var row enrollmentRow
	err = tx.QueryRowContext(ctx, `SELECT id,device_id,name,platform,architecture,state,token_digest,expires_at,created_at,updated_at,issue_key,COALESCE(challenge_id,''),actor_id,policy_id FROM enrollments WHERE id=?`, req.EnrollmentID).Scan(&row.id, &row.deviceID, &row.name, &row.platform, &row.architecture, &row.state, &row.tokenDigest, &row.expiresNS, &row.createdNS, &row.updatedNS, &row.issueKey, &row.challengeID, &row.actorID, &row.policyID)
	if errors.Is(err, sql.ErrNoRows) {
		return response, ErrEnrollmentNotFound
	}
	if err != nil {
		return response, fmt.Errorf("remote device: read enrollment: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(row.tokenDigest), []byte(digestHex([]byte("enrollment-token"), tokenRaw))) != 1 {
		return response, ErrInvalidToken
	}
	if row.state == string(EnrollmentExpired) || now.UnixNano() >= row.expiresNS {
		if row.state == string(EnrollmentPending) {
			if err := expireEnrollment(ctx, tx, row, now, requestID); err != nil {
				return response, err
			}
			if err := tx.Commit(); err != nil {
				return response, fmt.Errorf("remote device: commit enrollment expiry: %w", err)
			}
		}
		return response, ErrEnrollmentExpired
	}
	if req.DeviceID != row.deviceID || string(req.Platform) != row.platform || string(req.Architecture) != row.architecture || req.Name != row.name {
		updated, updateErr := tx.ExecContext(ctx, `UPDATE enrollments SET state='failed',updated_at=? WHERE id=? AND state='pending'`, now.UnixNano(), row.id)
		if updateErr != nil {
			return response, updateErr
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			return response, ErrEnrollmentUsed
		}
		if auditErr := insertAudit(ctx, tx, auditEvent{Kind: EventEnrollmentFailed, AggregateType: "enrollment", AggregateID: row.id, DeviceID: row.deviceID, EnrollmentID: row.id, RequestID: requestID, Actor: Actor{ID: row.actorID, PolicyID: row.policyID}, Outcome: OutcomeDeny, Reason: ReasonBindingMismatch, CreatedAt: now}); auditErr != nil {
			return response, auditErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return response, fmt.Errorf("remote device: commit binding failure: %w", commitErr)
		}
		return response, ErrBindingMismatch
	}
	if row.state != string(EnrollmentPending) {
		if row.issueKey == issueKey && row.challengeID != "" {
			prior, readErr := readChallenge(ctx, tx, row.challengeID)
			if readErr != nil {
				return response, readErr
			}
			requestedContextDigest := sha256.Sum256(req.Context)
			if subtle.ConstantTimeCompare(prior.PublicKeyDigest, publicDigestBytes) != 1 || subtle.ConstantTimeCompare(prior.OriginalContextDigest, requestedContextDigest[:]) != 1 {
				return response, ErrBindingMismatch
			}
			if err := tx.Commit(); err != nil {
				return response, fmt.Errorf("remote device: commit challenge replay: %w", err)
			}
			prior.AlreadyIssued = true
			return prior, ErrEnrollmentUsed
		}
		return response, ErrEnrollmentUsed
	}
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(s.random, nonce); err != nil {
		return response, fmt.Errorf("remote device: generate challenge nonce: %w", err)
	}
	defer wipeBytes(nonce)
	nonceDigest := Digest("challenge-nonce", nonce)
	contextDigest := sha256.Sum256(req.Context)
	challengeID := "chl-" + digestHexMany([]byte("challenge-id"), [][]byte{[]byte(row.id), []byte(issueKey)})[:32]
	expiresNS := now.Add(maxChallengeTTL).UnixNano()
	if row.expiresNS < expiresNS {
		expiresNS = row.expiresNS
	}
	message := canonicalChallengeMessage(challengeID, row.id, row.deviceID, row.name, Platform(row.platform), Architecture(row.architecture), row.expiresNS, now.UnixNano(), expiresNS, nonceDigest[:], contextDigest[:], canonicalSPKI, publicDigestBytes)
	messageDigest := sha256.Sum256(message)
	response = Challenge{ID: challengeID, EnrollmentID: row.id, DeviceID: row.deviceID, Name: row.name, Platform: Platform(row.platform), Architecture: Architecture(row.architecture), State: ChallengePending, Actor: Actor{ID: row.actorID, PolicyID: row.policyID}, IssuedAt: now, ExpiresAt: time.Unix(0, expiresNS).UTC(), Nonce: append([]byte(nil), nonce...), NonceDigest: append([]byte(nil), nonceDigest[:]...), ContextDigest: append([]byte(nil), messageDigest[:]...), OriginalContextDigest: append([]byte(nil), contextDigest[:]...), PublicKeySPKI: append([]byte(nil), canonicalSPKI...), PublicKeyDigest: append([]byte(nil), publicDigestBytes...)}
	_, err = tx.ExecContext(ctx, `UPDATE enrollments SET state='challenged',issue_key=?,challenge_id=?,updated_at=? WHERE id=? AND state='pending'`, issueKey, challengeID, now.UnixNano(), row.id)
	if err != nil {
		return Challenge{}, fmt.Errorf("remote device: consume enrollment: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO challenges(id,enrollment_id,device_id,name,platform,architecture,state,nonce_digest,context_digest,original_context_digest,public_key_spki,public_key_digest,actor_id,policy_id,issued_at,expires_at) VALUES (?,?,?,?,?,?, 'pending',?,?,?,?,?,?,?, ?,?)`, challengeID, row.id, row.deviceID, row.name, row.platform, row.architecture, hex.EncodeToString(nonceDigest[:]), hex.EncodeToString(messageDigest[:]), hex.EncodeToString(contextDigest[:]), canonicalSPKI, hex.EncodeToString(publicDigestBytes), row.actorID, row.policyID, now.UnixNano(), expiresNS)
	if err != nil {
		return Challenge{}, fmt.Errorf("remote device: create challenge: %w", err)
	}
	if err := insertAudit(ctx, tx, auditEvent{Kind: EventChallengeIssued, AggregateType: "challenge", AggregateID: challengeID, DeviceID: row.deviceID, EnrollmentID: row.id, RequestID: requestID, Actor: Actor{ID: row.actorID, PolicyID: row.policyID}, Outcome: OutcomeAllow, Reason: ReasonIssued, DigestRef: hex.EncodeToString(messageDigest[:]), CreatedAt: now}); err != nil {
		return Challenge{}, err
	}
	if err := tx.Commit(); err != nil {
		return Challenge{}, fmt.Errorf("remote device: commit challenge: %w", err)
	}
	return response, nil
}

type enrollmentRow struct {
	id, deviceID, name, platform, architecture, state string
	tokenDigest                                       string
	expiresNS, createdNS, updatedNS                   int64
	issueKey, challengeID                             string
	actorID, policyID                                 string
}

type challengeRow struct {
	id, enrollmentID, deviceID, name, platform, architecture, state string
	nonceDigest, contextDigest, originalContextDigest, publicDigest string
	publicSPKI                                                      []byte
	issuedNS, expiresNS                                             int64
	actorID, policyID                                               string
}

func readChallenge(ctx context.Context, tx *sql.Tx, id string) (Challenge, error) {
	var row challengeRow
	if err := tx.QueryRowContext(ctx, `SELECT id,enrollment_id,device_id,name,platform,architecture,state,nonce_digest,context_digest,original_context_digest,public_key_spki,public_key_digest,actor_id,policy_id,issued_at,expires_at FROM challenges WHERE id=?`, id).Scan(&row.id, &row.enrollmentID, &row.deviceID, &row.name, &row.platform, &row.architecture, &row.state, &row.nonceDigest, &row.contextDigest, &row.originalContextDigest, &row.publicSPKI, &row.publicDigest, &row.actorID, &row.policyID, &row.issuedNS, &row.expiresNS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Challenge{}, ErrChallengeNotFound
		}
		return Challenge{}, fmt.Errorf("remote device: read challenge: %w", err)
	}
	nonceDigest, err := hex.DecodeString(row.nonceDigest)
	if err != nil {
		return Challenge{}, ErrChallengeNotFound
	}
	contextDigest, err := hex.DecodeString(row.contextDigest)
	if err != nil {
		return Challenge{}, ErrChallengeNotFound
	}
	publicDigest, err := hex.DecodeString(row.publicDigest)
	if err != nil {
		return Challenge{}, ErrChallengeNotFound
	}
	originalContextDigest, err := hex.DecodeString(row.originalContextDigest)
	if err != nil {
		return Challenge{}, ErrChallengeNotFound
	}
	return Challenge{ID: row.id, EnrollmentID: row.enrollmentID, DeviceID: row.deviceID, Name: row.name, Platform: Platform(row.platform), Architecture: Architecture(row.architecture), State: ChallengeState(row.state), Actor: Actor{ID: row.actorID, PolicyID: row.policyID}, IssuedAt: time.Unix(0, row.issuedNS).UTC(), ExpiresAt: time.Unix(0, row.expiresNS).UTC(), NonceDigest: nonceDigest, ContextDigest: contextDigest, OriginalContextDigest: originalContextDigest, PublicKeySPKI: append([]byte(nil), row.publicSPKI...), PublicKeyDigest: publicDigest}, nil
}

func expireEnrollment(ctx context.Context, tx *sql.Tx, row enrollmentRow, now time.Time, requestID string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE enrollments SET state='expired',updated_at=? WHERE id=? AND state='pending'`, now.UnixNano(), row.id); err != nil {
		return fmt.Errorf("remote device: expire enrollment: %w", err)
	}
	return insertAudit(ctx, tx, auditEvent{Kind: EventEnrollmentExpired, AggregateType: "enrollment", AggregateID: row.id, DeviceID: row.deviceID, EnrollmentID: row.id, RequestID: requestID, Actor: Actor{ID: row.actorID, PolicyID: row.policyID}, Outcome: OutcomeDeny, Reason: ReasonExpired, CreatedAt: now})
}

func validatePlatformArchitecture(platform Platform, architecture Architecture) error {
	switch platform {
	case PlatformWindows, PlatformMacOS, PlatformLinux:
	default:
		return fmt.Errorf("%w: unsupported platform", ErrInvalidInput)
	}
	switch architecture {
	case ArchitectureX8664, ArchitectureArm64, ArchitectureArmv7:
	default:
		return fmt.Errorf("%w: unsupported architecture", ErrInvalidInput)
	}
	return nil
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func validateID(value, label string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%w: invalid %s", ErrInvalidInput, label)
	}
	return nil
}

func validateRequestID(value string) (string, error) {
	if len(value) > maxRequestID {
		return "", fmt.Errorf("%w: request id too long", ErrInvalidInput)
	}
	if value != "" {
		if err := validateID(value, "request id"); err != nil {
			return "", err
		}
	}
	return value, nil
}

func enrollmentExpiry(now, explicit time.Time, ttl time.Duration) (time.Time, error) {
	if !explicit.IsZero() && ttl != 0 {
		return time.Time{}, fmt.Errorf("%w: ExpiresAt and TTL are mutually exclusive", ErrInvalidInput)
	}
	if explicit.IsZero() {
		if ttl == 0 {
			ttl = defaultEnrollmentTTL
		}
		if ttl <= 0 || ttl > maxEnrollmentTTL {
			return time.Time{}, fmt.Errorf("%w: enrollment TTL must be between 1s and %s", ErrInvalidInput, maxEnrollmentTTL)
		}
		explicit = now.Add(ttl)
	} else {
		explicit = explicit.UTC()
		if !explicit.After(now) || explicit.After(now.Add(maxEnrollmentTTL)) {
			return time.Time{}, fmt.Errorf("%w: enrollment expiry exceeds %s", ErrInvalidInput, maxEnrollmentTTL)
		}
	}
	return explicit.UTC(), nil
}

func decodeToken(value string) ([]byte, error) {
	if len(value) != 43 {
		return nil, ErrInvalidToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != value {
		return nil, ErrInvalidToken
	}
	return raw, nil
}

func randomIdentifier(deviceID string, now time.Time) string {
	hash := sha256.Sum256([]byte(deviceID + "\x00" + now.Format(time.RFC3339Nano)))
	return hex.EncodeToString(hash[:])[:32]
}

func digestHex(domain, value []byte) string {
	return digestHexMany(domain, [][]byte{value})
}

func digestHexMany(domain []byte, values [][]byte) string {
	digest := canonicalDigest(string(domain), values)
	return hex.EncodeToString(digest[:])
}

func canonicalChallengeMessage(challengeID, enrollmentID, deviceID, name string, platform Platform, architecture Architecture, enrollmentExpiryNS, issuedNS, challengeExpiryNS int64, nonceDigest, contextDigest, spki, publicDigest []byte) []byte {
	fields := [][]byte{[]byte(ProtocolVersion), []byte(challengeID), []byte(enrollmentID), []byte(deviceID), []byte(name), []byte(platform), []byte(architecture), []byte(fmt.Sprint(enrollmentExpiryNS)), []byte(fmt.Sprint(issuedNS)), []byte(fmt.Sprint(challengeExpiryNS)), nonceDigest, contextDigest, spki, publicDigest}
	var message []byte
	message = append(message, []byte("atenea.remote.enrollment-proof.v1")...)
	message = append(message, 0)
	for _, field := range fields {
		length := uint32(len(field))
		message = append(message, byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
		message = append(message, field...)
	}
	return message
}

func wipeBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func securePath(path string) (string, error) {
	if path == ":memory:" {
		return path, nil
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%w: database path is required", ErrInvalidInput)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("remote device: resolve database path: %w", err)
	}
	abs = filepath.Clean(abs)
	if err := rejectSymlinkComponents(abs); err != nil {
		return "", err
	}
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("remote device: create dedicated database directory: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("remote device: inspect database directory: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%w: dedicated database directory must be private", ErrInvalidInput)
	}
	if info, err := os.Lstat(abs); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("%w: database path must be a regular file", ErrInvalidInput)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("remote device: inspect database path: %w", err)
	}
	return abs, nil
}

func rejectSymlinkComponents(abs string) error {
	volume := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, volume)
	current := volume + string(filepath.Separator)
	for _, component := range strings.Split(strings.Trim(rest, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return fmt.Errorf("remote device: inspect path component: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// macOS exposes the system temporary directory through /var ->
			// /private/var. It is not the caller-controlled final directory.
			if current == string(filepath.Separator)+"var" {
				continue
			}
			return fmt.Errorf("%w: path component is a symlink", ErrInvalidInput)
		}
	}
	return nil
}

func sqliteDSN(path string) (string, error) {
	if path == ":memory:" {
		return path, nil
	}
	u := &url.URL{Scheme: "file", Path: path}
	u.RawQuery = "_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)&_pragma=journal_mode(WAL)"
	return u.String(), nil
}

func secureDatabaseFiles(path string) error {
	if path == ":memory:" {
		return nil
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("remote device: inspect database file: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: database sidecar must be regular", ErrInvalidInput)
		}
		if err := os.Chmod(candidate, 0o600); err != nil {
			return fmt.Errorf("remote device: harden database file: %w", err)
		}
	}
	return nil
}
