package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill is a Codex skill managed by ATENEA.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

// CanonicalPlanModeSkill returns the agent-facing bridge between Codex Plan
// mode and ATENEA's dry-run decision planner.
func CanonicalPlanModeSkill() Skill {
	return Skill{
		Name:        "atenea-plan-mode",
		Description: "Automatically route repository planning through ATENEA when the current Codex collaboration mode is Plan. Do not use in Default mode or for work outside a registered repository.",
		Body: `# ATENEA Plan mode

Use this skill only when the active developer context says the Codex collaboration mode is ` + "`Plan`" + `. The mode signal, not words such as "plan" in the request, activates this workflow.

1. Before substantive planning, call ` + "`catalog.repositories`" + ` and choose the registered repository whose absolute path is the closest ancestor of the current working directory. If none matches, continue with ordinary Codex planning and say that ATENEA has no matching repository.
2. Call ` + "`decision.plan`" + ` once with the user's complete objective, the matched repository id, and only criteria or file paths the user actually supplied. Treat the returned graph, routing, budget, and policy as planning evidence; reconcile them with read-only repository inspection before presenting the final plan.
3. Keep Codex in Plan mode. Do not edit files, run implementation steps, or call ` + "`workflow.launch`" + `, ` + "`workflow.resume`" + `, ` + "`workflow.answer`" + `, or ` + "`atenea decide --run`" + `. Plan-mode selection authorizes planning, not execution or approval of effects.

If ` + "`decision.plan`" + ` is unavailable, state briefly that the ATENEA Plan bridge is not active and continue with normal read-only planning. Do not substitute a shell command containing untrusted user text.
`,
	}
}

// SkillSyncOptions configures managed Codex skill synchronization.
type SkillSyncOptions struct {
	Path   string
	Prune  bool
	Skills []Skill
}

const (
	skillManagedByLine = "  managed_by: atenea"
	skillDigestPrefix  = "  managed_digest: "
	skillDigestBlank   = "0000000000000000000000000000000000000000000000000000000000000000"
)

