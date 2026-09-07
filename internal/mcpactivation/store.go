// Package mcpactivation persists evidence-backed proposals before an MCP
// integration can enter the active settings file.
package mcpactivation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is part of ATENEA's public orchestration contract.
type Status string

const (
	// Proposed is part of ATENEA's public orchestration contract.
	Proposed Status = "proposed"
	// Applying is part of ATENEA's public orchestration contract.
	Applying Status = "applying"
	// Applied is part of ATENEA's public orchestration contract.
	Applied Status = "applied"
)

// Proposal is part of ATENEA's public orchestration contract.
type Proposal struct {
	ID                       string    `json:"id"`
	Status                   Status    `json:"status"`
	URL                      string    `json:"url,omitempty"`
	Command                  []string  `json:"command,omitempty"`
	ProtocolMode             string    `json:"protocol_mode"`
	RequestedProtocolVersion string    `json:"requested_protocol_version"`
	ObservedProtocolVersion  string    `json:"observed_protocol_version"`
	ServerName               string    `json:"server_name"`
	ServerVersion            string    `json:"server_version"`
	Capabilities             []string  `json:"capabilities"`
	Expose                   string    `json:"expose"`
	Tools                    []string  `json:"tools,omitempty"`
	Effects                  []string  `json:"effects,omitempty"`
	EvidenceDigest           string    `json:"evidence_digest"`
	CreatedAt                time.Time `json:"created_at"`
	AppliedAt                time.Time `json:"applied_at,omitempty"`
	SettingsPath             string    `json:"settings_path,omitempty"`
}

// Store is part of ATENEA's public orchestration contract.
type Store struct {
	path string
	mu   sync.Mutex
}

// New is part of ATENEA's public orchestration contract.
func New(path string) *Store { return &Store{path: path} }

// Put is part of ATENEA's public orchestration contract.
func (s *Store) Put(p Proposal) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireFileLock(s.path)
	if err != nil {
		return Proposal{}, err
	}
	defer release()
	if err := validate(p); err != nil {
		return Proposal{}, err
	}
	rows, err := s.load()
	if err != nil {
		return Proposal{}, err
	}
	for _, existing := range rows {
		if existing.ID == p.ID && (existing.Status == Applied || existing.Status == Applying) {
			return Proposal{}, fmt.Errorf("MCP proposal %q cannot be replaced while %s", p.ID, existing.Status)
		}
	}
	p.Status = Proposed
	p.CreatedAt = time.Now().UTC()
	p.AppliedAt = time.Time{}
	p.SettingsPath = ""
	p.Capabilities = clean(p.Capabilities)
	p.Tools = clean(p.Tools)
	p.Effects = clean(p.Effects)
	p.EvidenceDigest = digest(p)
	replaced := false
	for i := range rows {
		if rows[i].ID == p.ID {
			rows[i], replaced = p, true
		}
	}
	if !replaced {
		rows = append(rows, p)
	}
	return p, s.save(rows)
}

// Get is part of ATENEA's public orchestration contract.
func (s *Store) Get(id string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireFileLock(s.path)
	if err != nil {
		return Proposal{}, err
	}
	defer release()
	rows, err := s.load()
	if err != nil {
		return Proposal{}, err
	}
	for _, p := range rows {
		if p.ID == id {
			return p, nil
		}
	}
	return Proposal{}, fmt.Errorf("MCP proposal %q does not exist", id)
}

// List is part of ATENEA's public orchestration contract.
func (s *Store) List() ([]Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireFileLock(s.path)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := s.load()
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	return rows, err
}

