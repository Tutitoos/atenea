package config

// A repository may carry its own settings, in `.atenea/config.toml` at its
// root. It is a partial overlay: what it declares wins, what it leaves out
// falls back to the global file, and a repository without one changes nothing
// at all.
//
// Two things shape everything below.
//
// The first is a measurement. The obvious implementation -- decode the global
// file, then decode the local one on top of the same struct and let TOML do
// the merging -- is correct for scalars and tables and silently wrong for
// arrays of tables. Measured against toml v1.6.0: a global with two
// [[repository]] blocks, overlaid by a local file declaring one, comes back
// with a single repository, and that survivor keeps the FIRST global block's
// path while taking the local block's scale. The library decodes element 0
// onto element 0 and truncates -- it merges by position, not by identity. A
// local file naming its own repository would inherit another repository's
// path and drop the rest of the catalog, without an error. So nothing here
// ever decodes the local file onto the global one: the local file is decoded
// into its own shape and merged by key, explicitly, below.
//
// The second is who writes the file. A local file travels inside the
// repository, so cloning somebody's repository is accepting their settings.
// That makes most of this schema unsafe to accept from it: [[mcp_server]] and
// every `process` block carry a command to launch, `[security]` can disarm
// the guard that skips secrets, and `[[implementation]]` decides what runs
// behind a name. Those keys are safe today only because the machine's owner
// is the only author, which is not a property of a file that arrives with a
// git clone. The overlay therefore accepts three things -- what a repository
// is, which provider to prefer for it, and additional files to treat as
// delicate -- and refuses the rest by name.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// LocalDir is the per-repository settings directory, at the repository root.
const LocalDir = ".atenea"

// LocalFile is the file read inside LocalDir. It is named rather than globbed
// so that a misspelled file is a refusal instead of a setting that never takes
// effect.
const LocalFile = "config.toml"

// DisableLocalEnv turns the layer off. A repository-provided file that changes
// behavior needs a way to be ignored without editing it.
const DisableLocalEnv = "ATENEA_LOCAL_CONFIG"

// Local records where an overlay came from and what it declared, so a reader
// can be told which half of the effective settings is whose.
type Local struct {
	// Path is the overlay file.
	Path string
	// Root is the repository root it was found at.
	Root string
	// Repository is the id of the repository it patched or added.
	Repository string
	// Added is true when the overlay introduced a repository the global file
	// did not declare, rather than patching one it did.
	Added bool
	// Keys are the keys the file declared, sorted. Provenance for `config
	// show`: everything else in the effective settings came from the global
	// file or the compiled fallbacks.
	Keys []string
}

// localSettings is the whole of what a repository may declare. Every field is
// a pointer or a slice so that an omitted key and a key set to an empty value
// are different things: `scale = ""` is a repository declaring it is
// unmeasured, and leaving `scale` out is a repository saying nothing about it,
// which inherits.
//
// The shape is also the allow list. A key with no field here lands in
// meta.Undecoded() and is refused, which is why `path` and `id` are absent:
// the path of a local overlay is the directory it was found in, and letting
// the file name another one would be the one way this layer could reach
// outside its own tree.
type localSettings struct {
	Repositories []localRepository `toml:"repository"`
	Selector     localSelector     `toml:"selector"`
	Security     localSecurity     `toml:"security"`
}

type localRepository struct {
	Languages *[]string `toml:"languages"`
	Scale     *string   `toml:"scale"`
	VCS       *string   `toml:"vcs"`
	IndexedBy *[]string `toml:"indexed_by"`
}

type localSelector struct {
	Rules []localRule `toml:"rule"`
}

type localRule struct {
	Capability string `toml:"capability"`
	Repository string `toml:"repository"`
	Prefer     string `toml:"prefer"`
}

type localSecurity struct {
	Sensitive *[]string `toml:"sensitive"`
}