// SyncSkills atomically installs ATENEA-managed Codex skills while preserving
// foreign and locally modified skill files.
func SyncSkills(options SkillSyncOptions) (SyncReport, error) {
	path := strings.TrimSpace(options.Path)
	if path == "" {
		return SyncReport{}, errors.New("codex skills: directory is required")
	}
	skills := options.Skills
	if len(skills) == 0 {
		skills = []Skill{CanonicalPlanModeSkill()}
	}
	digest, err := skillsDigest(skills)
	if err != nil {
		return SyncReport{}, err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return SyncReport{}, fmt.Errorf("create Codex skills directory: %w", err)
	}
	skills = append([]Skill(nil), skills...)
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	report := SyncReport{Path: path, Digest: digest}
	canonical := make(map[string]bool, len(skills))
	for _, skill := range skills {
		canonical[skill.Name] = true
		skillDir := filepath.Join(path, skill.Name)
		if info, statErr := os.Lstat(skillDir); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			report.Skipped = append(report.Skipped, skill.Name+"/SKILL.md")
			continue
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return SyncReport{}, statErr
		}
		filename := filepath.Join(skillDir, "SKILL.md")
		want := renderSkillFile(skill)
		got, readErr := os.ReadFile(filename)
		if readErr == nil {
			if string(got) == want {
				continue
			}
			if !ownedSkillFile(got, skill.Name) {
				report.Skipped = append(report.Skipped, skill.Name+"/SKILL.md")
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
		if !entry.IsDir() || !strings.HasPrefix(name, "atenea-") || canonical[name] || !options.Prune {
			continue
		}
		filename := filepath.Join(path, name, "SKILL.md")
		data, readErr := os.ReadFile(filename)
		if readErr != nil || !ownedSkillFile(data, name) {
			continue
		}
		if err := os.Remove(filename); err != nil {
			return SyncReport{}, err
		}
		// Remove the directory only when it contains no foreign resources.
		_ = os.Remove(filepath.Dir(filename))
		report.Changed = true
		report.Pruned = append(report.Pruned, name)
	}
	sort.Strings(report.Pruned)
	sort.Strings(report.Skipped)
	report.Matches = !report.Changed && len(report.Skipped) == 0
	return report, nil
}

// CheckSkills reports whether every canonical skill is present byte-for-byte.
func CheckSkills(path string, skills []Skill) (SyncReport, error) {
	if len(skills) == 0 {
		skills = []Skill{CanonicalPlanModeSkill()}
	}
	digest, err := skillsDigest(skills)
	if err != nil {
		return SyncReport{}, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return SyncReport{Path: path, Digest: digest, Changed: true}, nil
	} else if err != nil {
		return SyncReport{}, err
	}
	report := SyncReport{Path: path, Digest: digest, Matches: true}
	for _, skill := range skills {
		data, readErr := os.ReadFile(filepath.Join(path, skill.Name, "SKILL.md"))
		if readErr != nil || string(data) != renderSkillFile(skill) {
			report.Changed = true
			report.Matches = false
		}
	}
	return report, nil
}

func validateSkill(skill Skill) error {
	if strings.TrimSpace(skill.Name) == "" || strings.TrimSpace(skill.Description) == "" || strings.TrimSpace(skill.Body) == "" {
		return fmt.Errorf("codex skill %q: name, description and body are required", skill.Name)
	}
	if skill.Name != filepath.Base(skill.Name) || strings.ContainsAny(skill.Name, "_/\\") {
		return fmt.Errorf("codex skill %q: name must use lowercase letters, digits and hyphens", skill.Name)
	}
	for _, r := range skill.Name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return fmt.Errorf("codex skill %q: name must use lowercase letters, digits and hyphens", skill.Name)
		}
	}
	if strings.ContainsAny(skill.Description, "\r\n") {
		return fmt.Errorf("codex skill %q: description must be one line", skill.Name)
	}
	return nil
}

func skillsDigest(skills []Skill) (string, error) {
	copyOfSkills := append([]Skill(nil), skills...)
	for _, skill := range copyOfSkills {
		if err := validateSkill(skill); err != nil {
			return "", err
		}
	}
	sort.Slice(copyOfSkills, func(i, j int) bool { return copyOfSkills[i].Name < copyOfSkills[j].Name })
	h := sha256.New()
	for _, skill := range copyOfSkills {
		_, _ = h.Write([]byte(renderSkillFile(skill)))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func renderSkillFile(skill Skill) string {
	description := strings.ReplaceAll(skill.Description, "\"", "\\\"")
	body := strings.TrimSpace(skill.Body) + "\n"
	unsigned := fmt.Sprintf("---\nname: %s\ndescription: \"%s\"\nmetadata:\n%s\n%s%s\n---\n\n%s", skill.Name, description, skillManagedByLine, skillDigestPrefix, skillDigestBlank, body)
	sum := sha256.Sum256([]byte(unsigned))
	return strings.Replace(unsigned, skillDigestBlank, hex.EncodeToString(sum[:]), 1)
}

func ownedSkillFile(data []byte, expectedName string) bool {
	text := string(data)
	if !strings.HasPrefix(text, "---\nname: "+expectedName+"\n") || strings.Count(text, skillManagedByLine) != 1 || strings.Count(text, skillDigestPrefix) != 1 {
		return false
	}
	start := strings.Index(text, skillDigestPrefix) + len(skillDigestPrefix)
	end := strings.IndexByte(text[start:], '\n')
	if end < 0 {
		return false
	}
	end += start
	digest := text[start:end]
	if len(digest) != sha256.Size*2 {
		return false
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return false
	}
	unsigned := text[:start] + skillDigestBlank + text[end:]
	sum := sha256.Sum256([]byte(unsigned))
	return digest == hex.EncodeToString(sum[:])
}
