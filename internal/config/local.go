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
// every `process` block carry a command to launch, `[[agent]]` carries one
// AND the effects it may hold while running it, `[security]` can disarm the
// guard that skips secrets, and `[[implementation]]` decides what runs behind
// a name. Those keys are safe today only because the machine's owner
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
	"time"

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
	// Types are the agent types this overlay added, sorted. Named rather
	// than counted: a reader asking which types this machine has wants to
	// know which of them arrived with a clone.
	Types []string
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
	Agents       []localAgent      `toml:"agent"`
}

// localAgent is what a repository may declare about an agent type.
//
// Deliberately not fileAgent. The three keys that decide what actually runs
// -- command, args and env -- have no field here, so the decoder reports them
// undecoded and refuseUnreadableKeys names each one and why. What a local
// type runs is chosen by `runs`, which may only name a type this machine has
// already declared: the same latitude [[implementation]] gets, for the same
// reason.
//
// Every optional field is a pointer so that omitted and empty stay different.
// `effects = []` is a repository declaring a type that may cause nothing,
// which Validate refuses; leaving `effects` out is a repository saying
// nothing about them, which inherits the ceiling.
type localAgent struct {
	Name         string      `toml:"name"`
	Runs         string      `toml:"runs"`
	Summary      string      `toml:"summary"`
	Kind         *string     `toml:"kind"`
	Pool         *string     `toml:"pool"`
	ReadsSubject *bool       `toml:"reads_subject"`
	Context      *[]string   `toml:"context"`
	Effects      *[]string   `toml:"effects"`
	MaxDuration  *string     `toml:"max_duration"`
	MaxTokens    *int        `toml:"max_tokens"`
	Result       []fileField `toml:"result"`
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
//
// The three agent keys are the spawn: the binary, its argv and its
// environment. They are refused by name rather than by refusing the block
// around them, because the reason holds whether or not a repository may
// declare a type at all -- and since 2026-08-14 it may. A local type runs
// what `runs` names, and these three come from the type it named.
var refusedLocalLeaves = map[string]string{
	"repository.id":   "the id of a local overlay's repository is not the file's to choose",
	"repository.path": "the path is the directory this file was found in; naming another one is the one way this layer could reach outside its own tree",
	"agent.command":   "the command is the binary this machine spawns; a repository choosing it is a cloned file deciding what runs here",
	"agent.args":      "the arguments are the other half of the command; a repository choosing them would pick what the binary does",
	"agent.env":       "the environment is handed to the spawned process, and a PATH set there redirects even a command this machine declared",
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
	root, ok := RepoRoot(dir)
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

// RepoRoot walks up from dir to the first directory holding a .git, and that
// is the active repository.
//
// It stops at the first one rather than continuing to an outer repository on
// purpose. A nested repository is cloned and published on its own, so
// inheriting a parent workspace's overlay would make it behave differently
// depending on where it happens to be checked out. It is also the boundary
// already measured on this machine for the harnesses' own context files, and
// one boundary to remember beats two.
//
// Exported because the overlay is not the only thing that has to agree on
// what "this repository" means: reading the client configuration a team keeps
// in the repository has to resolve the same root, and two walks that could
// drift is one more thing that can be subtly wrong.
func RepoRoot(dir string) (string, bool) {
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
	if cfg, err = localAgents(cfg, declared.Agents, source, local); err != nil {
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

// localEffectCeiling and localContextCeiling are the machine-side cap on a
// type a repository declared: it may cause nothing but a read, and it may see
// nothing but the repository it came from.
//
// The cap is a constant here and a setting in neither file, which is
// deliberate for one release: a repository cannot raise it, and neither can a
// global file that predates the feature and therefore says nothing about it.
// An absent ceiling that reads as no ceiling is the same failure shape as an
// unmeasured cost that reads as free.
//
// Read is the floor of usefulness rather than a token gesture -- every shipped
// type holds exactly this and nothing more. Repository is the level that
// cannot leak: `workspace` is the catalog of every repository on the machine,
// and `history` is what other runs of this type were told.
var (
	localEffectCeiling  = []contract.Effect{contract.EffectRead}
	localContextCeiling = []contract.ContextLevel{contract.ContextRepository}
)

// localSummaryMax caps a repository's own summary.
//
// The planner is handed one line per type -- `- name (pool): summary. effects:
// ...` -- so the summary is the one piece of repository-authored prose that
// reaches a model's prompt. A newline in it would close that line and open
// another, and the next line could claim a type that does not exist with
// effects nobody granted. Control characters are refused for that reason and
// the length for a duller one: a menu is a menu.
const localSummaryMax = 200

// localAgents merges the overlay's agent types. Three properties hold this
// layer where it is.
//
// It runs what this machine already declared. `runs` names a type from the
// global settings, and the command, its arguments and its environment come
// from that type; the overlay has no field for any of the three. A repository
// picks among what is here, which is the latitude [[implementation]] already
// has and for the same reason.
//
// It cannot widen. Effects and context are checked against two ceilings and
// refused if they exceed either -- the type being run, and the machine-side
// cap above -- and when omitted they are the intersection of the two rather
// than the borrowed type's own. Limits may only come down.
//
// It cannot shadow. A name this machine already declares is a refusal naming
// both files. AgentTypeByName returns the first match, so a silent winner
// would be decided by the order the two files were read in, and a repository
// quietly redefining `reviewer` as something weaker is exactly what would
// hide there.
func localAgents(cfg Config, declared []localAgent, source string, local *Local) (Config, error) {
	if len(declared) == 0 {
		return cfg, nil
	}
	// Snapshotted before anything is appended: `runs` may name a type this
	// machine declared, never one the same file declared a few lines above.
	// A local type built on another local type is a chain whose ceiling
	// depends on which end you read it from.
	shipped := make(map[string]AgentType, len(cfg.Agents))
	taken := make(map[string]string, len(cfg.Agents))
	for _, agent := range cfg.Agents {
		shipped[agent.Spec.Name] = agent
		taken[agent.Spec.Name] = cfg.Source
	}

	for _, want := range declared {
		built, err := buildLocalAgent(want, shipped, taken, source)
		if err != nil {
			return Config{}, err
		}
		taken[built.Spec.Name] = source
		cfg.Agents = append(cfg.Agents, built)
		local.Types = append(local.Types, built.Spec.Name)
	}
	sort.Strings(local.Types)
	return cfg, nil
}

// buildLocalAgent turns one declared block into a type, or says why not.
func buildLocalAgent(want localAgent, shipped map[string]AgentType,
	taken map[string]string, source string) (AgentType, error) {
	name := strings.TrimSpace(want.Name)
	fail := func(format string, args ...any) (AgentType, error) {
		return AgentType{}, contract.Fail(contract.FailureInvalidInput,
			"local settings %s: agent %s: %s", source, name, fmt.Sprintf(format, args...))
	}
	if name == "" {
		return AgentType{}, contract.Fail(contract.FailureInvalidInput,
			"local settings %s: an [[agent]] block declares no name", source)
	}
	if where, ok := taken[name]; ok {
		return fail("already declared in %s: a repository may add a type, never redefine one", where)
	}
	runs := strings.TrimSpace(want.Runs)
	if runs == "" {
		return fail("runs is required: a local type has no command of its own, " +
			"so it has to name the declared type whose command it borrows")
	}
	base, ok := shipped[runs]
	if !ok {
		return fail("runs %q, which this machine does not declare: declared are %s",
			runs, strings.Join(sortedNames(shipped), ", "))
	}

	out := base
	out.Local = true
	out.Spec = base.Spec.Clone()
	out.Spec.Name = name
	out.Args = slices.Clone(base.Args)
	out.Env = slices.Clone(base.Env)

	summary := strings.TrimSpace(want.Summary)
	switch {
	case summary == "":
		return fail("summary is required: it is the line the planner reads to " +
			"decide whether to pick this type at all")
	case len(summary) > localSummaryMax:
		return fail("summary is %d characters, over the %d a local one may take",
			len(summary), localSummaryMax)
	case strings.ContainsFunc(summary, isControl):
		return fail("summary carries a control character: it is rendered as one " +
			"line of the planner's menu, and a second line here would be a " +
			"repository writing an entry of its own")
	}
	out.Summary = summary

	// Kind, pool and subject belong to what runs, not to what named it. A
	// repository may restate them -- a declaration that says what it is is
	// worth more than one that leaves it implied -- and a mismatch is
	// refused rather than ignored. Turning reads_subject on is the one of
	// the three that would hand this type another step's whole answer.
	if want.Kind != nil {
		kind, err := contract.ParseAgentType(*want.Kind)
		if err != nil {
			return fail("%v", err)
		}
		if kind != base.Spec.Kind {
			return fail("kind %s, but %s is %s: a local type may restate what it runs, not relabel it",
				kind, runs, base.Spec.Kind)
		}
	}
	if want.Pool != nil {
		pool, err := ParsePool(*want.Pool)
		if err != nil {
			return fail("%v", err)
		}
		if pool != base.Pool {
			return fail("pool %s, but %s is scheduled in the %s pool: the lane belongs to the type being run",
				pool, runs, base.Pool)
		}
	}
	if want.ReadsSubject != nil && *want.ReadsSubject != base.ReadsSubject {
		return fail("reads_subject = %t, but %s is %t: being handed another step's whole answer is a property of what runs",
			*want.ReadsSubject, runs, base.ReadsSubject)
	}

	if want.Effects == nil {
		out.Effects = intersectEffects(base.Effects, localEffectCeiling)
	} else {
		effects := make([]contract.Effect, 0, len(*want.Effects))
		for _, declared := range *want.Effects {
			effect, err := contract.ParseEffect(declared)
			if err != nil {
				return fail("%v", err)
			}
			if !slices.Contains(base.Effects, effect) {
				return fail("effect %s, which %s does not hold", effect, runs)
			}
			if !slices.Contains(localEffectCeiling, effect) {
				return fail("effect %s: a type declared by a repository may cause %s and nothing else",
					effect, joinEffects(localEffectCeiling))
			}
			effects = append(effects, effect)
		}
		out.Effects = effects
	}

	if want.Context == nil {
		out.Context = intersectLevels(base.Context, localContextCeiling)
	} else {
		levels := make([]contract.ContextLevel, 0, len(*want.Context))
		for _, declared := range *want.Context {
			level, err := contract.ParseContextLevel(declared)
			if err != nil {
				return fail("%v", err)
			}
			if !slices.Contains(base.Context, level) {
				return fail("context %s, which %s is not served", level, runs)
			}
			if !slices.Contains(localContextCeiling, level) {
				return fail("context %s: a type declared by a repository is served %s and nothing else",
					level, joinLevels(localContextCeiling))
			}
			levels = append(levels, level)
		}
		out.Context = levels
	}

	limits := base.Limits
	if want.MaxDuration != nil {
		parsed, err := time.ParseDuration(strings.TrimSpace(*want.MaxDuration))
		if err != nil {
			return fail("max_duration %q: %v", *want.MaxDuration, err)
		}
		limits.MaxDuration = parsed
	}
	if want.MaxTokens != nil {
		limits.MaxTokens = *want.MaxTokens
	}
	if !limits.Fits(base.Limits) {
		return fail("limits %v and %d tokens, over the %v and %d that %s allows",
			limits.MaxDuration, limits.MaxTokens,
			base.Limits.MaxDuration, base.Limits.MaxTokens, runs)
	}
	out.Limits = limits

	if len(want.Result) > 0 {
		fields, err := buildFields(want.Result)
		if err != nil {
			return fail("%v", err)
		}
		out.Spec.Result = fields
	}

	if err := out.Validate(source); err != nil {
		return AgentType{}, err
	}
	return out, nil
}

// isControl reports whether r would break the one line a summary is rendered
// as. Tab is included: it is not a line break, but nothing in a menu needs it.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

func sortedNames(agents map[string]AgentType) []string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func intersectEffects(declared, ceiling []contract.Effect) []contract.Effect {
	out := make([]contract.Effect, 0, len(declared))
	for _, effect := range declared {
		if slices.Contains(ceiling, effect) {
			out = append(out, effect)
		}
	}
	return out
}

func intersectLevels(declared, ceiling []contract.ContextLevel) []contract.ContextLevel {
	out := make([]contract.ContextLevel, 0, len(declared))
	for _, level := range declared {
		if slices.Contains(ceiling, level) {
			out = append(out, level)
		}
	}
	return out
}

func joinEffects(effects []contract.Effect) string {
	names := make([]string, 0, len(effects))
	for _, effect := range effects {
		names = append(names, effect.String())
	}
	return strings.Join(names, ", ")
}

func joinLevels(levels []contract.ContextLevel) string {
	names := make([]string, 0, len(levels))
	for _, level := range levels {
		names = append(names, level.String())
	}
	return strings.Join(names, ", ")
}
