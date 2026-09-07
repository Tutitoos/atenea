package codex

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

	"github.com/BurntSushi/toml"
)

// AgentProfile is part of ATENEA's public orchestration contract.
type AgentProfile struct {
	Name                  string `json:"name"`
	Role                  string `json:"role"`
	Description           string `json:"description"`
	DeveloperInstructions string `json:"developer_instructions"`
	Model                 string `json:"model"`
	ReasoningEffort       string `json:"reasoning_effort"`
	SandboxMode           string `json:"sandbox_mode"`
}

// CanonicalAgentProfiles is part of ATENEA's public orchestration contract.
func CanonicalAgentProfiles() []AgentProfile {
	const noDelegation = "Do not delegate work, create subagents, or perform external effects beyond the assigned scope."
	return []AgentProfile{
		{Name: "research", Role: "research", Description: "Research and plan the assigned Atenea task.", DeveloperInstructions: "Collect evidence and produce a concise plan. " + noDelegation, Model: "gpt-5.6-sol", ReasoningEffort: "medium", SandboxMode: "read-only"},
		{Name: "implement", Role: "implementation", Description: "Implement the assigned Atenea change.", DeveloperInstructions: "Make only the authorized implementation and verify it with focused tests. " + noDelegation, Model: "gpt-5.6-luna", ReasoningEffort: "xhigh", SandboxMode: "workspace-write"},
		{Name: "review", Role: "review", Description: "Review the assigned Atenea change independently.", DeveloperInstructions: "Inspect evidence, tests, and regressions; report findings without modifying scope. " + noDelegation, Model: "gpt-5.6-sol", ReasoningEffort: "medium", SandboxMode: "read-only"},
		{Name: "audit", Role: "audit", Description: "Audit the assigned Atenea result and its safety boundaries.", DeveloperInstructions: "Audit the delivered evidence and reject unsupported claims. " + noDelegation, Model: "gpt-6-astra", ReasoningEffort: "medium", SandboxMode: "read-only"},
	}
}

func validateAgentProfile(profile AgentProfile) error {
	if strings.TrimSpace(profile.Name) == "" || strings.TrimSpace(profile.Description) == "" || strings.TrimSpace(profile.DeveloperInstructions) == "" {
		return fmt.Errorf("codex profile %q: name, description and developer_instructions are required", profile.Name)
	}
	if strings.TrimSpace(profile.Model) == "" || strings.TrimSpace(profile.ReasoningEffort) == "" {
		return fmt.Errorf("codex profile %q: model and model_reasoning_effort are required", profile.Name)
	}
	if profile.SandboxMode != "read-only" && profile.SandboxMode != "workspace-write" {
		return fmt.Errorf("codex profile %q: unsupported sandbox_mode %q", profile.Name, profile.SandboxMode)
	}
	return nil
}

