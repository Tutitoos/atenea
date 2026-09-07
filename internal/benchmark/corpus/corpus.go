// Package corpus provides the fixed, provider-free acceptance corpus used by
// the benchmark command.
package corpus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/benchmark"
)

const (
	// SchemaVersion is part of ATENEA's public orchestration contract.
	SchemaVersion = 1
	// ManifestName is part of ATENEA's public orchestration contract.
	ManifestName = "manifest.json"
	// SchemaName is part of ATENEA's public orchestration contract.
	SchemaName = "schema.json"
	// CanonicalCorpusSHA256 is part of ATENEA's public orchestration contract.
	CanonicalCorpusSHA256 = "bb57b8555c44fdd754bc72855c7899adda318677b48ee5696820a710efec98a5"
)

// State is part of ATENEA's public orchestration contract.
type State string

const (
	// Passed is part of ATENEA's public orchestration contract.
	Passed State = "passed"
	// Failed is part of ATENEA's public orchestration contract.
	Failed State = "failed"
	// Unsupported is part of ATENEA's public orchestration contract.
	Unsupported State = "unsupported"
)

// Scenario is part of ATENEA's public orchestration contract.
type Scenario struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Capability  string `json:"capability"`
	Language    string `json:"language"`
	Check       string `json:"check"`
	Fixture     string `json:"fixture,omitempty"`
	Description string `json:"description"`
}

// Manifest is part of ATENEA's public orchestration contract.
type Manifest struct {
	SchemaVersion int        `json:"schema_version"`
	ID            string     `json:"id"`
	Version       string     `json:"version"`
	Description   string     `json:"description"`
	Scenarios     []Scenario `json:"scenarios"`
}

// ScenarioSpec is code-owned. The mutable manifest is checked against this
// catalog and cannot introduce checks or turn an absent capability into a
// silent skip.
type ScenarioSpec struct {
	ID              string
	Title           string
	Capability      string
	Language        string
	Check           string
	Fixture         string
	AllowedStates   []State
	ProtocolVersion string
	Transport       string
}

var canonicalCatalog = []ScenarioSpec{
	{ID: "S01", Title: "Go repository context", Capability: "code.context", Language: "go", Check: "go_context", Fixture: "fixtures/go/repository.go", AllowedStates: []State{Passed, Failed, Unsupported}},
	{ID: "S02", Title: "TypeScript repository context", Capability: "code.context", Language: "typescript", Check: "typescript_context", Fixture: "fixtures/typescript/repository.ts", AllowedStates: []State{Passed, Failed, Unsupported}},
	{ID: "S03", Title: "Dart repository context", Capability: "code.context", Language: "dart", Check: "dart_context", Fixture: "fixtures/dart/repository.dart", AllowedStates: []State{Passed, Failed, Unsupported}},
	{ID: "S04", Title: "Workspace context scope", Capability: "workspace.context", Language: "workspace", Check: "workspace_context", Fixture: "fixtures/workspace.json", AllowedStates: []State{Passed, Failed, Unsupported}},
	{ID: "S05", Title: "Go implementation relation", Capability: "symbol.implementations", Language: "go", Check: "go_implementations", Fixture: "fixtures/go/repository.go", AllowedStates: []State{Passed, Failed, Unsupported}},
	{ID: "S06", Title: "TypeScript implementation relation", Capability: "symbol.implementations", Language: "typescript", Check: "typescript_implementations", Fixture: "fixtures/typescript/repository.ts", AllowedStates: []State{Passed, Failed, Unsupported}},
	{ID: "S07", Title: "Dart semantic implementation coverage", Capability: "symbol.implementations", Language: "dart", Check: "dart_implementations", Fixture: "fixtures/dart/repository.dart", AllowedStates: []State{Passed, Failed, Unsupported}},
	{ID: "S08", Title: "MCP legacy protocol declaration", Capability: "mcp.compatibility", Language: "protocol", Check: "protocol_schema", Fixture: "fixtures/protocol-2025.json", ProtocolVersion: "2025-06-18", Transport: "stdio", AllowedStates: []State{Passed, Failed, Unsupported}},
	{ID: "S09", Title: "MCP current protocol declaration", Capability: "mcp.compatibility", Language: "protocol", Check: "protocol_schema", Fixture: "fixtures/protocol-2026.json", ProtocolVersion: "2026-07-28", Transport: "streamable-http", AllowedStates: []State{Passed, Failed, Unsupported}},
	{ID: "S10", Title: "Plan progress rendering", Capability: "plan.progress", Language: "markdown", Check: "progress_rendering", Fixture: "fixtures/progress.md", AllowedStates: []State{Passed, Failed, Unsupported}},
	{ID: "S11", Title: "Bounded recovery baseline", Capability: "workflow.recovery", Language: "integration", Check: "recovery_integration", Fixture: "fixtures/recovery.json", AllowedStates: []State{Passed, Failed, Unsupported}},
	{ID: "S12", Title: "Client chat visibility", Capability: "chat.visibility", Language: "integration", Check: "chat_visibility_integration", Fixture: "fixtures/chat-visibility.json", AllowedStates: []State{Passed, Failed, Unsupported}},
}