// refusedLocally names the keys a repository may not declare, and why. The
// reason is carried because the bare refusal is the less useful half: a key
// that exists in the global schema is not a typo, and a reader who is told
// only "not allowed here" has to guess whether the layer is limited on
// purpose or broken.
var refusedLocally = map[string]string{
	"contract":       "the contract version is a property of this binary, not of a repository",
	"core":           "operational knobs are machine-wide; a repository cannot retune the process that serves every other one",
	"orchestrator":   "it carries commands to launch, so a cloned repository would be handing this machine a process to run",
	"metrics":        "the measurement base is machine-wide state, and a repository pointing it elsewhere would split the history it is ranked on",
	"backup":         "the backup target is machine-wide state",
	"capability":     "the catalog is what Atenea answers for; a repository redefining it would change what a name means",
	"implementation": "a repository cannot declare what runs behind a capability, only prefer among the implementations already declared",
	"mcp_server":     "it carries a command to launch, so a cloned repository would be handing this machine a process to run",
}

// refusedLocalLeaves names the keys refused inside a block that is otherwise
// allowed.
var refusedLocalLeaves = map[string]string{
	"repository.id":   "the id of a local overlay's repository is not the file's to choose",
	"repository.path": "the path is the directory this file was found in; naming another one is the one way this layer could reach outside its own tree",
}

// LoadEffective reads the global settings and, when the working directory sits
// in a repository that carries one, merges that repository's overlay over it.
//
// Separate from Load rather than a flag on it because the two answer different
// questions. Load answers "what does this file say", which is what the service
// and `service install` need: both are machine-wide, and a long-lived process
// that answers about many repositories must not bake in the overlay of
// whichever directory it happened to start in. LoadEffective answers "what
// applies here", which is what a one-shot command in a working tree wants.
func LoadEffective(explicit string) (Config, error) {
	cfg, err := Load(explicit)
	if err != nil {
		return Config{}, err
	}
	if os.Getenv(DisableLocalEnv) == "0" {
		return cfg, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return Config{}, contract.Fail(contract.FailureUnavailable,
			"cannot read the working directory to look for %s: %v", LocalDir, err)
	}
	return withLocal(cfg, cwd)
}

// withLocal applies the overlay of the repository containing dir, if there is
// one. Split from LoadEffective so a test can name the directory instead of
// changing the process's own.
func withLocal(cfg Config, dir string) (Config, error) {
	root, ok := repoRoot(dir)
	if !ok {
		// Not in a repository. Nothing to overlay, and nothing wrong: the
		// unit of work here is the repository, so outside one there is no
		// place for this file to live.
		return cfg, nil
	}
	path := filepath.Join(root, LocalDir, LocalFile)
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		return applyLocal(cfg, raw, root, path)
	case errors.Is(err, fs.ErrNotExist):
		// A directory that is there but holds no file we read is the one
		// shape worth refusing: `.atenea/atenea.toml` is the mistake this
		// catches, and left quiet it is a whole settings file that never
		// takes effect and never says so.
		if info, statErr := os.Stat(filepath.Join(root, LocalDir)); statErr == nil && info.IsDir() {
			return Config{}, contract.Fail(contract.FailureNotFound,
				"%s exists but holds no %s: %s declares nothing",
				filepath.Join(root, LocalDir), LocalFile, filepath.Join(root, LocalDir))
		}
		return cfg, nil
	default:
		return Config{}, contract.Fail(contract.FailureNotFound,
			"local settings %s: %v", path, err)
	}
}

