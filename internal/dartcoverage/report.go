// Package dartcoverage records the bounded, auditable Dart capability matrix.
//
// The report intentionally distinguishes local contract evidence from a real
// provider or client observation. A component test is never promoted to an
// Atenea end-to-end claim.
package dartcoverage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Tutitoos/atenea/internal/adapter/kivgraph"
	"github.com/Tutitoos/atenea/internal/benchmark/corpus"
	"github.com/Tutitoos/atenea/internal/workspacecontext"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const (
	// SchemaVersion is part of ATENEA's public orchestration contract.
	SchemaVersion = 1
	// MatrixVersion is part of ATENEA's public orchestration contract.
	MatrixVersion = "1.0.0"
	// KivgraphRevision is the provider baseline observed for this matrix. The
	// revision is recorded as evidence metadata; it does not enable any
	// capability in Atenea by itself.
	// KivgraphRevision is part of ATENEA's public orchestration contract.
	KivgraphRevision = "e28323742c8f148a859dcc54045727629ab4ba8e"

	// CapabilityCodeContext is part of ATENEA's public orchestration contract.
	CapabilityCodeContext = "code.context"
	// CapabilityWorkspace is part of ATENEA's public orchestration contract.
	CapabilityWorkspace = "workspace.context"
	// CapabilityImplementations is part of ATENEA's public orchestration contract.
	CapabilityImplementations = "symbol.implementations"
)

// Status is intentionally closed. Unknown means that the observation was not
// made; unsupported means that the declared language/capability combination is
// deliberately refused.
type Status string

const (
	// Proven is part of ATENEA's public orchestration contract.
	Proven Status = "proven"
	// Unsupported is part of ATENEA's public orchestration contract.
	Unsupported Status = "unsupported"
	// Unknown is part of ATENEA's public orchestration contract.
	Unknown Status = "unknown"
)

// EvidenceLevel is part of ATENEA's public orchestration contract.
type EvidenceLevel string

const (
	// Local is part of ATENEA's public orchestration contract.
	Local EvidenceLevel = "local"
	// ComponentLocal is part of ATENEA's public orchestration contract.
	ComponentLocal EvidenceLevel = "component_local"
	// RealProvider is part of ATENEA's public orchestration contract.
	RealProvider EvidenceLevel = "real_provider"
	// RealClient is part of ATENEA's public orchestration contract.
	RealClient EvidenceLevel = "real_client"
	// Pending is part of ATENEA's public orchestration contract.
	Pending EvidenceLevel = "pending"
)

// Evidence is part of ATENEA's public orchestration contract.
type Evidence struct {
	ID     string        `json:"id"`
	Source string        `json:"source"`
	Level  EvidenceLevel `json:"level"`
	Detail string        `json:"detail"`
}

// Row is part of ATENEA's public orchestration contract.
type Row struct {
	Capability         string        `json:"capability"`
	Language           string        `json:"language"`
	Declared           Status        `json:"declared"`
	Connected          Status        `json:"connected"`
	FunctionallyTested Status        `json:"functionally_tested"`
	EvidenceLevel      EvidenceLevel `json:"evidence_level"`
	Date               string        `json:"date"`
	Version            string        `json:"version"`
	Scope              string        `json:"scope"`
	Evidence           []Evidence    `json:"evidence"`
	Limitations        []string      `json:"limitations"`
}

// Component is part of ATENEA's public orchestration contract.
type Component struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Status        Status        `json:"status"`
	EvidenceLevel EvidenceLevel `json:"evidence_level"`
	Date          string        `json:"date"`
	Version       string        `json:"version"`
	Scope         string        `json:"scope"`
	Evidence      []Evidence    `json:"evidence"`
	Limitations   []string      `json:"limitations"`
}

// Baseline is part of ATENEA's public orchestration contract.
type Baseline struct {
	ID        string `json:"id"`
	Preflight string `json:"preflight"`
	State     string `json:"state"`
	Valid     bool   `json:"valid"`
	Evidence  string `json:"evidence"`
}

// ProviderBase is part of ATENEA's public orchestration contract.
type ProviderBase struct {
	Name       string `json:"name"`
	Revision   string `json:"revision"`
	Repository string `json:"repository"`
	Evidence   string `json:"evidence"`
}