// Catalog is part of ATENEA's public orchestration contract.
func Catalog() []ScenarioSpec {
	result := make([]ScenarioSpec, len(canonicalCatalog))
	for i, spec := range canonicalCatalog {
		result[i] = spec
		result[i].AllowedStates = append([]State(nil), spec.AllowedStates...)
	}
	return result
}

// ArtifactHash is part of ATENEA's public orchestration contract.
type ArtifactHash struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Snapshot owns the one read of all bytes evaluated by a run.
type Snapshot struct {
	Root      string
	Manifest  Manifest
	Bytes     map[string][]byte
	Artifacts []ArtifactHash
	Hash      string
}

// ScenarioResult is part of ATENEA's public orchestration contract.
type ScenarioResult struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Preflight     State   `json:"fixture_preflight"`
	State         State   `json:"state"`
	AllowedStates []State `json:"allowed_states"`
	Valid         bool    `json:"valid"`
	Evidence      string  `json:"evidence,omitempty"`
	Failure       string  `json:"failure,omitempty"`
	DurationMS    float64 `json:"duration_ms"`
}

// DartBaselineResult is the read-only result of revalidating the two Dart
// scenarios in the fixed acceptance corpus. It deliberately exposes only the
// baseline checks needed by the Dart coverage report; it does not run a
// provider or modify the corpus.
type DartBaselineResult struct {
	ID         string `json:"id"`
	Capability string `json:"capability"`
	Language   string `json:"language"`
	Preflight  State  `json:"fixture_preflight"`
	State      State  `json:"state"`
	Valid      bool   `json:"valid"`
	Evidence   string `json:"evidence,omitempty"`
}

// RevalidateDartBaseline checks S03 and S07 against the pinned, provider-free
// corpus. The snapshot is read once and the expected hash is required, so a
// report cannot accidentally claim evidence from a changed corpus.
func RevalidateDartBaseline(root, expectedHash string) ([]DartBaselineResult, string, error) {
	if strings.TrimSpace(expectedHash) == "" {
		expectedHash = CanonicalCorpusSHA256
	}
	snapshot, err := LoadSnapshot(root)
	if err != nil {
		return nil, "", err
	}
	if err := ValidateSHA256(expectedHash); err != nil {
		return nil, "", err
	}
	if !strings.EqualFold(snapshot.Hash, expectedHash) {
		return nil, "", fmt.Errorf("corpus sha256 mismatch: got %s, want %s", snapshot.Hash, strings.ToLower(expectedHash))
	}
	results := make([]DartBaselineResult, 0, 2)
	for _, spec := range canonicalCatalog {
		if spec.ID != "S03" && spec.ID != "S07" {
			continue
		}
		preflight, detail := checkScenario(snapshot, spec)
		valid := slices.Contains(spec.AllowedStates, preflight)
		state := preflight
		results = append(results, DartBaselineResult{
			ID: spec.ID, Capability: spec.Capability, Language: spec.Language,
			Preflight: preflight, State: state, Valid: valid, Evidence: detail,
		})
	}
	return results, snapshot.Hash, nil
}

// Counts is part of ATENEA's public orchestration contract.
type Counts struct {
	Total       int `json:"total"`
	Passed      int `json:"passed"`
	Failed      int `json:"failed"`
	Unsupported int `json:"unsupported"`
}

