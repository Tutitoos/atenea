// Command atenea-laya-eval compares dry-run decision plans on a private corpus.
// It never executes a workflow or writes commission text to its metrics report.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/decision"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type sample struct {
	ID             string                  `json:"id"`
	Text           string                  `json:"text"`
	Split          string                  `json:"split"`
	Context        *decision.IntentContext `json:"context,omitempty"`
	Files          []string                `json:"files,omitempty"`
	GrantedEffects []contract.Effect       `json:"granted_effects,omitempty"`
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var safeEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type label struct {
	ID                 string                    `json:"id"`
	Expected           decision.Kind             `json:"expected,omitempty"`
	ExpectedResolution decision.ResolutionStatus `json:"expected_resolution,omitempty"`
}

type planView struct {
	Resolution       decision.ResolutionStatus `json:"resolution"`
	ResolutionReason string                    `json:"resolution_reason,omitempty"`
	ContextUsed      bool                      `json:"context_used,omitempty"`
	Intent           decision.Kind             `json:"intent,omitempty"`
	Agent            string                    `json:"agent,omitempty"`
	Specialists      []string                  `json:"specialists,omitempty"`
	Steps            []stepView                `json:"steps,omitempty"`
	Effects          []contract.Effect         `json:"effects,omitempty"`
	Models           []string                  `json:"models,omitempty"`
	Tools            []string                  `json:"tools,omitempty"`
	BudgetUSD        float64                   `json:"budget_required_usd,omitempty"`
	Valid            bool                      `json:"valid"`
}

type stepView struct {
	Type    string            `json:"type"`
	Effects []contract.Effect `json:"effects"`
}

type result struct {
	ID                     string                    `json:"id"`
	Split                  string                    `json:"split"`
	HasContext             bool                      `json:"has_context"`
	Expected               decision.Kind             `json:"expected,omitempty"`
	ExpectedResolution     decision.ResolutionStatus `json:"expected_resolution,omitempty"`
	Rules                  decision.Kind             `json:"rules,omitempty"`
	Laya                   decision.Kind             `json:"laya,omitempty"`
	Gated                  decision.Kind             `json:"gated,omitempty"`
	Context                decision.Kind             `json:"context_intent,omitempty"`
	ContextLaya            decision.Kind             `json:"context_laya,omitempty"`
	ContextGated           decision.Kind             `json:"context_gated,omitempty"`
	Confidence             float64                   `json:"answer_confidence,omitempty"`
	ContextConfidence      float64                   `json:"context_answer_confidence,omitempty"`
	Model                  string                    `json:"model,omitempty"`
	RoutingModel           string                    `json:"routing_model,omitempty"`
	ContextModel           string                    `json:"context_model,omitempty"`
	ContextRoutingModel    string                    `json:"context_routing_model,omitempty"`
	Fallback               string                    `json:"fallback,omitempty"`
	ContextFallback        string                    `json:"context_fallback,omitempty"`
	LayaDisposition        string                    `json:"laya_disposition"`
	ContextLayaDisposition string                    `json:"context_laya_disposition"`
	ObserveMS              int64                     `json:"observe_duration_ms"`
	ContextObserveMS       int64                     `json:"context_observe_duration_ms,omitempty"`
	RulesPlan              planView                  `json:"rules_plan"`
	GatedPlan              planView                  `json:"gated_plan"`
	ContextPlan            planView                  `json:"context_plan"`
	ContextGatedPlan       planView                  `json:"context_gated_plan"`
	Safe                   bool                      `json:"safe"`
	BlindedA               string                    `json:"blinded_a"`
	BlindedB               string                    `json:"blinded_b"`
}