// ProviderAttempt records a real process attempt separately from successful
// provider/client evidence. A failed attempt identifies the boundary reached
// without promoting connection or E2E status.
type ProviderAttempt struct {
	ID            string        `json:"id"`
	Provider      string        `json:"provider"`
	Revision      string        `json:"revision"`
	Command       string        `json:"command"`
	Status        string        `json:"status"`
	EvidenceLevel EvidenceLevel `json:"evidence_level"`
	Date          string        `json:"date"`
	Error         string        `json:"error"`
}

// Report is part of ATENEA's public orchestration contract.
type Report struct {
	SchemaVersion int               `json:"schema_version"`
	MatrixVersion string            `json:"matrix_version"`
	Date          string            `json:"date"`
	Scope         string            `json:"scope"`
	FixtureSHA256 string            `json:"fixture_sha256,omitempty"`
	CorpusSHA256  string            `json:"corpus_sha256,omitempty"`
	ProviderBase  ProviderBase      `json:"provider_base"`
	Attempts      []ProviderAttempt `json:"provider_attempts"`
	Baseline      []Baseline        `json:"baseline,omitempty"`
	Rows          []Row             `json:"rows"`
	Components    []Component       `json:"components"`
	Limitations   []string          `json:"limitations"`
}

// Options is part of ATENEA's public orchestration contract.
type Options struct {
	FixtureRoot  string
	CorpusRoot   string
	CorpusSHA256 string
	Date         string
}

// DefaultReport is the deterministic P14 fixture report. Its date is part of
// the versioned fixture, so rerunning the command does not rewrite evidence
// merely because the clock moved.
func DefaultReport() Report {
	date := "2026-09-06"
	version := MatrixVersion
	return Report{
		SchemaVersion: SchemaVersion,
		MatrixVersion: MatrixVersion,
		Date:          date,
		Scope:         "Dart fixtures and local Atenea contracts; no external provider or client",
		ProviderBase: ProviderBase{
			Name:       "Kivgraph",
			Revision:   KivgraphRevision,
			Repository: "github.com/Tutitoos/kivgraph",
			Evidence:   "verified upstream main revision; Atenea consumes the native MCP tool and does not duplicate its graph implementation",
		},
		Attempts: []ProviderAttempt{{
			ID:            "kivgraph-dart-cli-2026-09-06",
			Provider:      "Kivgraph",
			Revision:      KivgraphRevision,
			Command:       "kivgraph index --full",
			Status:        "failed",
			EvidenceLevel: Pending,
			Date:          date,
			Error:         "LadybugDB native support is unavailable; no generation was published",
		}},
		Rows: []Row{
			{
				Capability: CapabilityCodeContext, Language: "dart", Declared: Proven, Connected: Unknown,
				FunctionallyTested: Proven, EvidenceLevel: Local, Date: date, Version: version,
				Scope:       "one explicit Dart repository fixture through the local code.context contract",
				Evidence:    []Evidence{{ID: "dart-runner-local-session", Source: "internal/dartcoverage/report.go", Level: Local, Detail: "the productive code.context Runner route completed against a deterministic local Session response"}},
				Limitations: []string{"No live Kivgraph provider, MCP connection, or client chat was used; connected remains unknown; real Dart source interpretation was not tested."},
			},
			{
				Capability: CapabilityWorkspace, Language: "dart", Declared: Proven, Connected: Unknown,
				FunctionallyTested: Proven, EvidenceLevel: Local, Date: date, Version: version,
				Scope:       "two independent Dart child repository fixtures coordinated locally",
				Evidence:    []Evidence{{ID: "dart-workspace-coordinator", Source: "internal/dartcoverage/report.go", Level: Local, Detail: "the productive workspace.context Coordinator dispatched two child repository identities with deterministic local Session responses"}},
				Limitations: []string{"The fan-out responses are simulated local Session evidence; provider, client, cross-process presentation, and real Dart source interpretation remain unknown."},
			},
			{
				Capability: CapabilityImplementations, Language: "dart", Declared: Unsupported, Connected: Unknown,
				FunctionallyTested: Unsupported, EvidenceLevel: Local, Date: date, Version: version,
				Scope:       "Dart request rejected during Atenea preflight before a Kivgraph session/provider call",
				Evidence:    []Evidence{{ID: "p13-dart-preflight", Source: "internal/dartcoverage/report.go", Level: Local, Detail: "the productive symbol.implementations preflight rejects .dart before creating a provider session"}},
				Limitations: []string{"Dart semantic implementations are intentionally not enabled; no Dart locations or source interpretation are claimed."},
			},
		},
		Components: []Component{
			{
				ID: "kivgraph-dart-loader", Name: "Kivgraph Dart loader", Status: Proven,
				EvidenceLevel: ComponentLocal, Date: date, Version: KivgraphRevision,
				Scope:       "Kivgraph component loader and direct tests, separate from Atenea E2E",
				Evidence:    []Evidence{{ID: "kivgraph-dart-loader-tests", Source: "kivgraph/internal/dartloader/loader_test.go", Level: ComponentLocal, Detail: "component tests observed Kivgraph generating Dart IMPLEMENTS/OVERRIDES evidence at the pinned revision; no published generation or Atenea E2E is claimed"}},
				Limitations: []string{"Component-local evidence does not prove an Atenea provider connection, client presentation, or Dart symbol.implementations support"},
			},
		},
		Limitations: []string{
			"real_provider and real_client evidence are pending; a real Kivgraph CLI attempt reached index --full but failed because LadybugDB native support was unavailable, so no generation was published",
			"Dart source interpretation was not tested; local Session responses prove routing and policy only",
			"S03 and S07 are revalidated from the fixed corpus separately and its SHA is never changed by this report",
		},
	}
}