// Evidence is part of ATENEA's public orchestration contract.
type Evidence struct {
	SchemaVersion int                `json:"schema_version"`
	CorpusID      string             `json:"corpus_id"`
	CorpusVersion string             `json:"corpus_version"`
	StartedAt     time.Time          `json:"started_at"`
	FinishedAt    time.Time          `json:"finished_at"`
	Manifest      benchmark.Manifest `json:"manifest"`
	ManifestHash  string             `json:"manifest_sha256"`
	CorpusHash    string             `json:"corpus_sha256"`
	Artifacts     []ArtifactHash     `json:"artifacts"`
	Results       []ScenarioResult   `json:"results"`
	Counts        Counts             `json:"counts"`
}

// Load is part of ATENEA's public orchestration contract.
func Load(root string) (Manifest, []ArtifactHash, error) {
	snapshot, err := LoadSnapshot(root)
	if err != nil {
		return Manifest{}, nil, err
	}
	return snapshot.Manifest, snapshot.Artifacts, nil
}

// LoadSnapshot is part of ATENEA's public orchestration contract.
func LoadSnapshot(root string) (Snapshot, error) {
	cleanRoot, err := validatedRoot(root)
	if err != nil {
		return Snapshot{}, err
	}
	rootFS, err := os.OpenRoot(cleanRoot)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open confined corpus root: %w", err)
	}
	defer func() { _ = rootFS.Close() }()
	bytesByPath := map[string][]byte{}
	artifacts := make([]ArtifactHash, 0, len(canonicalCatalog)+2)
	read := func(relative string) ([]byte, error) {
		relative = filepath.ToSlash(relative)
		if data, ok := bytesByPath[relative]; ok {
			return data, nil
		}
		if _, err := safePath(cleanRoot, relative); err != nil {
			return nil, err
		}
		// Root.ReadFile resolves the same relative name beneath an open root
		// descriptor. Unlike a separate Lstat followed by os.ReadFile, a
		// concurrent symlink replacement cannot escape the corpus tree.
		data, err := rootFS.ReadFile(filepath.FromSlash(relative))
		if err != nil {
			return nil, fmt.Errorf("read corpus file %q: %w", relative, err)
		}
		copyOfData := append([]byte(nil), data...)
		bytesByPath[relative] = copyOfData
		artifacts = append(artifacts, ArtifactHash{Path: relative, SHA256: hashBytes(copyOfData)})
		return copyOfData, nil
	}
	manifestBytes, err := read(ManifestName)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err := read(SchemaName); err != nil {
		return Snapshot{}, err
	}
	var manifest Manifest
	if err := decodeStrict(manifestBytes, &manifest); err != nil {
		return Snapshot{}, fmt.Errorf("decode corpus manifest: %w", err)
	}
	if err := ValidateManifest(manifest); err != nil {
		return Snapshot{}, err
	}
	for _, spec := range canonicalCatalog {
		if _, err := read(spec.Fixture); err != nil {
			return Snapshot{}, err
		}
	}
	paths, err := workspaceReferences(bytesByPath["fixtures/workspace.json"])
	if err != nil {
		return Snapshot{}, err
	}
	for _, path := range paths {
		if _, err := read(path); err != nil {
			return Snapshot{}, err
		}
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	return Snapshot{Root: cleanRoot, Manifest: manifest, Bytes: bytesByPath, Artifacts: artifacts, Hash: hashArtifacts(artifacts)}, nil
}

// ValidateManifest is part of ATENEA's public orchestration contract.
func ValidateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported corpus schema_version %d", manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.ID) == "" || strings.TrimSpace(manifest.Version) == "" {
		return errors.New("corpus id and version are required")
	}
	if len(manifest.Scenarios) != len(canonicalCatalog) {
		return fmt.Errorf("corpus must contain exactly %d scenarios, got %d", len(canonicalCatalog), len(manifest.Scenarios))
	}
	for index, scenario := range manifest.Scenarios {
		spec := canonicalCatalog[index]
		if scenario.ID != spec.ID || scenario.Title != spec.Title || scenario.Capability != spec.Capability || scenario.Language != spec.Language || scenario.Check != spec.Check || scenario.Fixture != spec.Fixture {
			return fmt.Errorf("scenario %d does not match canonical catalog: got %q/%q, want %q/%q", index, scenario.ID, scenario.Check, spec.ID, spec.Check)
		}
	}
	return nil
}