// repoRoot walks up from dir to the first directory holding a .git, and that
// is the active repository.
//
// It stops at the first one rather than continuing to an outer repository on
// purpose. A nested repository is cloned and published on its own, so
// inheriting a parent workspace's overlay would make it behave differently
// depending on where it happens to be checked out. It is also the boundary
// already measured on this machine for the harnesses' own context files, and
// one boundary to remember beats two.
func repoRoot(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		// A .git file, not only a directory: that is what a worktree and a
		// submodule leave behind, and both are repository roots.
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// applyLocal merges one overlay over cfg.
func applyLocal(cfg Config, raw []byte, root, source string) (Config, error) {
	var declared localSettings
	meta, err := toml.Decode(string(raw), &declared)
	if err != nil {
		return Config{}, contract.Fail(contract.FailureInvalidInput,
			"local settings %s: %v", source, err)
	}
	if err := refuseUnreadableKeys(meta, source); err != nil {
		return Config{}, err
	}

	local := &Local{Path: source, Root: root, Keys: declaredKeys(meta)}

	if cfg, err = localRepositories(cfg, declared.Repositories, root, source, local); err != nil {
		return Config{}, err
	}
	if cfg, err = localRules(cfg, declared.Selector.Rules, source, local); err != nil {
		return Config{}, err
	}
	cfg.Security = localSensitive(cfg.Security, declared.Security.Sensitive)

	cfg.Source = cfg.Source + " + " + source
	cfg.Local = local
	return cfg, nil
}

// refuseUnreadableKeys turns every key the overlay shape did not accept into a
// refusal that says which it was and why.
//
// Reported once per refused block, not once per key inside it. A single
// [[mcp_server]] block produces one undecoded key per line it contains, and
// three copies of the same sentence is how a precise error becomes one nobody
// finishes reading.
func refuseUnreadableKeys(meta toml.MetaData, source string) error {
	undecoded := meta.Undecoded()
	if len(undecoded) == 0 {
		return nil
	}
	reasons := make([]string, 0, len(undecoded))
	blocked := make(map[string]bool, len(undecoded))
	for _, key := range undecoded {
		name := key.String()
		switch {
		case refusedLocalLeaves[name] != "":
			reasons = append(reasons, fmt.Sprintf("%s: %s", name, refusedLocalLeaves[name]))
		case refusedLocally[first(name)] != "":
			block := first(name)
			if blocked[block] {
				continue
			}
			blocked[block] = true
			reasons = append(reasons, fmt.Sprintf("%s: %s", block, refusedLocally[block]))
		default:
			// Not in the global schema either: a typo, and the message for a
			// typo is the one the global file already gives.
			reasons = append(reasons, fmt.Sprintf("%s: unknown key", name))
		}
	}
	sort.Strings(reasons)
	return contract.Fail(contract.FailureInvalidInput,
		"local settings %s: %s", source, strings.Join(reasons, "; "))
}

// first returns the top-level segment of a dotted key.
func first(key string) string {
	if i := strings.IndexByte(key, '.'); i >= 0 {
		return key[:i]
	}
	return key
}

func declaredKeys(meta toml.MetaData) []string {
	keys := meta.Keys()
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key.String())
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// localRepositories patches the repository this overlay sits in, or adds it.
//
// One block only. The file describes the repository it lives in, so a second
// block has no second repository to be about, and reading one of them as the
// subject would be a guess.
func localRepositories(cfg Config, declared []localRepository, root, source string, local *Local) (Config, error) {
	switch len(declared) {
	case 0:
		return cfg, nil
	case 1:
	default:
		return Config{}, contract.Fail(contract.FailureInvalidInput,
			"local settings %s: %d [[repository]] blocks: this file describes the repository it lives in, so there is one",
			source, len(declared))
	}
	block := declared[0]

	// Matched by path, not by id: the subject is the directory, and the id is
	// whatever the global file chose to call it.
	index := -1
	for i, repo := range cfg.Repositories {
		if samePath(repo.Path, root) {
			index = i
			break
		}
	}

	var merged fileRepository
	if index >= 0 {
		existing := cfg.Repositories[index]
		merged = fileRepository{
			ID:        existing.ID,
			Path:      existing.Path,
			Languages: existing.Languages,
			Scale:     existing.Scale.String(),
			VCS:       existing.VCS.String(),
			IndexedBy: existing.Indexes(),
		}
	} else {
		// A repository the global file never declared. The id has to come
		// from somewhere and the file may not choose it, so it is the
		// directory's own name -- and a collision with a different
		// repository's id is refused rather than resolved, because both
		// answers would be wrong for one of them.
		id := strings.ToLower(filepath.Base(root))
		for _, repo := range cfg.Repositories {
			if repo.ID == id {
				return Config{}, contract.Fail(contract.FailureInvalidInput,
					"local settings %s: this repository would be added as %q, which the global settings already use for %s",
					source, id, repo.Path)
			}
		}
		merged = fileRepository{ID: id, Path: root}
		local.Added = true
	}

	if block.Languages != nil {
		merged.Languages = *block.Languages
	}
	if block.Scale != nil {
		merged.Scale = *block.Scale
	}
	if block.VCS != nil {
		merged.VCS = *block.VCS
	}
	if block.IndexedBy != nil {
		merged.IndexedBy = *block.IndexedBy
	}

	built, err := merged.build(source)
	if err != nil {
		return Config{}, err
	}
	local.Repository = built.ID
	if index >= 0 {
		cfg.Repositories = slices.Clone(cfg.Repositories)
		cfg.Repositories[index] = built
	} else {
		cfg.Repositories = append(slices.Clone(cfg.Repositories), built)
	}
	return cfg, nil
}

// samePath compares two declared paths as directories on this machine.
// Symlinks are resolved when they can be: the global file may name a path
// through one and a shell may have arrived by another.
func samePath(a, b string) bool {
	clean := func(p string) string {
		abs, err := filepath.Abs(p)
		if err != nil {
			return filepath.Clean(p)
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			return resolved
		}
		return abs
	}
	return clean(a) == clean(b)
}

// localRules merges the overlay's selector rules.
//
// A rule replaces the global rule for the same capability and repository
// rather than joining it: selector.New refuses two rules under one such pair,
// so appending would turn a local preference that repeats a global one into a
// refused boot.
func localRules(cfg Config, declared []localRule, source string, local *Local) (Config, error) {
	if len(declared) == 0 {
		return cfg, nil
	}
	if local.Repository == "" {
		// Every rule this layer accepts is scoped to the overlay's own
		// repository, so there has to be one to scope it to.
		return Config{}, contract.Fail(contract.FailureInvalidInput,
			"local settings %s: a [[selector.rule]] needs a [[repository]] block in the same file to scope it to",
			source)
	}
	known := make(map[string]bool, len(cfg.Implementations))
	for _, impl := range cfg.Implementations {
		known[impl.ID] = true
	}

	rules := slices.Clone(cfg.Selector.Rules)
	for _, rule := range declared {
		if strings.TrimSpace(rule.Capability) == "" {
			return Config{}, contract.Fail(contract.FailureInvalidInput,
				"local settings %s: selector rule: capability is required", source)
		}
		if strings.TrimSpace(rule.Prefer) == "" {
			return Config{}, contract.Fail(contract.FailureInvalidInput,
				"local settings %s: selector rule for %s: prefer is required", source, rule.Capability)
		}
		// An implementation this binary does not have is a rule that would
		// never fire. Left quiet it reads as a preference in force.
		if !known[rule.Prefer] {
			return Config{}, contract.Fail(contract.FailureInvalidInput,
				"local settings %s: selector rule for %s prefers %q, which the global settings do not declare",
				source, rule.Capability, rule.Prefer)
		}
		// Scoped to this repository, always. A rule with no repository would
		// be a repository voting on how every other one is answered.
		switch rule.Repository {
		case "", local.Repository:
			rule.Repository = local.Repository
		default:
			return Config{}, contract.Fail(contract.FailureInvalidInput,
				"local settings %s: selector rule for %s names repository %q: a local rule may only apply to %s",
				source, rule.Capability, rule.Repository, local.Repository)
		}
		next := selector.Rule{
			Capability: rule.Capability,
			Repository: rule.Repository,
			Prefer:     rule.Prefer,
		}
		replaced := false
		for i, existing := range rules {
			if existing.Capability == next.Capability && existing.Repository == next.Repository {
				rules[i] = next
				replaced = true
				break
			}
		}
		if !replaced {
			rules = append(rules, next)
		}
	}
	cfg.Selector.Rules = rules
	return cfg, nil
}

// localSensitive unions the overlay's sensitive list with the global one.
//
// Union, never replacement: a repository naming more files as delicate is
// tightening the guard, and a repository shortening the list would be
// disarming it for the machine that cloned it. The one direction is safe and
// the other is not, so only one is offered.
func localSensitive(security Security, declared *[]string) Security {
	if declared == nil {
		return security
	}
	out := slices.Clone(security.Sensitive)
	for _, pattern := range *declared {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || slices.Contains(out, pattern) {
			continue
		}
		out = append(out, pattern)
	}
	security.Sensitive = out
	return security
}
