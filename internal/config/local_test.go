package config

// These tests are in the package rather than config_test because withLocal
// takes the directory to resolve from. The alternative is chdir, and a test
// that changes the process's working directory cannot run beside another one.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// repo makes a directory look like a repository root and optionally writes an
// overlay into it.
func repo(t *testing.T, root, overlay string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", root, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
		t.Fatalf("write .git: %v", err)
	}
	if overlay != "" {
		dir := filepath.Join(root, LocalDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, LocalFile), []byte(overlay), 0o600); err != nil {
			t.Fatalf("write overlay: %v", err)
		}
	}
	return root
}

// base builds a global config with two repositories, the first of which is
// deliberately not the one under test: the measured failure this layer exists
// to avoid is a local block silently inheriting the FIRST global block's path.
func base(t *testing.T, root string) Config {
	t.Helper()
	first, err := fileRepository{
		ID: "decoy", Path: "/somewhere/else",
		Languages: []string{"rust"}, Scale: "large", VCS: "present",
	}.build("global")
	if err != nil {
		t.Fatalf("decoy: %v", err)
	}
	second, err := fileRepository{
		ID: "workspace", Path: root,
		Languages: []string{"go", "typescript"}, Scale: "large", VCS: "present",
	}.build("global")
	if err != nil {
		t.Fatalf("subject: %v", err)
	}
	return Config{
		Source:       "global",
		Repositories: []contract.Repository{first, second},
		Implementations: []contract.Implementation{
			{ID: "ripgrep"}, {ID: "fixture.search"},
		},
		Security: Security{Sensitive: []string{".env", "*.pem"}},
	}
}

// The whole point of the layer: a declared key wins, an omitted one inherits.
func TestDeclaredKeyWinsAndOmittedKeyInherits(t *testing.T) {
	root := repo(t, t.TempDir(), "[[repository]]\nscale = \"medium\"\n")
	cfg, err := withLocal(base(t, root), root)
	if err != nil {
		t.Fatalf("withLocal: %v", err)
	}

	subject := find(t, cfg, "workspace")
	if subject.Scale.String() != "medium" {
		t.Errorf("scale = %q, want the local value medium", subject.Scale)
	}
	if got := strings.Join(subject.Languages, ","); got != "go,typescript" {
		t.Errorf("languages = %q, want the global value inherited", got)
	}
	if subject.VCS.String() != "present" {
		t.Errorf("vcs = %q, want the global value inherited", subject.VCS)
	}
}

// The measured failure, pinned. Decoding the local file onto the global struct
// merges arrays of tables by position: the survivor keeps the first global
// block's path and every other repository is dropped, with no error. Both
// halves are checked because either alone would still be a working config that
// answers about the wrong directory.
func TestALocalRepositoryBlockNeitherStealsAPathNorDropsTheCatalog(t *testing.T) {
	root := repo(t, t.TempDir(), "[[repository]]\nscale = \"small\"\n")
	cfg, err := withLocal(base(t, root), root)
	if err != nil {
		t.Fatalf("withLocal: %v", err)
	}

	if len(cfg.Repositories) != 2 {
		t.Fatalf("repositories = %d, want 2: the local block replaced the catalog", len(cfg.Repositories))
	}
	decoy := find(t, cfg, "decoy")
	if decoy.Path != "/somewhere/else" || decoy.Scale.String() != "large" {
		t.Errorf("the unrelated repository changed: %+v", decoy)
	}
	subject := find(t, cfg, "workspace")
	if subject.Path != root {
		t.Errorf("path = %q, want %q: the local block inherited another repository's path",
			subject.Path, root)
	}
}

// Nothing is required locally, and a repository without a file must be
// indistinguishable from one that never had the layer.
func TestNoOverlayChangesNothing(t *testing.T) {
	root := repo(t, t.TempDir(), "")
	before := base(t, root)
	after, err := withLocal(before, root)
	if err != nil {
		t.Fatalf("withLocal: %v", err)
	}
	if after.Local != nil {
		t.Errorf("Local = %+v, want nil", after.Local)
	}
	if after.Source != before.Source {
		t.Errorf("Source = %q, want it untouched", after.Source)
	}
	if len(after.Repositories) != len(before.Repositories) {
		t.Errorf("repositories = %d, want %d", len(after.Repositories), len(before.Repositories))
	}
}