// Run is part of ATENEA's public orchestration contract.
func Run(ctx context.Context, root string, runManifest benchmark.Manifest) (Evidence, error) {
	return RunPinned(ctx, root, runManifest, CanonicalCorpusSHA256)
}

// RunPinned is part of ATENEA's public orchestration contract.
func RunPinned(ctx context.Context, root string, runManifest benchmark.Manifest, expectedHash string) (Evidence, error) {
	started := time.Now().UTC()
	if strings.TrimSpace(expectedHash) == "" {
		return Evidence{}, errors.New("expected corpus sha256 is required")
	}
	snapshot, err := LoadSnapshot(root)
	if err != nil {
		return Evidence{}, err
	}
	if err := ValidateSHA256(expectedHash); err != nil {
		return Evidence{}, err
	}
	if !strings.EqualFold(snapshot.Hash, expectedHash) {
		return Evidence{}, fmt.Errorf("corpus sha256 mismatch: got %s, want %s", snapshot.Hash, strings.ToLower(expectedHash))
	}
	evidence := runSnapshot(ctx, snapshot, runManifest, started)
	if err := ctx.Err(); err != nil {
		return evidence, err
	}
	return evidence, nil
}

// RunSnapshot evaluates the exact immutable bytes already loaded by the
// caller. Acceptance orchestration uses it so preflight and product gates
// share one identified input rather than rereading mutable files.
func RunSnapshot(ctx context.Context, snapshot Snapshot, runManifest benchmark.Manifest) Evidence {
	return runSnapshot(ctx, snapshot, runManifest, time.Now().UTC())
}

func runSnapshot(ctx context.Context, snapshot Snapshot, runManifest benchmark.Manifest, started time.Time) (evidence Evidence) {
	evidence = Evidence{SchemaVersion: SchemaVersion, CorpusID: snapshot.Manifest.ID, CorpusVersion: snapshot.Manifest.Version, StartedAt: started, Manifest: runManifest, ManifestHash: artifactHash(snapshot.Artifacts, ManifestName), CorpusHash: snapshot.Hash, Artifacts: append([]ArtifactHash(nil), snapshot.Artifacts...), Results: make([]ScenarioResult, 0, len(canonicalCatalog))}
	defer func() { finalizeEvidence(&evidence) }()
	for _, spec := range canonicalCatalog {
		select {
		case <-ctx.Done():
			return evidence
		default:
		}
		begin := time.Now()
		preflight, detail := checkScenario(snapshot, spec)
		// This provider-free runner proves that the fixed input is valid. It
		// deliberately does not claim that Atenea answered the capability: that
		// requires the product harness added by the corresponding delivery.
		state := Unsupported
		if preflight == Failed {
			state = Failed
		}
		valid := preflight != Failed && state != Failed && containsState(spec.AllowedStates, state)
		result := ScenarioResult{ID: spec.ID, Title: spec.Title, Preflight: preflight, State: state, AllowedStates: append([]State(nil), spec.AllowedStates...), Valid: valid, DurationMS: float64(time.Since(begin).Microseconds()) / 1000}
		if valid {
			prefix := "fixture preflight unavailable"
			if preflight == Passed {
				prefix = "fixture preflight passed"
			}
			result.Evidence = prefix + "; capability requires a product integration probe: " + detail
		} else {
			result.Failure = fmt.Sprintf("observed state %s is not allowed: %s", state, detail)
		}
		evidence.Results = append(evidence.Results, result)
	}
	return evidence
}

func finalizeEvidence(evidence *Evidence) {
	evidence.FinishedAt = time.Now().UTC()
	evidence.Counts.Total = len(evidence.Results)
	for _, result := range evidence.Results {
		switch result.State {
		case Passed:
			if result.Valid {
				evidence.Counts.Passed++
			} else {
				evidence.Counts.Failed++
			}
		case Unsupported:
			if result.Valid {
				evidence.Counts.Unsupported++
			} else {
				evidence.Counts.Failed++
			}
		default:
			evidence.Counts.Failed++
		}
	}
}

// ValidateSHA256 is part of ATENEA's public orchestration contract.
func ValidateSHA256(value string) error {
	if len(value) != sha256.Size*2 {
		return fmt.Errorf("corpus sha256 must contain %d hexadecimal characters", sha256.Size*2)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("invalid corpus sha256: %w", err)
	}
	return nil
}

// WriteJSON is part of ATENEA's public orchestration contract.
func WriteJSON(path string, evidence Evidence) error { return benchmark.WriteJSON(path, evidence) }