// Run validates independent fixtures and revalidates S03/S07 from the fixed
// corpus. It makes no provider, client, or network calls.
func Run(options Options) (Report, error) {
	if strings.TrimSpace(options.FixtureRoot) == "" || strings.TrimSpace(options.CorpusRoot) == "" {
		return Report{}, errors.New("fixture and corpus roots are required")
	}
	date := options.Date
	if date == "" {
		date = "2026-09-06"
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return Report{}, fmt.Errorf("invalid observation date: %w", err)
	}
	fixtureHash, dartFiles, err := validateFixtures(options.FixtureRoot)
	if err != nil {
		return Report{}, err
	}
	if len(dartFiles) < 2 {
		return Report{}, errors.New("dart workspace fixture requires at least two child repositories")
	}
	if err := runProductiveLocalChecks(options.FixtureRoot, dartFiles); err != nil {
		return Report{}, fmt.Errorf("run local Dart product checks: %w", err)
	}
	baseline, corpusHash, err := corpus.RevalidateDartBaseline(options.CorpusRoot, options.CorpusSHA256)
	if err != nil {
		return Report{}, fmt.Errorf("revalidate S03/S07: %w", err)
	}
	for _, result := range baseline {
		if !result.Valid || (result.ID == "S03" && result.State != corpus.Passed) || (result.ID == "S07" && result.State != corpus.Unsupported) {
			return Report{}, fmt.Errorf("fixed Dart baseline %s is not accepted: %s", result.ID, result.Evidence)
		}
	}
	report := DefaultReport()
	report.Date = date
	report.FixtureSHA256 = fixtureHash
	report.CorpusSHA256 = corpusHash
	report.Baseline = make([]Baseline, 0, len(baseline))
	for _, result := range baseline {
		report.Baseline = append(report.Baseline, Baseline{ID: result.ID, Preflight: string(result.Preflight), State: string(result.State), Valid: result.Valid, Evidence: result.Evidence})
	}
	for index := range report.Rows {
		report.Rows[index].Date = date
		switch report.Rows[index].Capability {
		case CapabilityCodeContext:
			report.Rows[index].Evidence[0].Detail = fmt.Sprintf("%s; fixture set contains %d independent Dart files", report.Rows[index].Evidence[0].Detail, len(dartFiles))
		case CapabilityWorkspace:
			report.Rows[index].Evidence[0].Detail = fmt.Sprintf("%s; two child identities were dispatched from a %d-file fixture set", report.Rows[index].Evidence[0].Detail, len(dartFiles))
		}
	}
	for index := range report.Components {
		report.Components[index].Date = date
	}
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func validateFixtures(root string) (string, []string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", nil, fmt.Errorf("dart fixture root is not a directory: %s", filepath.Base(absolute))
	}
	var paths []string
	contents := map[string][]byte{}
	err = filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("dart fixture symlinks are not allowed")
		}
		if filepath.Ext(path) != ".dart" {
			return nil
		}
		rel, err := filepath.Rel(absolute, path)
		if err != nil || filepath.IsAbs(rel) || strings.HasPrefix(filepath.Clean(rel), ".."+string(filepath.Separator)) {
			return errors.New("dart fixture escaped its root")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		paths = append(paths, rel)
		contents[rel] = data
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(contents[path])
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), paths, nil
}