// A directory that is there but holds nothing readable is a settings file that
// never takes effect. That is the failure this whole codebase refuses to let
// happen quietly.
func TestAnEmptyOverlayDirectoryIsRefused(t *testing.T) {
	root := repo(t, t.TempDir(), "")
	if err := os.MkdirAll(filepath.Join(root, LocalDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, LocalDir, "atenea.toml"), []byte("\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := withLocal(base(t, root), root)
	if contract.KindOf(err) != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found (err = %v)", contract.KindOf(err), err)
	}
}

// Walking up stops at the first repository root. A nested repository is cloned
// and published on its own, so it must not inherit the workspace above it.
func TestTheNearestRepositoryRootWins(t *testing.T) {
	outer := repo(t, t.TempDir(), "[[repository]]\nscale = \"large\"\n")
	inner := repo(t, filepath.Join(outer, "nested"), "[[repository]]\nscale = \"small\"\n")
	deep := filepath.Join(inner, "packages", "thing")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for _, tc := range []struct{ from, want string }{
		{outer, outer},
		{filepath.Join(outer, "cli"), outer},
		{inner, inner},
		{deep, inner},
	} {
		if err := os.MkdirAll(tc.from, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		got, ok := RepoRoot(tc.from)
		if !ok {
			t.Fatalf("no repository root found from %s", tc.from)
		}
		if got != tc.want {
			t.Errorf("from %s: root = %s, want %s", tc.from, got, tc.want)
		}
	}

	// And the overlay that applies is the near one, not the far one.
	cfg, err := withLocal(base(t, inner), deep)
	if err != nil {
		t.Fatalf("withLocal: %v", err)
	}
	if cfg.Local == nil || cfg.Local.Root != inner {
		t.Fatalf("Local = %+v, want the nested root %s", cfg.Local, inner)
	}
}

// Outside a repository there is nothing to be about.
func TestOutsideARepositoryNothingApplies(t *testing.T) {
	dir := t.TempDir()
	cfg, err := withLocal(base(t, dir), dir)
	if err != nil {
		t.Fatalf("withLocal: %v", err)
	}
	if cfg.Local != nil {
		t.Errorf("Local = %+v, want nil", cfg.Local)
	}
}

// The allow list, key by key. Each of these would be a repository that arrived
// by clone deciding something about the machine that cloned it.
func TestKeysARepositoryMayNotDeclare(t *testing.T) {
	cases := map[string]string{
		"a command to launch":  "[[mcp_server]]\nid = \"x\"\ncommand = [\"sh\", \"-c\", \"curl evil\"]\n",
		"a retired dashboard":  "[[mcp_server]]\nid = \"x\"\nurl = \"http://127.0.0.1:1/mcp\"\ndashboard = \"http://127.0.0.1:1/ui\"\n",
		"an agent type":        "[[agent]]\nname = \"mine\"\nruns = \"reviewer\"\nsummary = \"weaker\"\n",
		"an agent ceiling":     "[local_agents]\neffects = [\"read\", \"write\"]\ncontext = [\"workspace\"]\n",
		"a model seam":         "[model]\nbinary = \"/tmp/x\"\n",
		"the orchestrator":     "[orchestrator]\nmax_parallel = 99\n",
		"a new implementation": "[[implementation]]\nid = \"mine\"\nprovider = \"p\"\ncapability = \"code.search\"\n",
		"a new capability":     "[[capability]]\nid = \"code.search\"\nversion = \"1.0.0\"\n",
		"the contract version": "contract = \"9.0.0\"\n",
		"machine-wide knobs":   "[core]\nshutdown_grace = \"99s\"\n",
		"the measurement base": "[metrics]\npath = \"/tmp/elsewhere.db\"\n",
		"the backup target":    "[backup]\npath = \"/tmp/elsewhere\"\n",
		"its own path":         "[[repository]]\npath = \"/etc\"\n",
		"its own id":           "[[repository]]\nid = \"something-else\"\n",
		"a key that is a typo": "[[repository]]\nscal = \"small\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := repo(t, t.TempDir(), body)
			_, err := withLocal(base(t, root), root)
			if contract.KindOf(err) != contract.FailureInvalidInput {
				t.Fatalf("kind = %v, want invalid_input (err = %v)", contract.KindOf(err), err)
			}
		})
	}
}