// Markdown is part of ATENEA's public orchestration contract.
func Markdown(evidence Evidence) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Atenea acceptance corpus\n\nCorpus: %s@%s  \nRun: %s  \nCommit: %s  \nCorpus SHA-256: %s\n\n", markdownCell(evidence.CorpusID), markdownCell(evidence.CorpusVersion), markdownCell(evidence.Manifest.RunID), markdownCell(evidence.Manifest.Commit), markdownCell(evidence.CorpusHash))
	fmt.Fprintf(&out, "Environment: %s/%s, Go %s  \nStatus: %d passed, %d failed, %d unsupported\n\n", markdownCell(evidence.Manifest.Environment.OS), markdownCell(evidence.Manifest.Environment.Arch), markdownCell(evidence.Manifest.Environment.Go), evidence.Counts.Passed, evidence.Counts.Failed, evidence.Counts.Unsupported)
	out.WriteString("| ID | Scenario | Preflight | Capability | Allowed | Valid | Evidence |\n|---|---|---|---|---|---|---|\n")
	for _, result := range evidence.Results {
		detail := result.Evidence
		if detail == "" {
			detail = result.Failure
		}
		allowed := make([]string, len(result.AllowedStates))
		for i, state := range result.AllowedStates {
			allowed[i] = string(state)
		}
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s | %t | %s |\n", markdownCell(result.ID), markdownCell(result.Title), markdownCell(string(result.Preflight)), markdownCell(string(result.State)), markdownCell(strings.Join(allowed, ", ")), result.Valid, markdownCell(detail))
	}
	return out.String()
}

// WriteMarkdown is part of ATENEA's public orchestration contract.
func WriteMarkdown(path string, evidence Evidence) error {
	return benchmark.WriteText(path, Markdown(evidence))
}

func checkScenario(snapshot Snapshot, spec ScenarioSpec) (State, string) {
	data, ok := snapshot.Bytes[filepath.ToSlash(spec.Fixture)]
	if !ok {
		return Failed, "fixture is absent from the snapshot"
	}
	switch spec.Check {
	case "go_context":
		return checkGo(data, false)
	case "typescript_context":
		return checkTypeScript(data, false)
	case "dart_context":
		return checkDart(data, false)
	case "workspace_context":
		return checkWorkspace(snapshot, data)
	case "go_implementations":
		return checkGo(data, true)
	case "typescript_implementations":
		return checkTypeScript(data, true)
	case "dart_implementations":
		return Unsupported, "Dart semantic implementation evidence is not supported locally"
	case "protocol_schema":
		return checkProtocol(data, spec)
	case "progress_rendering":
		return checkProgress(data)
	case "recovery_integration":
		return Unsupported, "provider failure injection requires an explicit integration run"
	case "chat_visibility_integration":
		return Unsupported, "live client chat rendering requires a host integration run"
	default:
		return Failed, "unknown canonical check " + spec.Check
	}
}

func checkGo(data []byte, implementation bool) (State, string) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "repository.go", data, parser.AllErrors)
	if err != nil {
		return Failed, "parse Go fixture: " + err.Error()
	}
	var hasRepository, hasMemory, hasStruct bool
	ast.Inspect(file, func(node ast.Node) bool {
		switch declaration := node.(type) {
		case *ast.TypeSpec:
			switch declaration.Name.Name {
			case "Repository":
				_, hasRepository = declaration.Type.(*ast.InterfaceType)
			case "MemoryRepository":
				hasMemory = true
				_, hasStruct = declaration.Type.(*ast.StructType)
			}
		}
		return true
	})
	if !hasRepository || !hasMemory || !hasStruct {
		return Failed, "Go AST is missing Repository, MemoryRepository, or its struct declaration"
	}
	if implementation {
		pkg, err := new(types.Config).Check("fixturego", fset, []*ast.File{file}, nil)
		if err != nil {
			return Failed, "type-check Go fixture: " + err.Error()
		}
		repository, ok := pkg.Scope().Lookup("Repository").Type().Underlying().(*types.Interface)
		if !ok {
			return Failed, "Repository is not an interface"
		}
		memoryObject := pkg.Scope().Lookup("MemoryRepository")
		if memoryObject == nil || !types.Implements(memoryObject.Type(), repository.Complete()) {
			return Failed, "MemoryRepository does not implement Repository with a compatible method set"
		}
		return Passed, "Go type checker confirms MemoryRepository implements Repository"
	}
	return Passed, "Go AST contains the repository interface and concrete declaration"
}