type splitMetrics struct {
	Count                   int            `json:"count"`
	ContextCount            int            `json:"context_count"`
	ContextLabeled          int            `json:"context_labeled"`
	RulesCorrect            int            `json:"rules_correct"`
	LayaCorrect             int            `json:"laya_correct"`
	GatedCorrect            int            `json:"gated_correct"`
	ContextCorrect          int            `json:"context_correct"`
	ContextGatedCorrect     int            `json:"context_gated_correct"`
	LayaCovered             int            `json:"laya_covered"`
	ContextLayaCovered      int            `json:"context_laya_covered"`
	LayaLabeled             int            `json:"laya_labeled"`
	ContextLayaCorrect      int            `json:"context_laya_correct"`
	ContextLayaLabeled      int            `json:"context_laya_labeled"`
	GatedWins               int            `json:"gated_wins"`
	GatedLosses             int            `json:"gated_losses"`
	ContextGatedWins        int            `json:"context_gated_wins"`
	ContextGatedLosses      int            `json:"context_gated_losses"`
	UnsafePlans             int            `json:"unsafe_plans"`
	FalseChangeRules        int            `json:"false_change_rules"`
	FalseChangeLaya         int            `json:"false_change_laya"`
	FalseChangeGated        int            `json:"false_change_gated"`
	FalseChangeContext      int            `json:"false_change_context"`
	FalseChangeContextGated int            `json:"false_change_context_gated"`
	ConfusionRules          map[string]int `json:"confusion_rules"`
	ConfusionLaya           map[string]int `json:"confusion_laya"`
	ConfusionGated          map[string]int `json:"confusion_gated"`
	ConfusionContext        map[string]int `json:"confusion_context"`
	ConfusionContextLaya    map[string]int `json:"confusion_context_laya"`
	ConfusionContextGated   map[string]int `json:"confusion_context_gated"`
}

type report struct {
	SchemaVersion                  int                      `json:"schema_version"`
	Evidence                       string                   `json:"evidence"`
	CorpusSHA256                   string                   `json:"corpus_sha256"`
	LabelsSHA256                   string                   `json:"labels_sha256,omitempty"`
	SettingsSHA256                 string                   `json:"settings_sha256"`
	RequestedModel                 string                   `json:"requested_model,omitempty"`
	Threshold                      float64                  `json:"threshold"`
	Count                          int                      `json:"count"`
	Labeled                        int                      `json:"labeled"`
	ServiceResponses               int                      `json:"service_responses"`
	ServiceFailures                int                      `json:"service_failures"`
	ServiceIntentionalSkips        int                      `json:"service_intentional_skips"`
	ContextServiceResponses        int                      `json:"context_service_responses"`
	ContextServiceFailures         int                      `json:"context_service_failures"`
	ContextServiceIntentionalSkips int                      `json:"context_service_intentional_skips"`
	ContextCases                   int                      `json:"context_cases"`
	Models                         []string                 `json:"models"`
	RoutingModels                  []string                 `json:"routing_models"`
	BySplit                        map[string]*splitMetrics `json:"by_split,omitempty"`
	Cases                          []result                 `json:"cases"`
}

type reviewItem struct {
	ID       string                  `json:"id"`
	Text     string                  `json:"text"`
	Split    string                  `json:"split"`
	Context  *decision.IntentContext `json:"context,omitempty"`
	OptionA  reviewPlanView          `json:"option_a"`
	OptionB  reviewPlanView          `json:"option_b"`
	Decision string                  `json:"preferred_option"`
	Reason   string                  `json:"reason"`
}

type reviewPlanView struct {
	Summary      planView `json:"summary"`
	Repositories []string `json:"repositories,omitempty"`
	ScopeFiles   []string `json:"scope_files,omitempty"`
}

type fixedClassifier struct {
	classification decision.IntentClassification
	err            error
}