// The refusal has to name the key. A message that only says "not allowed"
// leaves the reader guessing which line to delete.
func TestARefusalNamesTheKeyAndTheReason(t *testing.T) {
	root := repo(t, t.TempDir(), "[[mcp_server]]\nid = \"x\"\ncommand = [\"sh\"]\n")
	_, err := withLocal(base(t, root), root)
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{"mcp_server", "command to launch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Tightening the guard is allowed; loosening it is not. A repository that
// could shorten this list would be disarming the secret skip for the machine
// that cloned it.
func TestSensitiveIsUnionedNeverReplaced(t *testing.T) {
	root := repo(t, t.TempDir(), "[security]\nsensitive = [\"secrets.yaml\", \".env\"]\n")
	cfg, err := withLocal(base(t, root), root)
	if err != nil {
		t.Fatalf("withLocal: %v", err)
	}
	for _, want := range []string{".env", "*.pem", "secrets.yaml"} {
		if !slices.Contains(cfg.Security.Sensitive, want) {
			t.Errorf("%q missing from %v", want, cfg.Security.Sensitive)
		}
	}
	if len(cfg.Security.Sensitive) != 3 {
		t.Errorf("sensitive = %v, want the union with no repeat", cfg.Security.Sensitive)
	}

	empty := repo(t, t.TempDir(), "[security]\nsensitive = []\n")
	cleared, err := withLocal(base(t, empty), empty)
	if err != nil {
		t.Fatalf("withLocal: %v", err)
	}
	if len(cleared.Security.Sensitive) != 2 {
		t.Errorf("an empty local list shortened the guard to %v", cleared.Security.Sensitive)
	}
}

// A rule may only speak for the repository it sits in, and may only prefer an
// implementation this binary actually has.
func TestSelectorRulesAreScopedAndChecked(t *testing.T) {
	root := repo(t, t.TempDir(),
		"[[repository]]\nscale = \"small\"\n\n[[selector.rule]]\ncapability = \"code.search\"\nprefer = \"ripgrep\"\n")
	cfg, err := withLocal(base(t, root), root)
	if err != nil {
		t.Fatalf("withLocal: %v", err)
	}
	if len(cfg.Selector.Rules) != 1 {
		t.Fatalf("rules = %+v, want one", cfg.Selector.Rules)
	}
	if got := cfg.Selector.Rules[0]; got.Repository != "workspace" || got.Prefer != "ripgrep" {
		t.Errorf("rule = %+v, want it scoped to this repository", got)
	}

	unknown := repo(t, t.TempDir(),
		"[[repository]]\nscale = \"small\"\n\n[[selector.rule]]\ncapability = \"code.search\"\nprefer = \"nope\"\n")
	if _, err := withLocal(base(t, unknown), unknown); err == nil {
		t.Error("a rule preferring an implementation this binary does not have was accepted")
	}

	elsewhere := repo(t, t.TempDir(),
		"[[repository]]\nscale = \"small\"\n\n[[selector.rule]]\ncapability = \"code.search\"\nrepository = \"decoy\"\nprefer = \"ripgrep\"\n")
	if _, err := withLocal(base(t, elsewhere), elsewhere); err == nil {
		t.Error("a rule about another repository was accepted")
	}
}

// selector.New refuses two rules under one capability+repository pair, so a
// local rule repeating a global one has to replace it rather than join it --
// otherwise the overlay would turn a preference into a refused boot.
func TestALocalRuleReplacesTheGlobalOneForTheSamePair(t *testing.T) {
	root := repo(t, t.TempDir(),
		"[[repository]]\nscale = \"small\"\n\n[[selector.rule]]\ncapability = \"code.search\"\nprefer = \"fixture.search\"\n")
	cfg := base(t, root)
	cfg.Selector.Rules = append(cfg.Selector.Rules, selector.Rule{
		Capability: "code.search", Repository: "workspace", Prefer: "ripgrep",
	})

	merged, err := withLocal(cfg, root)
	if err != nil {
		t.Fatalf("withLocal: %v", err)
	}
	if len(merged.Selector.Rules) != 1 {
		t.Fatalf("rules = %+v, want the global one replaced, not joined", merged.Selector.Rules)
	}
	if merged.Selector.Rules[0].Prefer != "fixture.search" {
		t.Errorf("prefer = %q, want the local value", merged.Selector.Rules[0].Prefer)
	}
}

// A repository the global file never declared is added, not ignored.
func TestAnUndeclaredRepositoryIsAdded(t *testing.T) {
	root := repo(t, filepath.Join(t.TempDir(), "brand-new"), "[[repository]]\nlanguages = [\"go\"]\n")
	cfg, err := withLocal(Config{Source: "global"}, root)
	if err != nil {
		t.Fatalf("withLocal: %v", err)
	}
	if len(cfg.Repositories) != 1 {
		t.Fatalf("repositories = %+v, want the overlay's own", cfg.Repositories)
	}
	if cfg.Repositories[0].ID != "brand-new" || cfg.Repositories[0].Path != root {
		t.Errorf("repository = %+v, want id from the directory and path from where the file was found", cfg.Repositories[0])
	}
	if cfg.Local == nil || !cfg.Local.Added {
		t.Errorf("Local = %+v, want Added", cfg.Local)
	}
}

// The source has to name both files. It is what tells a reader the settings
// are not only the global ones -- and `status` compares it to decide whether
// the running service is answering the same question, which it is not.
func TestSourceNamesBothFiles(t *testing.T) {
	root := repo(t, t.TempDir(), "[[repository]]\nscale = \"small\"\n")
	cfg, err := withLocal(base(t, root), root)
	if err != nil {
		t.Fatalf("withLocal: %v", err)
	}
	if !strings.Contains(cfg.Source, "global") || !strings.Contains(cfg.Source, LocalFile) {
		t.Errorf("Source = %q, want both files named", cfg.Source)
	}
}

// One file, one repository: a second block has no second subject.
func TestASecondRepositoryBlockIsRefused(t *testing.T) {
	root := repo(t, t.TempDir(), "[[repository]]\nscale = \"small\"\n\n[[repository]]\nscale = \"large\"\n")
	if _, err := withLocal(base(t, root), root); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
}

func find(t *testing.T, cfg Config, id string) contract.Repository {
	t.Helper()
	for _, repo := range cfg.Repositories {
		if repo.ID == id {
			return repo
		}
	}
	t.Fatalf("repository %q is gone from %+v", id, cfg.Repositories)
	return contract.Repository{}
}

// The agent keys left the schema with the built-in agents (contract 5.0.0), so
// an overlay that still carries one is refused by name with the same remedy
// the global file gets.
func TestAnOverlayWithARetiredAgentKeyIsRefusedByName(t *testing.T) {
	for key, body := range map[string]string{
		"agent":        "[[agent]]\nname = \"mine\"\nruns = \"reviewer\"\nsummary = \"s\"\n",
		"local_agents": "[local_agents]\neffects = [\"read\", \"write\"]\n",
		"model":        "[model]\nbinary = \"/tmp/x\"\n",
	} {
		root := repo(t, t.TempDir(), body)
		_, err := withLocal(base(t, root), root)
		if contract.KindOf(err) != contract.FailureInvalidInput {
			t.Fatalf("%s: kind = %v, want invalid_input (err = %v)", key, contract.KindOf(err), err)
		}
		if !strings.Contains(err.Error(), "was removed in contract 5.0.0") || !strings.Contains(err.Error(), "migration-5.md") {
			t.Errorf("%s: error %q does not name the removal and its remedy", key, err)
		}
	}
}

// indexed_by left the schema with the Kivgraph provider (#208), so an overlay
// that still carries it is refused by name, once, with the same remedy the
// global file gets.
func TestAnOverlayWithTheRemovedIndexedByKeyIsRefusedByName(t *testing.T) {
	root := repo(t, t.TempDir(), "[[repository]]\nscale = \"small\"\nindexed_by = [\"fixture\"]\n")
	_, err := withLocal(base(t, root), root)
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (err = %v)", contract.KindOf(err), err)
	}
	for _, want := range []string{"indexed_by was removed in contract 5.0.0", "migration-5.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "unknown key") {
		t.Errorf("error %q is the bare typo message", err)
	}
}

// LoadEffective asks about the process's own directory, which is the right
// question for a command someone typed and the wrong one for an agent spawned
// somewhere else. This test never changes its working directory: if the
// overlay is read from `os.Getwd` it does not appear at all.
func TestTheEffectiveSettingsCanBeAskedAboutAnotherDirectory(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "atenea.toml")
	if err := WriteDefault(settings, false); err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}
	t.Setenv("ATENEA_CONFIG", settings)

	elsewhere := repo(t, filepath.Join(t.TempDir(), "elsewhere"), "[[repository]]\nscale = \"small\"\n")

	here, err := LoadEffective("")
	if err != nil {
		t.Fatalf("LoadEffective: %v", err)
	}
	if here.Local != nil {
		t.Fatalf("this directory is not that repository, yet its overlay applied here: %+v", here.Local)
	}

	there, err := LoadEffectiveIn("", elsewhere)
	if err != nil {
		t.Fatalf("LoadEffectiveIn: %v", err)
	}
	if there.Local == nil || there.Local.Root != elsewhere {
		t.Errorf("the named repository's own overlay did not apply: %+v", there.Local)
	}
}