var (
	tsInterfaceRE  = regexp.MustCompile(`(?m)\bexport\s+interface\s+Repository\s*\{`)
	tsClassRE      = regexp.MustCompile(`(?m)\bexport\s+class\s+MemoryRepository(?:\s+implements\s+Repository)?\s*\{`)
	tsImplementsRE = regexp.MustCompile(`(?m)\bclass\s+MemoryRepository\s+implements\s+Repository\b`)
	tsGetRE        = regexp.MustCompile(`(?m)\bget\s*\(\s*id\s*:\s*string\s*\)\s*:\s*string\s*\|\s*undefined`)
	dartAbstractRE = regexp.MustCompile(`(?m)\babstract\s+class\s+Repository\s*\{`)
	dartClassRE    = regexp.MustCompile(`(?m)\bclass\s+MemoryRepository\s+implements\s+Repository\s*\{`)
	dartGetRE      = regexp.MustCompile(`(?m)\bString\?\s+get\s*\(\s*String\s+id\s*\)`)
)

func checkTypeScript(data []byte, implementation bool) (State, string) {
	text := string(data)
	if !tsInterfaceRE.MatchString(text) || !tsClassRE.MatchString(text) || !tsGetRE.MatchString(text) {
		return Failed, "TypeScript declaration probe is missing the interface, class, or typed method"
	}
	if implementation && !tsImplementsRE.MatchString(text) {
		return Failed, "TypeScript class has no explicit Repository implementation relation"
	}
	if implementation {
		return Passed, "TypeScript declaration probe contains the implements relation and typed method"
	}
	return Passed, "TypeScript declaration probe contains the interface and concrete class"
}

func checkDart(data []byte, implementation bool) (State, string) {
	text := string(data)
	if !dartAbstractRE.MatchString(text) || !dartClassRE.MatchString(text) || !dartGetRE.MatchString(text) {
		return Failed, "Dart declaration probe is missing the abstract class, concrete class, or typed method"
	}
	if implementation {
		return Unsupported, "Dart declarations are present, but semantic implementation evidence is not supported locally"
	}
	return Passed, "Dart declaration probe contains the repository contract and concrete class"
}

type workspaceFixture struct {
	Repositories []struct {
		Name     string   `json:"name"`
		Language string   `json:"language"`
		Files    []string `json:"files"`
	} `json:"repositories"`
}

var canonicalWorkspace = map[string]struct {
	language string
	files    []string
}{
	"fixture-go":         {language: "go", files: []string{"fixtures/go/repository.go"}},
	"fixture-typescript": {language: "typescript", files: []string{"fixtures/typescript/repository.ts"}},
	"fixture-dart":       {language: "dart", files: []string{"fixtures/dart/repository.dart"}},
}

func workspaceReferences(data []byte) ([]string, error) {
	var workspace workspaceFixture
	if err := json.Unmarshal(data, &workspace); err != nil {
		return nil, fmt.Errorf("decode workspace fixture: %w", err)
	}
	paths := make([]string, 0)
	for _, repository := range workspace.Repositories {
		paths = append(paths, repository.Files...)
	}
	return paths, nil
}

func checkWorkspace(snapshot Snapshot, data []byte) (State, string) {
	var workspace workspaceFixture
	if err := json.Unmarshal(data, &workspace); err != nil {
		return Failed, "decode workspace fixture: " + err.Error()
	}
	if len(workspace.Repositories) != len(canonicalWorkspace) {
		return Failed, fmt.Sprintf("workspace declares %d repositories, want %d", len(workspace.Repositories), len(canonicalWorkspace))
	}
	seen := map[string]bool{}
	for _, repository := range workspace.Repositories {
		want, ok := canonicalWorkspace[repository.Name]
		if !ok || seen[repository.Name] {
			return Failed, "workspace has an unknown or duplicate repository " + repository.Name
		}
		seen[repository.Name] = true
		if repository.Language != want.language || !sameStrings(repository.Files, want.files) {
			return Failed, "workspace scope for " + repository.Name + " is inconsistent"
		}
		for _, file := range repository.Files {
			if _, ok := snapshot.Bytes[filepath.ToSlash(file)]; !ok {
				return Failed, "workspace references a file absent from the snapshot: " + file
			}
		}
	}
	return Passed, "workspace identities, languages, and file scopes are consistent"
}

