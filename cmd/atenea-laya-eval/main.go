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
	ID             string            `json:"id"`
	Text           string            `json:"text"`
	Split          string            `json:"split"`
	GrantedEffects []contract.Effect `json:"granted_effects,omitempty"`
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var safeEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type label struct {
	ID       string        `json:"id"`
	Expected decision.Kind `json:"expected"`
}

type planView struct {
	Intent      decision.Kind `json:"intent"`
	Agent       string        `json:"agent"`
	Specialists []string      `json:"specialists"`
	Steps       []stepView    `json:"steps"`
	Models      []string      `json:"models"`
	Tools       []string      `json:"tools"`
	BudgetUSD   float64       `json:"budget_required_usd"`
	Valid       bool          `json:"valid"`
}

type stepView struct {
	Type    string            `json:"type"`
	Effects []contract.Effect `json:"effects"`
}

type result struct {
	ID           string        `json:"id"`
	Split        string        `json:"split"`
	Expected     decision.Kind `json:"expected,omitempty"`
	Rules        decision.Kind `json:"rules"`
	Laya         decision.Kind `json:"laya,omitempty"`
	Gated        decision.Kind `json:"gated"`
	Confidence   float64       `json:"answer_confidence,omitempty"`
	Model        string        `json:"model,omitempty"`
	RoutingModel string        `json:"routing_model,omitempty"`
	Fallback     string        `json:"fallback,omitempty"`
	ObserveMS    int64         `json:"observe_duration_ms"`
	RulesPlan    planView      `json:"rules_plan"`
	GatedPlan    planView      `json:"gated_plan"`
	Safe         bool          `json:"safe"`
	BlindedA     string        `json:"blinded_a"`
	BlindedB     string        `json:"blinded_b"`
}

type splitMetrics struct {
	Count            int            `json:"count"`
	RulesCorrect     int            `json:"rules_correct"`
	LayaCorrect      int            `json:"laya_correct"`
	GatedCorrect     int            `json:"gated_correct"`
	LayaCovered      int            `json:"laya_covered"`
	GatedWins        int            `json:"gated_wins"`
	GatedLosses      int            `json:"gated_losses"`
	UnsafePlans      int            `json:"unsafe_plans"`
	FalseChangeRules int            `json:"false_change_rules"`
	FalseChangeLaya  int            `json:"false_change_laya"`
	FalseChangeGated int            `json:"false_change_gated"`
	ConfusionRules   map[string]int `json:"confusion_rules"`
	ConfusionLaya    map[string]int `json:"confusion_laya"`
	ConfusionGated   map[string]int `json:"confusion_gated"`
}

type report struct {
	Evidence         string                   `json:"evidence"`
	CorpusSHA256     string                   `json:"corpus_sha256"`
	LabelsSHA256     string                   `json:"labels_sha256,omitempty"`
	SettingsSHA256   string                   `json:"settings_sha256"`
	RequestedModel   string                   `json:"requested_model,omitempty"`
	Threshold        float64                  `json:"threshold"`
	Count            int                      `json:"count"`
	Labeled          int                      `json:"labeled"`
	ServiceResponses int                      `json:"service_responses"`
	ServiceFailures  int                      `json:"service_failures"`
	Models           []string                 `json:"models"`
	RoutingModels    []string                 `json:"routing_models"`
	BySplit          map[string]*splitMetrics `json:"by_split,omitempty"`
	Cases            []result                 `json:"cases"`
}