// TestTomlStringEscapesTheWayTomlReadsItBack is the unit the awkward-directory
// test cannot reach.
//
// A directory named with a bell or a vertical tab is legal on Linux and awkward
// to create in a test on every filesystem, and one named with invalid UTF-8 is
// legal too. Those are precisely the three cases where Go's quoting and TOML's
// grammar disagree, so they are checked here against the parser itself rather
// than through the filesystem.
func TestTomlStringEscapesTheWayTomlReadsItBack(t *testing.T) {
	for _, path := range []string{
		"/tmp/bell\aend",      // Go writes \a; TOML does not define it
		"/tmp/vertical\vend",  // Go writes \v; TOML does not define it
		"/tmp/plain",          //
		"/tmp/with\"quote",    //
		`/tmp/with\backslash`, //
		"/tmp/ñ-日本語",          //
		"/tmp/null\x00ish",    // not reachable as a real path, but not our job to assume
	} {
		quoted, err := tomlString(path)
		if err != nil {
			t.Errorf("tomlString(%q) refused a valid UTF-8 path: %v", path, err)
			continue
		}
		// Round-trip through the real parser: what TOML reads back has to be
		// the string that went in, which is the property strconv.Quote broke.
		var got struct {
			Path string `toml:"path"`
		}
		if _, err := toml.Decode("path = "+quoted, &got); err != nil {
			t.Errorf("tomlString(%q) produced %s, which TOML will not parse: %v", path, quoted, err)
			continue
		}
		if got.Path != path {
			t.Errorf("tomlString(%q) round-tripped to %q: the settings file would name a "+
				"different directory than the one it was made in", path, got.Path)
		}
	}

	// Invalid UTF-8 is refused rather than mangled. Go would write \xNN, which
	// TOML accepts and reads as U+00NN -- a silently different path.
	if _, err := tomlString("/tmp/\xff\xfe"); err == nil {
		t.Error("a path that is not valid UTF-8 was written into a settings file anyway")
	}
}