func checkProtocol(data []byte, spec ScenarioSpec) (State, string) {
	var protocol struct {
		ProtocolVersion string `json:"protocol_version"`
		Transport       string `json:"transport"`
	}
	if err := json.Unmarshal(data, &protocol); err != nil {
		return Failed, "decode protocol fixture: " + err.Error()
	}
	if protocol.ProtocolVersion != spec.ProtocolVersion {
		return Failed, fmt.Sprintf("protocol version %s does not match %s", protocol.ProtocolVersion, spec.ProtocolVersion)
	}
	if protocol.Transport != spec.Transport {
		return Failed, fmt.Sprintf("transport %s does not match %s", protocol.Transport, spec.Transport)
	}
	return Passed, "protocol version and transport match the canonical scenario"
}

var progressRE = regexp.MustCompile(`(?m)([0-9]+)\s*%\s*·\s*([0-9]+)/([0-9]+)\s+puntos\s+completados`)

func checkProgress(data []byte) (State, string) {
	text := string(data)
	progress := progressRE.FindStringSubmatch(text)
	if len(progress) != 4 {
		return Failed, "progress fixture has no parseable percentage and counter"
	}
	percent, _ := strconv.Atoi(progress[1])
	completed, _ := strconv.Atoi(progress[2])
	total, _ := strconv.Atoi(progress[3])
	checked := len(regexp.MustCompile(`(?m)^- \[x\]`).FindAllString(text, -1))
	unchecked := len(regexp.MustCompile(`(?m)^- \[ \]`).FindAllString(text, -1))
	if total != checked+unchecked || completed != checked || total == 0 || percent != completed*100/total {
		return Failed, "progress percentage, checkbox count, and total disagree"
	}
	return Passed, "progress counter and checkbox states are internally consistent"
}

func validatedRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("corpus root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve corpus root: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("stat corpus root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("corpus root must not be a symlink")
	}
	if !info.IsDir() {
		return "", errors.New("corpus root must be a directory")
	}
	return absolute, nil
}

func safePath(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("fixture path must be relative: %q", relative)
	}
	cleanRoot, err := validatedRoot(root)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(cleanRoot, filepath.FromSlash(relative)))
	if err != nil {
		return "", fmt.Errorf("resolve fixture %q: %w", relative, err)
	}
	prefix := cleanRoot + string(os.PathSeparator)
	if path == cleanRoot || !strings.HasPrefix(path, prefix) {
		return "", fmt.Errorf("fixture path escapes corpus root: %q", relative)
	}
	rel, err := filepath.Rel(cleanRoot, path)
	if err != nil {
		return "", fmt.Errorf("relativize fixture %q: %w", relative, err)
	}
	current := cleanRoot
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("fixture %q is unavailable: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("fixture path contains symlink component: %q", relative)
		}
	}
	return path, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
func hashBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func artifactHash(artifacts []ArtifactHash, path string) string {
	for _, artifact := range artifacts {
		if artifact.Path == path {
			return artifact.SHA256
		}
	}
	return ""
}
func hashArtifacts(artifacts []ArtifactHash) string {
	values := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		values = append(values, artifact.Path+":"+artifact.SHA256)
	}
	sort.Strings(values)
	return hashBytes([]byte(strings.Join(values, "\n") + "\n"))
}
func containsState(states []State, wanted State) bool {
	for _, state := range states {
		if state == wanted {
			return true
		}
	}
	return false
}
func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if filepath.ToSlash(left[i]) != filepath.ToSlash(right[i]) {
			return false
		}
	}
	return true
}

func markdownCell(value string) string {
	value = html.EscapeString(value)
	value = strings.NewReplacer(
		"\\", "&#92;", "|", "&#124;", "`", "&#96;", "\r", "&#13;", "\n", "&#10;",
		"!", "&#33;", "[", "&#91;", "]", "&#93;", "(", "&#40;", ")", "&#41;",
		"*", "&#42;", "_", "&#95;", "~", "&#126;", "#", "&#35;",
	).Replace(value)
	if len(value) > 240 {
		value = value[:240] + "&#8230;"
	}
	return value
}
