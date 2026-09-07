// Package codexcert owns the durable, sanitized evidence used to qualify a
// specific Codex installation. It never stores credentials, prompts, chat
// text, screenshots, or opaque account data.
package codexcert

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// SchemaVersion identifies the sanitized certificate JSON contract.
const SchemaVersion = 1

// State is the effective certification state.
type State string

// Certificate lifecycle states.
const (
	Pending State = "partial"
	Passed  State = "passed"
	Failed  State = "failed"
	Stale   State = "stale"
	Expired State = "expired"
)

// Fingerprint identifies one exact executable and version.
type Fingerprint struct {
	Path       string `json:"-"`
	Identifier string `json:"identifier,omitempty"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
}

// Machine identifies the certified operating-system installation without
// persisting its hostname.
type Machine struct {
	OS           string `json:"os"`
	OSVersion    string `json:"os_version"`
	Architecture string `json:"architecture"`
	HostnameHash string `json:"hostname_sha256"`
}

// Gate is one independently verified certification result.
type Gate struct {
	State      State     `json:"state"`
	EvidenceID string    `json:"evidence_id,omitempty"`
	CheckedAt  time.Time `json:"checked_at,omitempty"`
	Reason     string    `json:"reason,omitempty"`
}

// ProfileReceipt records the observable App Server identity for one role.
type ProfileReceipt struct {
	Role             string `json:"role"`
	RequestedModel   string `json:"requested_model"`
	ObservedModel    string `json:"observed_model"`
	RequestedEffort  string `json:"requested_effort"`
	ObservedEffort   string `json:"observed_effort"`
	ObservedProvider string `json:"observed_provider"`
	ObservedSource   string `json:"observed_source"`
	ThreadHash       string `json:"thread_sha256"`
	TurnHash         string `json:"turn_sha256"`
	UsageRevision    uint64 `json:"usage_revision"`
	Rerouted         bool   `json:"rerouted"`
}

// Certificate is the sanitized, exportable Codex evidence record.
type Certificate struct {
	SchemaVersion          int              `json:"schema_version"`
	ID                     string           `json:"certificate_id"`
	State                  State            `json:"state"`
	CreatedAt              time.Time        `json:"created_at"`
	ExpiresAt              time.Time        `json:"expires_at"`
	Commit                 string           `json:"commit"`
	Atenea                 Fingerprint      `json:"atenea"`
	CodexCLI               Fingerprint      `json:"codex_cli"`
	CodexDesktop           Fingerprint      `json:"codex_desktop"`
	Machine                Machine          `json:"machine"`
	AppServerSchemaVersion string           `json:"app_server_schema_version"`
	AppServerSchemaSHA256  string           `json:"app_server_schema_sha256"`
	ProfilesSHA256         string           `json:"profiles_sha256"`
	MCPOverlaySHA256       string           `json:"mcp_overlay_sha256"`
	PresentationSHA256     string           `json:"presentation_contract_sha256"`
	Identity               Gate             `json:"identity"`
	CLI                    Gate             `json:"cli"`
	Desktop                Gate             `json:"desktop"`
	Profiles               []ProfileReceipt `json:"profiles"`
	CleanupVerified        bool             `json:"cleanup_verified"`
	ChallengeHash          string           `json:"challenge_sha256,omitempty"`
	SignerKeySHA256        string           `json:"signer_key_sha256,omitempty"`
	Signature              string           `json:"signature,omitempty"`
}

// Current contains the fingerprints observed while evaluating a certificate.
type Current struct {
	Commit, AppServerSchemaVersion, AppServerSchemaSHA256, ProfilesSHA256, MCPOverlaySHA256, PresentationSHA256 string
	Atenea, CodexCLI, CodexDesktop                                                                              Fingerprint
	Machine                                                                                                     Machine
}

// New creates a pending certificate and a separately stored challenge nonce.
func New(ttl time.Duration, current Current) (Certificate, string, error) {
	if ttl <= 0 || ttl > 30*24*time.Hour {
		return Certificate{}, "", errors.New("codex certificate TTL must be between zero and 30 days")
	}
	idBytes, nonceBytes := make([]byte, 16), make([]byte, 24)
	if _, err := rand.Read(idBytes); err != nil {
		return Certificate{}, "", err
	}
	if _, err := rand.Read(nonceBytes); err != nil {
		return Certificate{}, "", err
	}
	id, nonce := hex.EncodeToString(idBytes), hex.EncodeToString(nonceBytes)
	now := time.Now().UTC().Truncate(time.Second)
	cert := Certificate{SchemaVersion: SchemaVersion, ID: id, State: Pending, CreatedAt: now, ExpiresAt: now.Add(ttl), Commit: current.Commit, Atenea: current.Atenea, CodexCLI: current.CodexCLI, CodexDesktop: current.CodexDesktop, Machine: current.Machine, AppServerSchemaVersion: current.AppServerSchemaVersion, AppServerSchemaSHA256: current.AppServerSchemaSHA256, ProfilesSHA256: current.ProfilesSHA256, MCPOverlaySHA256: current.MCPOverlaySHA256, PresentationSHA256: current.PresentationSHA256, Identity: Gate{State: Pending}, CLI: Gate{State: Pending}, Desktop: Gate{State: Pending}, ChallengeHash: Hash(nonce)}
	return cert, nonce, nil
}

// Hash returns a lowercase SHA-256 digest.
func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// FileFingerprint hashes one executable without retaining its exported path.
func FileFingerprint(path, version string) (Fingerprint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Fingerprint{}, err
	}
	sum := sha256.Sum256(data)
	return Fingerprint{Path: path, Version: strings.TrimSpace(version), SHA256: hex.EncodeToString(sum[:])}, nil
}

// CurrentMachine returns a privacy-preserving machine identity.
func CurrentMachine() Machine {
	host, _ := os.Hostname()
	return Machine{OS: runtime.GOOS, Architecture: runtime.GOARCH, HostnameHash: Hash(host)}
}

// EffectiveState applies expiry and exact-environment invalidation.
func (c Certificate) EffectiveState(now time.Time, current Current) State {
	if c.State == Failed {
		return Failed
	}
	if !now.Before(c.ExpiresAt) {
		return Expired
	}
	if c.Commit != current.Commit || !sameFingerprint(c.Atenea, current.Atenea) || !sameFingerprint(c.CodexCLI, current.CodexCLI) || !sameFingerprint(c.CodexDesktop, current.CodexDesktop) || c.Machine != current.Machine || c.AppServerSchemaVersion != current.AppServerSchemaVersion || c.AppServerSchemaSHA256 != current.AppServerSchemaSHA256 || c.ProfilesSHA256 != current.ProfilesSHA256 || c.MCPOverlaySHA256 != current.MCPOverlaySHA256 || c.PresentationSHA256 != current.PresentationSHA256 {
		return Stale
	}
	if c.Identity.State == Passed && c.CLI.State == Passed && c.Desktop.State == Passed && len(c.Profiles) == 4 && c.CleanupVerified {
		return Passed
	}
	return Pending
}

func sameFingerprint(a, b Fingerprint) bool {
	return a.Identifier == b.Identifier && a.Version == b.Version && a.SHA256 == b.SHA256
}

// Seal derives the stored state from completed gates.
func (c *Certificate) Seal() {
	if c.Identity.State == Passed && c.CLI.State == Passed && c.Desktop.State == Passed && len(c.Profiles) == 4 && c.CleanupVerified {
		c.State = Passed
	} else if c.Identity.State == Failed || c.CLI.State == Failed || c.Desktop.State == Failed || !c.CleanupVerified {
		c.State = Failed
	} else {
		c.State = Pending
	}
}

// Store persists certificates and challenges below ATENEA's state root.
type Store struct{ Root string }

// Save atomically writes one sanitized certificate and updates latest.
func (s Store) Save(c Certificate) error {
	if !validID(c.ID) {
		return errors.New("certificate id is required")
	}
	if err := ValidateSanitized(c); err != nil {
		return err
	}
	dir := filepath.Join(s.Root, "codex-certificates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".certificate-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, c.ID+".json")); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "latest"), []byte(c.ID+"\n"), 0o600)
}

// Load reads an explicit certificate or the latest when id is empty.
func (s Store) Load(id string) (Certificate, error) {
	if id == "" {
		data, err := os.ReadFile(filepath.Join(s.Root, "codex-certificates", "latest"))
		if err != nil {
			return Certificate{}, err
		}
		id = strings.TrimSpace(string(data))
	}
	if !validID(id) {
		return Certificate{}, errors.New("invalid certificate id")
	}
	data, err := os.ReadFile(filepath.Join(s.Root, "codex-certificates", id+".json"))
	if err != nil {
		return Certificate{}, err
	}
	var c Certificate
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	if c.ID != id {
		return Certificate{}, errors.New("certificate id does not match its filename")
	}
	return c, nil
}

// Challenge contains short-lived correlation values for CLI and Desktop.
type Challenge struct {
	Nonce, RunID, WorkflowID, InvocationID, ResultProof string
	ChecklistCount                                      int
	UsedAt                                              time.Time `json:"used_at,omitempty"`
	DesktopProcessSHA256                                string    `json:"desktop_process_sha256,omitempty"`
}

// NewChallengeToken returns a high-entropy correlation value.
func NewChallengeToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// SaveChallenge keeps correlation values separate from exported evidence.
func (s Store) SaveChallenge(id string, challenge Challenge) error {
	if !validID(id) || challenge.Nonce == "" || challenge.RunID == "" || challenge.WorkflowID == "" || challenge.InvocationID == "" || challenge.ResultProof == "" || challenge.ChecklistCount <= 0 {
		return errors.New("complete challenge is required")
	}
	dir := filepath.Join(s.Root, "codex-certificates", "challenges")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(challenge)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, id+".json"), data, 0o600)
}

// LoadChallenge returns the pending local Desktop challenge.
func (s Store) LoadChallenge(id string) (Challenge, error) {
	if !validID(id) {
		return Challenge{}, errors.New("invalid certificate id")
	}
	data, err := os.ReadFile(filepath.Join(s.Root, "codex-certificates", "challenges", id+".json"))
	if err != nil {
		return Challenge{}, err
	}
	var challenge Challenge
	if err := json.Unmarshal(data, &challenge); err != nil {
		return challenge, err
	}
	return challenge, nil
}

// ConsumeChallenge marks a Desktop challenge used exactly once. The exclusive
// marker makes concurrent invocations fail closed before either prints proof.
func (s Store) ConsumeChallenge(id string, usedAt time.Time, desktopProcessSHA256 string) (Challenge, error) {
	challenge, err := s.LoadChallenge(id)
	if err != nil {
		return Challenge{}, err
	}
	if !challenge.UsedAt.IsZero() {
		return Challenge{}, errors.New("certification challenge was already consumed")
	}
	if len(desktopProcessSHA256) != 64 {
		return Challenge{}, errors.New("desktop process receipt is required")
	}
	if _, err := hex.DecodeString(desktopProcessSHA256); err != nil {
		return Challenge{}, errors.New("desktop process receipt is required")
	}
	marker := filepath.Join(s.Root, "codex-certificates", "challenges", id+".used")
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return Challenge{}, errors.New("certification challenge was already consumed")
		}
		return Challenge{}, err
	}
	if err := file.Close(); err != nil {
		return Challenge{}, err
	}
	challenge.UsedAt = usedAt.UTC().Truncate(time.Second)
	challenge.DesktopProcessSHA256 = desktopProcessSHA256
	if err := s.SaveChallenge(id, challenge); err != nil {
		return Challenge{}, err
	}
	return challenge, nil
}

// DeleteChallenge removes a consumed or canceled challenge.
func (s Store) DeleteChallenge(id string) error {
	if !validID(id) {
		return errors.New("invalid certificate id")
	}
	dir := filepath.Join(s.Root, "codex-certificates", "challenges")
	var first error
	for _, suffix := range []string{".json", ".used", ".cursor", ".reconnect"} {
		if err := os.Remove(filepath.Join(dir, id+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}

// CursorPath is the private continuity marker shared by the two fresh CLI
// processes. It contains only a challenge digest and never enters a certificate.
func (s Store) CursorPath(id string) (string, error) {
	if !validID(id) {
		return "", errors.New("invalid certificate id")
	}
	return filepath.Join(s.Root, "codex-certificates", "challenges", id+".cursor"), nil
}

// ValidateSanitized refuses known secret or full-content field names.
func ValidateSanitized(c Certificate) error {
	data, _ := json.Marshal(c)
	lower := strings.ToLower(string(data))
	for _, forbidden := range []string{"access_token", "refresh_token", "id_token", "authorization", "prompt", "chat_text", "screenshot", "auth.json"} {
		if strings.Contains(lower, forbidden) {
			return fmt.Errorf("certificate contains forbidden field or value %q", forbidden)
		}
	}
	return nil
}

// ValidateReleaseArtifact checks an exported certificate without probing the
// CI runner, whose binaries and machine intentionally differ.
func ValidateReleaseArtifact(c Certificate, now time.Time, publicKey ed25519.PublicKey) error {
	if err := ValidateSanitized(c); err != nil {
		return err
	}
	if c.SchemaVersion != SchemaVersion || !validID(c.ID) || c.State != Passed || c.CreatedAt.IsZero() || c.ExpiresAt.Sub(c.CreatedAt) <= 0 || c.ExpiresAt.Sub(c.CreatedAt) > 30*24*time.Hour || !now.Before(c.ExpiresAt) || !c.CleanupVerified {
		return errors.New("codex certificate is not a current passed artifact")
	}
	if c.Identity.State != Passed || c.CLI.State != Passed || c.Desktop.State != Passed || len(c.Profiles) != len(RequiredProfiles) {
		return errors.New("codex certificate does not contain every passed gate and profile")
	}
	for _, gate := range []Gate{c.Identity, c.CLI, c.Desktop} {
		if gate.EvidenceID == "" || gate.CheckedAt.Before(c.CreatedAt) || gate.CheckedAt.After(c.ExpiresAt) {
			return errors.New("codex certificate contains an uncorrelated gate timestamp")
		}
	}
	seen := map[string]bool{}
	for _, receipt := range c.Profiles {
		if receipt.Rerouted || receipt.ObservedSource != "app_server_thread_receipt" || receipt.ObservedProvider != "openai" || receipt.RequestedModel != receipt.ObservedModel || receipt.RequestedEffort != receipt.ObservedEffort || receipt.ThreadHash == "" || receipt.TurnHash == "" || receipt.UsageRevision == 0 {
			return fmt.Errorf("invalid observable profile receipt for %s", receipt.Role)
		}
		seen[receipt.Role] = true
		var wanted *Profile
		for i := range RequiredProfiles {
			if RequiredProfiles[i].Role == receipt.Role {
				wanted = &RequiredProfiles[i]
				break
			}
		}
		if wanted == nil || receipt.RequestedModel != wanted.Model || receipt.RequestedEffort != wanted.Effort {
			return fmt.Errorf("unexpected required profile receipt for %s", receipt.Role)
		}
	}
	for _, profile := range RequiredProfiles {
		if !seen[profile.Role] {
			return fmt.Errorf("missing profile receipt for %s", profile.Role)
		}
	}
	for _, value := range []string{c.Commit, c.Atenea.Identifier, c.Atenea.Version, c.Atenea.SHA256, c.CodexCLI.Identifier, c.CodexCLI.Version, c.CodexCLI.SHA256, c.CodexDesktop.Identifier, c.CodexDesktop.Version, c.CodexDesktop.SHA256, c.Machine.OS, c.Machine.OSVersion, c.Machine.Architecture, c.Machine.HostnameHash, c.AppServerSchemaVersion, c.AppServerSchemaSHA256, c.ProfilesSHA256, c.MCPOverlaySHA256, c.PresentationSHA256, c.ChallengeHash} {
		if strings.TrimSpace(value) == "" {
			return errors.New("codex certificate has an empty required fingerprint")
		}
	}
	if len(publicKey) != ed25519.PublicKeySize || c.SignerKeySHA256 != Hash(string(publicKey)) {
		return errors.New("codex certificate signer is not trusted")
	}
	signature, err := base64.StdEncoding.DecodeString(c.Signature)
	if err != nil {
		return errors.New("codex certificate signature is malformed")
	}
	payload, err := signaturePayload(c)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("codex certificate signature is invalid")
	}
	return nil
}

// Sign seals a sanitized certificate with a local Ed25519 key.
func Sign(c *Certificate, privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 private key")
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	c.SignerKeySHA256 = Hash(string(publicKey))
	c.Signature = ""
	payload, err := signaturePayload(*c)
	if err != nil {
		return err
	}
	c.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return nil
}

func signaturePayload(c Certificate) ([]byte, error) { c.Signature = ""; return json.Marshal(c) }
func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".atomic-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