func runProductiveLocalChecks(root string, files []string) error {
	sort.Strings(files)
	if _, err := runLocalCodeContext(root, "dart-primary"); err != nil {
		return fmt.Errorf("code.context: %w", err)
	}
	coordinator, err := workspacecontext.New(workspacecontext.Config{MaxParallel: 2})
	if err != nil {
		return err
	}
	targets := make([]workspacecontext.Target, 0, 2)
	for index, relative := range files[:2] {
		childRoot := root
		if strings.Contains(filepath.ToSlash(relative), "/") {
			childRoot = filepath.Join(root, filepath.Dir(filepath.FromSlash(relative)))
		}
		id := fmt.Sprintf("dart-%d", index+1)
		repo := contract.NewRepository(id, childRoot, []string{"dart"}, contract.ScaleSmall, contract.VCSUnspecified, nil)
		targets = append(targets, workspacecontext.Target{ID: id, Root: childRoot, Repository: repo})
	}
	result, err := coordinator.Run(context.Background(), workspacecontext.Request{
		Targets: targets, Payload: map[string]any{"task": "find catalog", "limit": 1},
		Permission: contract.Permission{Task: workspacecontext.Capability, Effects: []contract.Effect{contract.EffectRead}},
	}, nil, func(ctx context.Context, child workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		out, err := runLocalCodeContext(child.Repository.Path, child.Repository.ID)
		if err != nil {
			return workspacecontext.ChildResult{}, err
		}
		return workspacecontext.ChildResult{Result: out.Result, Provider: "kivgraph", Implementation: kivgraph.ImplContext}, nil
	})
	if err != nil {
		return fmt.Errorf("workspace.context: %w", err)
	}
	if len(result.Repositories) != 2 {
		return fmt.Errorf("workspace.context returned %d repositories, want 2", len(result.Repositories))
	}
	for _, repository := range result.Repositories {
		if !repository.Started || repository.Error != "" || repository.Result == nil || repository.Provider == "" || repository.Implementation == "" {
			return fmt.Errorf("workspace.context child %s was not a successful product result: %+v", repository.ID, repository)
		}
	}
	return verifyDartImplementationPreflight()
}

func localFixtureName(root string) string {
	if filepath.Base(root) == "workspace" {
		return "secondary.dart"
	}
	return "repository.dart"
}

func runLocalCodeContext(root, repositoryID string) (contract.Outcome, error) {
	var sessionCreated atomic.Int32
	runner, err := kivgraph.New(kivgraph.Options{Timeout: time.Second, Session: func(context.Context) (kivgraph.Session, error) {
		sessionCreated.Add(1)
		return &localSession{repositoryID: repositoryID, root: root, file: localFixtureName(root)}, nil
	}})
	if err != nil {
		return contract.Outcome{}, err
	}
	request := contract.RunRequest{
		Capability:     localContextCapability(),
		Implementation: contract.Implementation{ID: kivgraph.ImplContext, Provider: "kivgraph", Capability: kivgraph.CapabilityContext},
		Repository:     contract.NewRepository(repositoryID, root, []string{"dart"}, contract.ScaleSmall, contract.VCSUnspecified, nil),
		Payload:        map[string]any{"task": "find catalog", "limit": 1},
		Permission:     contract.Permission{Task: "code.context", Effects: []contract.Effect{contract.EffectRead}},
	}
	out, err := runner.Run(context.Background(), request)
	if err != nil {
		return contract.Outcome{}, err
	}
	if sessionCreated.Load() == 0 || out.Result == nil {
		return contract.Outcome{}, errors.New("local code.context did not execute a provider-shaped runner")
	}
	return out, nil
}