func (f fixedClassifier) Classify(context.Context, string) (decision.IntentClassification, error) {
	return f.classification, f.err
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("atenea-laya-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	casesPath := flags.String("cases", "", "private JSONL with id, text and split")
	labelsPath := flags.String("labels", "", "separate JSONL with id and expected intent")
	settingsPath := flags.String("settings", "", "required ATENEA settings path with configured model roles")
	repository := flags.String("repository", "", "declared repository ID; empty uses first declared repository")
	endpoint := flags.String("endpoint", "", "Laya /v1/systemone endpoint")
	model := flags.String("model", "", "pin english, multilingual or typed-decisions; empty uses Laya routing")
	apiKeyEnv := flags.String("api-key-env", "", "environment variable containing the Laya bearer token")
	expectedRoute := flags.String("expected-routing-model", "", "require this routing model for every response")
	threshold := flags.Float64("threshold", 0.8, "minimum answer_confidence")
	timeout := flags.Duration("timeout", 10*time.Second, "per-request Laya timeout")
	reportPath := flags.String("report", "", "private metrics JSON output; empty writes to stdout")
	packetPath := flags.String("review-packet", "", "private blinded review JSONL output")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *casesPath == "" || *settingsPath == "" || *endpoint == "" || *threshold <= 0 || *threshold > 1 ||
		math.IsNaN(*threshold) || math.IsInf(*threshold, 0) || *timeout <= 0 || *timeout > time.Minute {
		return errors.New("usage: atenea-laya-eval --cases FILE --settings FILE --endpoint URL [--labels FILE] [--threshold 0.8] [--report FILE] [--review-packet FILE]")
	}
	if *model != "" && *model != "english" && *model != "multilingual" && *model != "typed-decisions" {
		return errors.New("--model must be english, multilingual or typed-decisions")
	}
	if err := validateEndpoint(*endpoint); err != nil {
		return err
	}
	if *apiKeyEnv != "" && !safeEnvName.MatchString(*apiKeyEnv) {
		return errors.New("--api-key-env must name an environment variable")
	}
	if *reportPath != "" && (*reportPath == *casesPath || *reportPath == *labelsPath || *reportPath == *packetPath) {
		return errors.New("report path must differ from inputs and review packet")
	}
	if *packetPath != "" && (*packetPath == *casesPath || *packetPath == *labelsPath) {
		return errors.New("review packet path must differ from inputs")
	}
	cases, err := readCases(*casesPath)
	if err != nil {
		return err
	}
	labels, err := readLabels(*labelsPath, cases)
	if err != nil {
		return err
	}
	corpusHash, err := hashFile(*casesPath)
	if err != nil {
		return err
	}
	labelsHash := ""
	if *labelsPath != "" {
		labelsHash, err = hashFile(*labelsPath)
		if err != nil {
			return err
		}
	}
	settingsHash, err := hashFile(*settingsPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(*settingsPath)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if *repository == "" {
		if len(cfg.Repositories) == 0 {
			return errors.New("settings declare no repository")
		}
		*repository = cfg.Repositories[0].ID
	}
	cfg.Decision = config.DecisionSettings{Mode: "observe", LayaEndpoint: *endpoint, LayaModel: *model, LayaAPIKeyEnv: *apiKeyEnv,
		Timeout: *timeout, MinimumConfidence: *threshold}
	observing := decision.Planner{Config: cfg}
	rep := report{SchemaVersion: 2, Evidence: "paired local dry-run plans with and without caller-supplied context; model identity must be verified separately",
		CorpusSHA256: corpusHash, LabelsSHA256: labelsHash, SettingsSHA256: settingsHash,
		RequestedModel: *model, Threshold: *threshold, Count: len(cases),
		BySplit: map[string]*splitMetrics{}, Cases: make([]result, 0, len(cases))}
	packet := make([]reviewItem, 0, len(cases))
	models := map[string]bool{}
	routes := map[string]bool{}
	for _, row := range cases {
		req := decision.Request{Text: row.Text, Repository: *repository, Files: row.Files, BudgetUSD: 10, Effects: row.GrantedEffects}
		start := time.Now()
		rulesPlan, err := observing.BuildContext(context.Background(), req)
		if err != nil {
			return fmt.Errorf("case %s: rules/observe plan: %w", row.ID, err)
		}
		observeMS := time.Since(start).Milliseconds()
		gatedPlan, err := buildGatedPlan(cfg, req, rulesPlan)
		if err != nil {
			return fmt.Errorf("case %s: gated plan: %w", row.ID, err)
		}

		contextPlan, contextGatedPlan := rulesPlan, gatedPlan
		contextObserveMS := int64(0)
		contextDisposition := "not_run_no_context"
		if row.Context != nil {
			rep.ContextCases++
			contextReq := req
			contextReq.Context = row.Context
			contextStarted := time.Now()
			contextPlan, err = observing.BuildContext(context.Background(), contextReq)
			if err != nil {
				return fmt.Errorf("case %s: context/observe plan: %w", row.ID, err)
			}
			contextObserveMS = time.Since(contextStarted).Milliseconds()
			contextGatedPlan, err = buildGatedPlan(cfg, contextReq, contextPlan)
			if err != nil {
				return fmt.Errorf("case %s: context/gated plan: %w", row.ID, err)
			}
			contextDisposition = layaDisposition(contextPlan)
		}
		item := result{ID: row.ID, Split: row.Split, HasContext: row.Context != nil, Rules: rulesPlan.Intent, Gated: gatedPlan.Intent,
			Context: contextPlan.Intent, ContextGated: contextGatedPlan.Intent,
			Fallback: rulesPlan.IntentEvidence.FallbackReason, ContextFallback: contextPlan.IntentEvidence.FallbackReason,
			LayaDisposition: layaDisposition(rulesPlan), ContextLayaDisposition: contextDisposition,
			RulesPlan: view(rulesPlan), GatedPlan: view(gatedPlan), ContextPlan: view(contextPlan), ContextGatedPlan: view(contextGatedPlan),
			ObserveMS: observeMS, ContextObserveMS: contextObserveMS,
			Safe: safeEvaluationPlan(rulesPlan, row.GrantedEffects) && safeEvaluationPlan(gatedPlan, row.GrantedEffects) &&
				safeEvaluationPlan(contextPlan, row.GrantedEffects) && safeEvaluationPlan(contextGatedPlan, row.GrantedEffects)}
		accountServiceResult(&rep, rulesPlan, false)
		if row.Context != nil {
			accountServiceResult(&rep, contextPlan, true)
		}
		if proposal := rulesPlan.IntentEvidence.Laya; proposal != nil {
			item.Laya, item.Confidence, item.Model, item.RoutingModel = proposal.Intent, proposal.Confidence, proposal.Model, proposal.RoutingModel
			models[proposal.Model] = true
			routes[proposal.RoutingModel] = true
			if *expectedRoute != "" && proposal.RoutingModel != *expectedRoute {
				return fmt.Errorf("case %s: Laya routed to %q, expected %q", row.ID, proposal.RoutingModel, *expectedRoute)
			}
		}
		if proposal := contextPlan.IntentEvidence.Laya; proposal != nil && row.Context != nil {
			item.ContextLaya, item.ContextConfidence = proposal.Intent, proposal.Confidence
			item.ContextModel, item.ContextRoutingModel = proposal.Model, proposal.RoutingModel
			models[proposal.Model] = true
			routes[proposal.RoutingModel] = true
			if *expectedRoute != "" && proposal.RoutingModel != *expectedRoute {
				return fmt.Errorf("case %s: context Laya routed to %q, expected %q", row.ID, proposal.RoutingModel, *expectedRoute)
			}
		}
		if gold, ok := labels[row.ID]; ok {
			item.Expected = gold.Expected
			item.ExpectedResolution = expectedResolution(gold)
			rep.Labeled++
			updateMetrics(rep.BySplit, item, gold, *threshold)
		}
		var order [1]byte
		if _, err := rand.Read(order[:]); err != nil {
			return fmt.Errorf("case %s: randomize review order: %w", row.ID, err)
		}
		leftName, rightName := "rules", "gated"
		leftPlan, rightPlan := reviewView(rulesPlan), reviewView(gatedPlan)
		if row.Context != nil {
			leftName, rightName = "gated", "context_gated"
			leftPlan, rightPlan = reviewView(gatedPlan), reviewView(contextGatedPlan)
		}
		review := reviewItem{ID: row.ID, Text: row.Text, Split: row.Split, Context: blindContext(row.Context)}
		if order[0]&1 == 0 {
			item.BlindedA, item.BlindedB = leftName, rightName
			review.OptionA, review.OptionB = leftPlan, rightPlan
		} else {
			item.BlindedA, item.BlindedB = rightName, leftName
			review.OptionA, review.OptionB = rightPlan, leftPlan
		}
		packet = append(packet, review)
		rep.Cases = append(rep.Cases, item)
	}
	for model := range models {
		rep.Models = append(rep.Models, model)
	}
	for route := range routes {
		rep.RoutingModels = append(rep.RoutingModels, route)
	}
	sort.Strings(rep.Models)
	sort.Strings(rep.RoutingModels)
	if *packetPath != "" {
		if err := writePrivate(*packetPath, packet); err != nil {
			return err
		}
	}
	if *reportPath != "" {
		return writePrivate(*reportPath, rep)
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(rep)
}

func validateEndpoint(raw string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" ||
		endpoint.Fragment != "" || endpoint.Path != "/v1/systemone" ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return errors.New("--endpoint must be an HTTP(S) URL ending in /v1/systemone without credentials, query or fragment")
	}
	if endpoint.Scheme == "http" {
		host := strings.ToLower(endpoint.Hostname())
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("--endpoint must use HTTPS unless it targets loopback")
		}
	}
	return nil
}

func readCases(path string) ([]sample, error) {
	if path == "" {
		return nil, errors.New("--cases is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var rows []sample
	seen := map[string]bool{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		var row sample
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("case line %d: invalid JSON: %w", len(rows)+1, err)
		}
		row.ID, row.Text, row.Split = strings.TrimSpace(row.ID), strings.TrimSpace(row.Text), strings.TrimSpace(row.Split)
		if !safeID.MatchString(row.ID) || !safeID.MatchString(row.Split) || row.Text == "" || seen[row.ID] {
			return nil, fmt.Errorf("case line %d: id and split must be short opaque tokens, text nonempty, and id unique", len(rows)+1)
		}
		for _, effect := range row.GrantedEffects {
			if effect != contract.EffectWrite {
				return nil, fmt.Errorf("case %s: evaluation only accepts an explicit write grant", row.ID)
			}
		}
		seen[row.ID] = true
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("case corpus is empty")
	}
	return rows, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func readLabels(path string, cases []sample) (map[string]label, error) {
	labels := map[string]label{}
	if path == "" {
		return labels, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	known := map[string]bool{}
	for _, row := range cases {
		known[row.ID] = true
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		var row label
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("invalid label JSON: %w", err)
		}
		resolution := expectedResolution(row)
		validResolution := resolution == decision.ResolutionResolved || resolution == decision.ResolutionNeedsContext
		validExpected := (resolution == decision.ResolutionResolved && validIntent(row.Expected)) ||
			(resolution == decision.ResolutionNeedsContext && row.Expected == "")
		if !known[row.ID] || labels[row.ID].ID != "" || !validResolution || !validExpected {
			return nil, fmt.Errorf("label for %q is unknown, repeated or invalid", row.ID)
		}
		labels[row.ID] = row
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(labels) != len(cases) {
		return nil, fmt.Errorf("labels cover %d of %d cases", len(labels), len(cases))
	}
	return labels, nil
}

func validIntent(value decision.Kind) bool {
	switch value {
	case decision.KindUnderstand, decision.KindSearch, decision.KindPlan, decision.KindChange:
		return true
	}
	return false
}

func view(plan decision.Plan) planView {
	out := planView{Resolution: plan.Resolution, ResolutionReason: plan.ResolutionReason, ContextUsed: plan.ContextUsed,
		Intent: plan.Intent, Agent: plan.Agent, Specialists: plan.Specialists,
		Effects: plan.Effects, BudgetUSD: plan.Budget.RequiredUSD, Valid: plan.Valid}
	for _, step := range plan.Workflow.Steps {
		out.Steps = append(out.Steps, stepView{Type: step.TypeName, Effects: step.Permission.Effects})
	}
	for _, model := range plan.Models {
		out.Models = append(out.Models, model.Role+":"+model.Name)
	}
	for _, tool := range plan.Tools {
		if tool.Selected {
			out.Tools = append(out.Tools, tool.ID)
		}
	}
	return out
}

func blindContext(context *decision.IntentContext) *decision.IntentContext {
	if context == nil {
		return nil
	}
	redacted := *context
	redacted.ScopeFiles = append([]string(nil), context.ScopeFiles...)
	redacted.Constraints = append([]string(nil), context.Constraints...)
	if redacted.Repository != "" {
		redacted.Repository = "repository"
	}
	if redacted.AcceptedPlanID != "" {
		redacted.AcceptedPlanID = "accepted-plan"
		redacted.AcceptedPlanRevision = "current"
	}
	return &redacted
}

func reviewView(plan decision.Plan) reviewPlanView {
	summary := view(plan)
	for i, model := range plan.Models {
		availability := "unavailable"
		if model.Available {
			availability = "available"
		}
		summary.Models[i] = model.Role + ":" + availability
	}

	var repositories []string
	if len(plan.Repositories) > 0 {
		repositories = []string{"repository"}
	}
	fileSet := map[string]bool{}
	for _, step := range plan.Workflow.Steps {
		for _, file := range step.Task.Files {
			if file != "" {
				fileSet[file] = true
			}
		}
	}
	files := make([]string, 0, len(fileSet))
	for file := range fileSet {
		files = append(files, file)
	}
	sort.Strings(files)
	return reviewPlanView{Summary: summary, Repositories: repositories, ScopeFiles: files}
}

func safePlan(plan decision.Plan, granted []contract.Effect) bool {
	allowed := map[contract.Effect]bool{contract.EffectRead: true}
	for _, effect := range granted {
		allowed[effect] = true
	}
	for _, effect := range plan.Effects {
		if !allowed[effect] {
			return false
		}
	}
	for _, step := range plan.Workflow.Steps {
		for _, effect := range step.Permission.Effects {
			if !allowed[effect] {
				return false
			}
		}
	}
	return true
}

func safeEvaluationPlan(plan decision.Plan, granted []contract.Effect) bool {
	if plan.Resolution == decision.ResolutionNeedsContext {
		return !plan.Valid && len(plan.Workflow.Steps) == 0 && len(plan.Effects) == 0
	}
	return plan.Resolution == decision.ResolutionResolved && plan.Valid && safePlan(plan, granted)
}

func buildGatedPlan(cfg config.Config, req decision.Request, observed decision.Plan) (decision.Plan, error) {
	selected := fixedClassifier{err: errors.New("no valid Laya proposal")}
	if observed.IntentEvidence.Laya != nil {
		selected = fixedClassifier{classification: *observed.IntentEvidence.Laya}
	}
	cfg.Decision.Mode = "laya"
	return (decision.Planner{Config: cfg, Classifier: selected}).BuildContext(context.Background(), req)
}

func layaDisposition(plan decision.Plan) string {
	if plan.IntentEvidence.Laya != nil {
		return "responded"
	}
	if plan.IntentEvidence.Source == "context" {
		return "skipped_context_resolved"
	}
	if plan.Resolution == decision.ResolutionNeedsContext {
		return "skipped_needs_context"
	}
	if plan.IntentEvidence.FallbackReason != "" {
		return "failed_fallback"
	}
	return "skipped_not_applicable"
}

func accountServiceResult(rep *report, plan decision.Plan, contextual bool) {
	switch layaDisposition(plan) {
	case "responded":
		rep.ServiceResponses++
		if contextual {
			rep.ContextServiceResponses++
		}
	case "failed_fallback":
		rep.ServiceFailures++
		if contextual {
			rep.ContextServiceFailures++
		}
	default:
		rep.ServiceIntentionalSkips++
		if contextual {
			rep.ContextServiceIntentionalSkips++
		}
	}
}

func expectedResolution(gold label) decision.ResolutionStatus {
	if gold.ExpectedResolution == "" {
		return decision.ResolutionResolved
	}
	return gold.ExpectedResolution
}

func correctPlan(plan planView, gold label) bool {
	if expectedResolution(gold) == decision.ResolutionNeedsContext {
		return plan.Resolution == decision.ResolutionNeedsContext
	}
	return plan.Resolution == decision.ResolutionResolved && plan.Intent == gold.Expected
}

func predictedOutcome(plan planView) string {
	if plan.Resolution != decision.ResolutionResolved {
		if plan.Resolution == "" {
			return "invalid_resolution"
		}
		return string(plan.Resolution)
	}
	return string(plan.Intent)
}

func goldOutcome(gold label) string {
	if expectedResolution(gold) == decision.ResolutionNeedsContext {
		return string(decision.ResolutionNeedsContext)
	}
	return string(gold.Expected)
}

func falseChange(plan planView, gold label) bool {
	return goldOutcome(gold) != string(decision.KindChange) && plan.Resolution == decision.ResolutionResolved && plan.Intent == decision.KindChange
}

func updateMetrics(splits map[string]*splitMetrics, row result, gold label, threshold float64) {
	m := splits[row.Split]
	if m == nil {
		m = &splitMetrics{ConfusionRules: map[string]int{}, ConfusionLaya: map[string]int{}, ConfusionGated: map[string]int{},
			ConfusionContext: map[string]int{}, ConfusionContextLaya: map[string]int{}, ConfusionContextGated: map[string]int{}}
		splits[row.Split] = m
	}
	m.Count++
	if correctPlan(row.RulesPlan, gold) {
		m.RulesCorrect++
	}
	if correctPlan(row.GatedPlan, gold) {
		m.GatedCorrect++
	}
	if row.LayaDisposition == "responded" && expectedResolution(gold) == decision.ResolutionResolved {
		m.LayaLabeled++
		if row.Laya == gold.Expected {
			m.LayaCorrect++
		}
		if row.Confidence >= threshold {
			m.LayaCovered++
		}
		m.ConfusionLaya[goldOutcome(gold)+"→"+string(row.Laya)]++
	}
	if !correctPlan(row.RulesPlan, gold) && correctPlan(row.GatedPlan, gold) {
		m.GatedWins++
	}
	if correctPlan(row.RulesPlan, gold) && !correctPlan(row.GatedPlan, gold) {
		m.GatedLosses++
	}
	if !row.Safe {
		m.UnsafePlans++
	}
	if falseChange(row.RulesPlan, gold) {
		m.FalseChangeRules++
	}
	if falseChange(row.GatedPlan, gold) {
		m.FalseChangeGated++
	}
	goldKey := goldOutcome(gold)
	m.ConfusionRules[goldKey+"→"+predictedOutcome(row.RulesPlan)]++
	m.ConfusionGated[goldKey+"→"+predictedOutcome(row.GatedPlan)]++
	if row.HasContext {
		m.ContextCount++
		m.ContextLabeled++
		if correctPlan(row.ContextPlan, gold) {
			m.ContextCorrect++
		}
		if correctPlan(row.ContextGatedPlan, gold) {
			m.ContextGatedCorrect++
		}
		if row.ContextLayaDisposition == "responded" && expectedResolution(gold) == decision.ResolutionResolved {
			m.ContextLayaLabeled++
			if row.ContextLaya == gold.Expected {
				m.ContextLayaCorrect++
			}
			if row.ContextConfidence >= threshold {
				m.ContextLayaCovered++
			}
			m.ConfusionContextLaya[goldKey+"→"+string(row.ContextLaya)]++
		}
		if !correctPlan(row.GatedPlan, gold) && correctPlan(row.ContextGatedPlan, gold) {
			m.ContextGatedWins++
		}
		if correctPlan(row.GatedPlan, gold) && !correctPlan(row.ContextGatedPlan, gold) {
			m.ContextGatedLosses++
		}
		if falseChange(row.ContextPlan, gold) {
			m.FalseChangeContext++
		}
		if falseChange(row.ContextGatedPlan, gold) {
			m.FalseChangeContextGated++
		}
		m.ConfusionContext[goldKey+"→"+predictedOutcome(row.ContextPlan)]++
		m.ConfusionContextGated[goldKey+"→"+predictedOutcome(row.ContextGatedPlan)]++
	}
}

func writePrivate(path string, value any) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if rows, ok := value.([]reviewItem); ok {
		for _, row := range rows {
			if err := json.NewEncoder(file).Encode(row); err != nil {
				return err
			}
		}
		return nil
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