type reviewItem struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Split    string   `json:"split"`
	OptionA  planView `json:"option_a"`
	OptionB  planView `json:"option_b"`
	Decision string   `json:"preferred_option"`
	Reason   string   `json:"reason"`
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
	rep := report{Evidence: "local dry-run plans and responses from the configured Laya service; model identity must be verified separately",
		CorpusSHA256: corpusHash, LabelsSHA256: labelsHash, SettingsSHA256: settingsHash,
		RequestedModel: *model, Threshold: *threshold, Count: len(cases),
		BySplit: map[string]*splitMetrics{}, Cases: make([]result, 0, len(cases))}
	packet := make([]reviewItem, 0, len(cases))
	models := map[string]bool{}
	routes := map[string]bool{}
	for _, row := range cases {
		req := decision.Request{Text: row.Text, Repository: *repository, BudgetUSD: 10, Effects: row.GrantedEffects}
		start := time.Now()
		rulesPlan, err := observing.BuildContext(context.Background(), req)
		if err != nil {
			return fmt.Errorf("case %s: rules/observe plan: %w", row.ID, err)
		}
		observeMS := time.Since(start).Milliseconds()
		if rulesPlan.IntentEvidence.Source != "rules" {
			return fmt.Errorf("case %s: observation changed the selected rules intent", row.ID)
		}
		selected := fixedClassifier{err: errors.New("no valid Laya proposal")}
		if rulesPlan.IntentEvidence.Laya != nil {
			selected = fixedClassifier{classification: *rulesPlan.IntentEvidence.Laya}
		}
		cfg.Decision.Mode = "laya"
		gatedPlan, err := (decision.Planner{Config: cfg, Classifier: selected}).BuildContext(context.Background(), req)
		cfg.Decision.Mode = "observe"
		if err != nil {
			return fmt.Errorf("case %s: gated plan: %w", row.ID, err)
		}
		if !rulesPlan.Valid || !gatedPlan.Valid {
			return fmt.Errorf("case %s: settings produced an invalid rules or gated plan; configure valid model roles and budget", row.ID)
		}
		item := result{ID: row.ID, Split: row.Split, Rules: rulesPlan.Intent, Gated: gatedPlan.Intent,
			Fallback: rulesPlan.IntentEvidence.FallbackReason, RulesPlan: view(rulesPlan), GatedPlan: view(gatedPlan),
			ObserveMS: observeMS, Safe: safePlan(rulesPlan, row.GrantedEffects) && safePlan(gatedPlan, row.GrantedEffects)}
		if proposal := rulesPlan.IntentEvidence.Laya; proposal != nil {
			item.Laya, item.Confidence, item.Model, item.RoutingModel = proposal.Intent, proposal.Confidence, proposal.Model, proposal.RoutingModel
			rep.ServiceResponses++
			models[proposal.Model] = true
			routes[proposal.RoutingModel] = true
			if *expectedRoute != "" && proposal.RoutingModel != *expectedRoute {
				return fmt.Errorf("case %s: Laya routed to %q, expected %q", row.ID, proposal.RoutingModel, *expectedRoute)
			}
		} else {
			rep.ServiceFailures++
		}
		if gold, ok := labels[row.ID]; ok {
			item.Expected = gold.Expected
			rep.Labeled++
			updateMetrics(rep.BySplit, item, *threshold)
		}
		var order [1]byte
		if _, err := rand.Read(order[:]); err != nil {
			return fmt.Errorf("case %s: randomize review order: %w", row.ID, err)
		}
		if order[0]&1 == 0 {
			item.BlindedA, item.BlindedB = "rules", "gated"
			packet = append(packet, reviewItem{ID: row.ID, Text: row.Text, Split: row.Split, OptionA: item.RulesPlan, OptionB: item.GatedPlan})
		} else {
			item.BlindedA, item.BlindedB = "gated", "rules"
			packet = append(packet, reviewItem{ID: row.ID, Text: row.Text, Split: row.Split, OptionA: item.GatedPlan, OptionB: item.RulesPlan})
		}
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
		if !known[row.ID] || labels[row.ID].ID != "" || !validIntent(row.Expected) {
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
	out := planView{Intent: plan.Intent, Agent: plan.Agent, Specialists: plan.Specialists,
		BudgetUSD: plan.Budget.RequiredUSD, Valid: plan.Valid}
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

func updateMetrics(splits map[string]*splitMetrics, row result, threshold float64) {
	m := splits[row.Split]
	if m == nil {
		m = &splitMetrics{ConfusionRules: map[string]int{}, ConfusionLaya: map[string]int{}, ConfusionGated: map[string]int{}}
		splits[row.Split] = m
	}
	m.Count++
	if row.Rules == row.Expected {
		m.RulesCorrect++
	}
	if row.Laya == row.Expected {
		m.LayaCorrect++
	}
	if row.Gated == row.Expected {
		m.GatedCorrect++
	}
	if row.Laya != "" && row.Confidence >= threshold {
		m.LayaCovered++
	}
	if row.Rules != row.Expected && row.Gated == row.Expected {
		m.GatedWins++
	}
	if row.Rules == row.Expected && row.Gated != row.Expected {
		m.GatedLosses++
	}
	if !row.Safe {
		m.UnsafePlans++
	}
	if row.Expected != decision.KindChange {
		if row.Rules == decision.KindChange {
			m.FalseChangeRules++
		}
		if row.Laya == decision.KindChange {
			m.FalseChangeLaya++
		}
		if row.Gated == decision.KindChange {
			m.FalseChangeGated++
		}
	}
	m.ConfusionRules[string(row.Expected)+"→"+string(row.Rules)]++
	if row.Laya != "" {
		m.ConfusionLaya[string(row.Expected)+"→"+string(row.Laya)]++
	}
	m.ConfusionGated[string(row.Expected)+"→"+string(row.Gated)]++
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