func verifyDartImplementationPreflight() error {
	var sessionCreated atomic.Int32
	runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) {
		sessionCreated.Add(1)
		return &localSession{}, nil
	}})
	if err != nil {
		return err
	}
	capability := contract.Capability{ID: kivgraph.CapabilityImplementations, Version: contract.Version{Major: 1}, Inputs: []contract.Field{
		{Name: "file", Type: contract.TypeString, Required: true}, {Name: "line", Type: contract.TypeInt, Required: true}, {Name: "column", Type: contract.TypeInt, Required: true},
	}, Effects: []contract.Effect{contract.EffectRead}}
	_, runErr := runner.Run(context.Background(), contract.RunRequest{
		Capability:     capability,
		Implementation: contract.Implementation{ID: kivgraph.ImplImplementations, Provider: "kivgraph", Capability: kivgraph.CapabilityImplementations},
		Repository:     contract.NewRepository("dart-preflight", ".", []string{"dart"}, contract.ScaleSmall, contract.VCSUnspecified, nil),
		Payload:        map[string]any{"file": "main.dart", "line": 1, "column": 1},
		Permission:     contract.Permission{Task: "symbol.implementations", Effects: []contract.Effect{contract.EffectRead}},
	})
	if runErr == nil {
		return errors.New("dart symbol.implementations preflight unexpectedly succeeded")
	}
	if sessionCreated.Load() != 0 {
		return fmt.Errorf("dart semantic preflight created %d provider sessions", sessionCreated.Load())
	}
	return nil
}

func localContextCapability() contract.Capability {
	return contract.Capability{ID: kivgraph.CapabilityContext, Version: contract.Version{Major: 1}, Inputs: []contract.Field{
		{Name: "task", Type: contract.TypeString, Required: true}, {Name: "limit", Type: contract.TypeInt},
	}, Outputs: []contract.Field{
		{Name: "symbols", Type: contract.TypeRecordList, Required: true, Fields: []contract.Field{
			{Name: "name", Type: contract.TypeString, Required: true}, {Name: "kind", Type: contract.TypeString, Required: true},
			{Name: "path", Type: contract.TypeString, Required: true}, {Name: "line", Type: contract.TypeInt, Required: true},
		}},
		{Name: "truncated", Type: contract.TypeBool}, {Name: "next_cursor", Type: contract.TypeString}, {Name: "source_trimmed", Type: contract.TypeInt},
	}}
}

type localSession struct {
	repositoryID string
	root         string
	file         string
}

func (s *localSession) Call(_ context.Context, tool string, args map[string]any) (string, error) {
	switch tool {
	case "graph_status":
		payload := map[string]any{"results": map[string]any{
			"status": "ready", "snapshot_id": 1, "symbols": 1, "edges": 1, "files": 1, "repositories": 1,
			"content_freshness":    map[string]any{"generation": 1, "state": "fresh"},
			"repository_freshness": []map[string]any{{"name": s.repositoryID, "path": s.root}},
		}}
		return marshalLocal(payload)
	case "find_by_intent":
		repository := s.repositoryID
		if value, ok := args["repo"].(string); ok && value != "" {
			repository = value
		}
		payload := map[string]any{"truncated": false, "next_cursor": "", "coverage": map[string]any{"exact": 1, "candidate": 0, "unresolved_related": 0, "package_level": 0}, "completeness": map[string]any{"verdict": "COMPLETE"}, "results": map[string]any{"symbols": []map[string]any{{"qualified_name": "Catalog.lookup", "kind": "method", "repository": repository, "file_path": s.file, "start_line": 1, "end_line": 3, "terms": 1, "match": "local"}}}}
		return marshalLocal(payload)
	default:
		return "", fmt.Errorf("unexpected local Kivgraph tool %q", tool)
	}
}

func marshalLocal(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}