// AgentProfilesDigest is part of ATENEA's public orchestration contract.
func AgentProfilesDigest(profiles []AgentProfile) (string, error) {
	profilesCopy := append([]AgentProfile(nil), profiles...)
	for _, profile := range profilesCopy {
		if err := validateAgentProfile(profile); err != nil {
			return "", err
		}
	}
	sort.Slice(profilesCopy, func(i, j int) bool { return profilesCopy[i].Name < profilesCopy[j].Name })
	data, err := json.Marshal(profilesCopy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// SyncOptions is part of ATENEA's public orchestration contract.
type SyncOptions struct {
	Path     string
	Prune    bool
	Profiles []AgentProfile
}

// SyncReport is part of ATENEA's public orchestration contract.
type SyncReport struct {
	Path    string   `json:"path"`
	Digest  string   `json:"digest"`
	Changed bool     `json:"changed"`
	Matches bool     `json:"matches"`
	Pruned  []string `json:"pruned,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
}

const (
	managedBegin = "# BEGIN ATENEA CODEX AGENTS"
	managedEnd   = "# END ATENEA CODEX AGENTS"
)

// SyncAgentProfiles is part of ATENEA's public orchestration contract.
func SyncAgentProfiles(options SyncOptions) (SyncReport, error) {
	path := strings.TrimSpace(options.Path)
	if path == "" {
		return SyncReport{}, errors.New("codex profiles: directory is required")
	}
	profiles := options.Profiles
	if len(profiles) == 0 {
		profiles = CanonicalAgentProfiles()
	}
	digest, err := AgentProfilesDigest(profiles)
	if err != nil {
		return SyncReport{}, err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return SyncReport{}, fmt.Errorf("create Codex agents directory: %w", err)
	}
	profiles = append([]AgentProfile(nil), profiles...)
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	report := SyncReport{Path: path, Digest: digest}
	canonical := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		filename := filepath.Join(path, "atenea-"+profile.Name+".toml")
		canonical[filepath.Base(filename)] = true
		want := renderProfileFile(profile, digest)
		got, readErr := os.ReadFile(filename)
		if readErr == nil {
			if string(got) == want {
				continue
			}
			if !ownedProfileFile(got, profile) {
				report.Skipped = append(report.Skipped, filepath.Base(filename))
				continue
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return SyncReport{}, readErr
		}
		if err := atomicWriteText(filename, []byte(want), 0o600); err != nil {
			return SyncReport{}, err
		}
		report.Changed = true
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return SyncReport{}, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "atenea-") || !strings.HasSuffix(name, ".toml") || canonical[name] || !options.Prune {
			continue
		}
		filename := filepath.Join(path, name)
		data, readErr := os.ReadFile(filename)
		if readErr != nil {
			continue
		}
		if !ownedProfileFile(data, AgentProfile{Name: strings.TrimSuffix(strings.TrimPrefix(name, "atenea-"), ".toml")}) {
			continue
		}
		if err := os.Remove(filename); err != nil {
			return SyncReport{}, err
		}
		report.Changed = true
		report.Pruned = append(report.Pruned, strings.TrimSuffix(strings.TrimPrefix(name, "atenea-"), ".toml"))
	}
	sort.Strings(report.Pruned)
	sort.Strings(report.Skipped)
	report.Matches = !report.Changed && len(report.Skipped) == 0
	return report, nil
}

// CheckAgentProfiles is part of ATENEA's public orchestration contract.
func CheckAgentProfiles(path string, profiles []AgentProfile) (SyncReport, error) {
	digest, err := AgentProfilesDigest(profiles)
	if err != nil {
		return SyncReport{}, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return SyncReport{Path: path, Digest: digest, Changed: true}, nil
	} else if err != nil {
		return SyncReport{}, err
	}
	report := SyncReport{Path: path, Digest: digest, Changed: false, Matches: true}
	for _, profile := range profiles {
		filename := filepath.Join(path, "atenea-"+profile.Name+".toml")
		data, readErr := os.ReadFile(filename)
		want := renderProfileFile(profile, digest)
		if readErr != nil || string(data) != want {
			report.Changed = true
			report.Matches = false
		}
	}
	return report, nil
}

func renderProfileFile(profile AgentProfile, digest string) string {
	return fmt.Sprintf("%s digest=%s profile_digest=%s\nname = %q\ndescription = %q\ndeveloper_instructions = %q\nmodel = %q\nmodel_reasoning_effort = %q\nsandbox_mode = %q\n", managedBegin, digest, profileWireDigest(profile), profile.Name, profile.Description, profile.DeveloperInstructions, profile.Model, profile.ReasoningEffort, profile.SandboxMode)
}

// profileWireDigest is deliberately computed from the complete top-level
// Codex agent payload. The marker is only an ownership hint; a file is
// managed when its marker is first-line exact, its TOML is valid, its fields
// are the closed schema we write, and this digest matches the parsed content.
// That prevents a substring marker or a hand-edited field from authorizing an
// overwrite or prune.
func profileWireDigest(profile AgentProfile) string {
	wire := struct {
		Name                  string `json:"name"`
		Description           string `json:"description"`
		DeveloperInstructions string `json:"developer_instructions"`
		Model                 string `json:"model"`
		ReasoningEffort       string `json:"model_reasoning_effort"`
		SandboxMode           string `json:"sandbox_mode"`
	}{profile.Name, profile.Description, profile.DeveloperInstructions, profile.Model, profile.ReasoningEffort, profile.SandboxMode}
	data, _ := json.Marshal(wire)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type parsedAgentFile struct {
	Name                  string `toml:"name"`
	Description           string `toml:"description"`
	DeveloperInstructions string `toml:"developer_instructions"`
	Model                 string `toml:"model"`
	ReasoningEffort       string `toml:"model_reasoning_effort"`
	SandboxMode           string `toml:"sandbox_mode"`
}

func ownedProfileFile(data []byte, expected AgentProfile) bool {
	line := string(data)
	first, _, _ := strings.Cut(line, "\n")
	prefix := managedBegin + " digest="
	if !strings.HasPrefix(first, prefix) || !strings.Contains(first, " profile_digest=") {
		return false
	}
	marker := strings.TrimPrefix(first, prefix)
	parts := strings.Split(marker, " profile_digest=")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(parts[0], " \t") || strings.ContainsAny(parts[1], " \t") {
		return false
	}
	var parsed parsedAgentFile
	meta, err := toml.Decode(string(data), &parsed)
	if err != nil || len(meta.Undecoded()) != 0 {
		return false
	}
	if parsed.Name == "" || parsed.Name != expected.Name || parsed.Description == "" || parsed.DeveloperInstructions == "" || parsed.Model == "" || parsed.ReasoningEffort == "" || (parsed.SandboxMode != "read-only" && parsed.SandboxMode != "workspace-write") {
		return false
	}
	actual := AgentProfile{Name: parsed.Name, Description: parsed.Description, DeveloperInstructions: parsed.DeveloperInstructions, Model: parsed.Model, ReasoningEffort: parsed.ReasoningEffort, SandboxMode: parsed.SandboxMode}
	return parts[1] == profileWireDigest(actual)
}

func atomicWriteText(path string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create Codex config directory: %w", err)
	}
	tmp, err := os.CreateTemp(parent, ".atenea-codex-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
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
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace Codex config atomically: %w", err)
	}
	dir, err := os.Open(parent)
	if err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return err
}