// Activate journals the transition before applying settings. A retry of an
// interrupted Applying proposal calls apply again; the callback must accept an
// already-present exact declaration and refuse a different one.
func (s *Store) Activate(id, settingsPath, evidenceDigest string, apply func(Proposal) error) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireFileLock(s.path)
	if err != nil {
		return Proposal{}, err
	}
	defer release()
	rows, err := s.load()
	if err != nil {
		return Proposal{}, err
	}
	settingsPath, err = canonicalPath(settingsPath)
	if err != nil {
		return Proposal{}, fmt.Errorf("canonicalizing MCP settings path: %w", err)
	}
	for i := range rows {
		if rows[i].ID != id {
			continue
		}
		if rows[i].Status == Applied {
			return Proposal{}, fmt.Errorf("MCP proposal %q is already applied", id)
		}
		if rows[i].EvidenceDigest != evidenceDigest {
			return Proposal{}, errors.New("MCP proposal evidence changed before activation")
		}
		if rows[i].Status != Proposed && rows[i].Status != Applying {
			return Proposal{}, fmt.Errorf("MCP proposal %q has invalid state %q", id, rows[i].Status)
		}
		if rows[i].Status == Applying && rows[i].SettingsPath != "" && rows[i].SettingsPath != settingsPath {
			return Proposal{}, fmt.Errorf("MCP proposal %q is already applying to %s", id, rows[i].SettingsPath)
		}
		rows[i].Status, rows[i].SettingsPath = Applying, settingsPath
		if err := s.save(rows); err != nil {
			return Proposal{}, err
		}
		if err := apply(rows[i]); err != nil {
			return Proposal{}, err
		}
		rows[i].Status, rows[i].AppliedAt, rows[i].SettingsPath = Applied, time.Now().UTC(), settingsPath
		return rows[i], s.save(rows)
	}
	return Proposal{}, fmt.Errorf("MCP proposal %q does not exist", id)
}

func validate(p Proposal) error {
	if strings.TrimSpace(p.ID) == "" || strings.ContainsAny(p.ID, ". /\\\t\r\n") {
		return errors.New("MCP proposal id must be a simple non-empty name")
	}
	if (p.URL == "") == (len(p.Command) == 0) {
		return errors.New("MCP proposal needs exactly one URL or command")
	}
	if p.ObservedProtocolVersion == "" || p.ObservedProtocolVersion == "unknown" {
		return errors.New("MCP proposal requires observed protocol evidence")
	}
	if p.Expose != "off" && p.Expose != "raw" {
		return errors.New("MCP proposal expose must be off or raw")
	}
	if p.Expose == "raw" && (len(p.Tools) == 0 || len(p.Effects) == 0) {
		return errors.New("raw MCP proposal requires explicit tools and effects")
	}
	return nil
}

func digest(p Proposal) string {
	proposalCopy := p
	proposalCopy.Status, proposalCopy.CreatedAt, proposalCopy.AppliedAt, proposalCopy.SettingsPath, proposalCopy.EvidenceDigest = "", time.Time{}, time.Time{}, "", ""
	raw, _ := json.Marshal(proposalCopy)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func clean(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Store) load() ([]Proposal, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []Proposal
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("reading MCP proposals: %w", err)
	}
	for i := range rows {
		if err := validatePersisted(rows[i]); err != nil {
			return nil, fmt.Errorf("reading MCP proposal %q: %w", rows[i].ID, err)
		}
	}
	return rows, nil
}

func validatePersisted(p Proposal) error {
	if err := validate(p); err != nil {
		return err
	}
	if p.Status != Proposed && p.Status != Applying && p.Status != Applied {
		return fmt.Errorf("invalid state %q", p.Status)
	}
	if p.EvidenceDigest == "" || p.EvidenceDigest != digest(p) {
		return errors.New("content does not match evidence digest")
	}
	switch p.Status {
	case Proposed:
		if p.SettingsPath != "" || !p.AppliedAt.IsZero() {
			return errors.New("proposed entry contains activation state")
		}
	case Applying:
		if !filepath.IsAbs(p.SettingsPath) || !p.AppliedAt.IsZero() {
			return errors.New("applying entry has invalid activation state")
		}
	case Applied:
		if !filepath.IsAbs(p.SettingsPath) || p.AppliedAt.IsZero() {
			return errors.New("applied entry has invalid activation state")
		}
	}
	return nil
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

func (s *Store) save(rows []Proposal) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".mcp-proposals-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, s.path)
}