// Canonical is part of ATENEA's public orchestration contract.
func (r Report) Canonical() Report {
	out := r
	out.Rows = append([]Row(nil), r.Rows...)
	for i := range out.Rows {
		out.Rows[i].Evidence = append([]Evidence(nil), out.Rows[i].Evidence...)
		out.Rows[i].Limitations = append([]string(nil), out.Rows[i].Limitations...)
		sort.Slice(out.Rows[i].Evidence, func(a, b int) bool { return out.Rows[i].Evidence[a].ID < out.Rows[i].Evidence[b].ID })
		sort.Strings(out.Rows[i].Limitations)
	}
	sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].Capability < out.Rows[j].Capability })
	out.Components = append([]Component(nil), r.Components...)
	for i := range out.Components {
		out.Components[i].Evidence = append([]Evidence(nil), out.Components[i].Evidence...)
		out.Components[i].Limitations = append([]string(nil), out.Components[i].Limitations...)
		sort.Slice(out.Components[i].Evidence, func(a, b int) bool { return out.Components[i].Evidence[a].ID < out.Components[i].Evidence[b].ID })
		sort.Strings(out.Components[i].Limitations)
	}
	sort.Slice(out.Components, func(i, j int) bool { return out.Components[i].ID < out.Components[j].ID })
	out.Attempts = append([]ProviderAttempt(nil), r.Attempts...)
	sort.Slice(out.Attempts, func(i, j int) bool { return out.Attempts[i].ID < out.Attempts[j].ID })
	out.Limitations = append([]string(nil), r.Limitations...)
	sort.Strings(out.Limitations)
	return out
}

// Validate is part of ATENEA's public orchestration contract.
func (r Report) Validate() error {
	if r.SchemaVersion != SchemaVersion || r.MatrixVersion != MatrixVersion {
		return fmt.Errorf("unsupported Dart coverage schema/version: %d/%q", r.SchemaVersion, r.MatrixVersion)
	}
	if _, err := time.Parse("2006-01-02", r.Date); err != nil {
		return fmt.Errorf("date must be YYYY-MM-DD: %w", err)
	}
	if strings.TrimSpace(r.Scope) == "" || len(r.Limitations) == 0 {
		return errors.New("report scope and limitations are required")
	}
	if strings.TrimSpace(r.ProviderBase.Name) == "" || strings.TrimSpace(r.ProviderBase.Revision) == "" || strings.TrimSpace(r.ProviderBase.Repository) == "" || strings.TrimSpace(r.ProviderBase.Evidence) == "" {
		return errors.New("provider baseline metadata is required")
	}
	if len(r.Attempts) == 0 {
		return errors.New("provider attempt evidence is required")
	}
	attemptIDs := map[string]bool{}
	for _, attempt := range r.Attempts {
		if strings.TrimSpace(attempt.ID) == "" || attemptIDs[attempt.ID] || attempt.Provider != r.ProviderBase.Name || attempt.Revision != r.ProviderBase.Revision || strings.TrimSpace(attempt.Command) == "" || attempt.Status != "failed" || attempt.EvidenceLevel != Pending || attempt.Date != r.Date || strings.TrimSpace(attempt.Error) == "" {
			return fmt.Errorf("invalid provider attempt %q", attempt.ID)
		}
		attemptIDs[attempt.ID] = true
	}
	wanted := map[string]bool{CapabilityCodeContext: true, CapabilityWorkspace: true, CapabilityImplementations: true}
	seen := map[string]bool{}
	for _, row := range r.Rows {
		if !wanted[row.Capability] || seen[row.Capability] {
			return fmt.Errorf("row capability is missing, unknown, or duplicated: %q", row.Capability)
		}
		seen[row.Capability] = true
		if row.Language != "dart" || row.Date != r.Date || strings.TrimSpace(row.Version) == "" || strings.TrimSpace(row.Scope) == "" || len(row.Evidence) == 0 || len(row.Limitations) == 0 {
			return fmt.Errorf("row %q has incomplete identity, scope, evidence, or limitations", row.Capability)
		}
		if !validStatus(row.Declared) || !validStatus(row.Connected) || !validStatus(row.FunctionallyTested) || !validLevel(row.EvidenceLevel) {
			return fmt.Errorf("row %q has an unknown status or evidence level", row.Capability)
		}
		if row.Connected == Proven && row.EvidenceLevel != RealProvider && row.EvidenceLevel != RealClient {
			return fmt.Errorf("row %q claims a connection without real provider/client evidence", row.Capability)
		}
		if row.FunctionallyTested == Proven && row.EvidenceLevel == Pending {
			return fmt.Errorf("row %q claims a pending functional result", row.Capability)
		}
		ids := map[string]bool{}
		for _, evidence := range row.Evidence {
			if strings.TrimSpace(evidence.ID) == "" || strings.TrimSpace(evidence.Source) == "" || strings.TrimSpace(evidence.Detail) == "" || !validLevel(evidence.Level) || ids[evidence.ID] {
				return fmt.Errorf("row %q has invalid evidence reference", row.Capability)
			}
			ids[evidence.ID] = true
		}
		if row.Capability == CapabilityImplementations && (row.Declared != Unsupported || row.FunctionallyTested != Unsupported) {
			return errors.New("dart symbol.implementations must remain unsupported")
		}
	}
	if len(seen) != len(wanted) {
		return fmt.Errorf("report has %d rows, want %d", len(seen), len(wanted))
	}
	componentIDs := map[string]bool{}
	for _, component := range r.Components {
		if component.ID == "" || componentIDs[component.ID] || component.Status != Proven || component.EvidenceLevel != ComponentLocal || component.Date != r.Date || component.Version == "" || component.Scope == "" || len(component.Evidence) == 0 || len(component.Limitations) == 0 {
			return fmt.Errorf("invalid component evidence %q", component.ID)
		}
		componentIDs[component.ID] = true
		for _, evidence := range component.Evidence {
			if evidence.Level != ComponentLocal || evidence.ID == "" || evidence.Source == "" || evidence.Detail == "" {
				return fmt.Errorf("component %q has invalid evidence", component.ID)
			}
		}
	}
	if len(componentIDs) == 0 {
		return errors.New("component evidence is required")
	}
	return nil
}

func validStatus(status Status) bool {
	return status == Proven || status == Unsupported || status == Unknown
}
func validLevel(level EvidenceLevel) bool {
	return level == Local || level == ComponentLocal || level == RealProvider || level == RealClient || level == Pending
}

// JSON is part of ATENEA's public orchestration contract.
func (r Report) JSON() ([]byte, error) {
	canonical := r.Canonical()
	if err := canonical.Validate(); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(canonical); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Markdown is part of ATENEA's public orchestration contract.
func (r Report) Markdown() (string, error) {
	canonical := r.Canonical()
	if err := canonical.Validate(); err != nil {
		return "", err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Dart capability coverage\n\nVersion: `%s`  \nDate: `%s`  \nScope: %s\n\n", canonical.MatrixVersion, canonical.Date, canonical.Scope)
	fmt.Fprintf(&out, "Provider baseline: **%s** at `%s` (%s). %s.\n\n", canonical.ProviderBase.Name, canonical.ProviderBase.Revision, canonical.ProviderBase.Repository, canonical.ProviderBase.Evidence)
	out.WriteString("| Capability | Language | Declared | Connected | Functionally tested | Evidence | Scope |\n|---|---|---|---|---|---|---|\n")
	for _, row := range canonical.Rows {
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s | %s | %s |\n", row.Capability, row.Language, row.Declared, row.Connected, row.FunctionallyTested, row.EvidenceLevel, row.Scope)
	}
	out.WriteString("\n## Provider attempts\n\n")
	for _, attempt := range canonical.Attempts {
		fmt.Fprintf(&out, "- **%s**: `%s` via `%s`, evidence `%s`; %s.\n", attempt.Provider, attempt.Status, attempt.Command, attempt.EvidenceLevel, attempt.Error)
	}
	out.WriteString("\n## Component evidence\n\n")
	for _, component := range canonical.Components {
		fmt.Fprintf(&out, "- **%s**: `%s`, evidence `%s`; %s.\n", component.Name, component.Status, component.EvidenceLevel, component.Limitations[0])
	}
	out.WriteString("\n## Limitations\n\n")
	for _, limitation := range canonical.Limitations {
		fmt.Fprintf(&out, "- %s\n", limitation)
	}
	return out.String(), nil
}
