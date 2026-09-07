package workflow

import (
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/Tutitoos/atenea/pkg/contract"
)

var activitySecret = regexp.MustCompile(`(?i)\b(?:ghp_|github_pat_|sk-|xox[a-z]-|AIza)[A-Za-z0-9_-]+`)

// ActivityNotice is an intent written before an invocation. It is separate
// from report Notices, which describe evidence learned after an invocation.
type ActivityNotice struct {
	Cursor     int64  `json:"cursor"`
	WorkflowID string `json:"workflow_id"`
	PointID    string `json:"point_id,omitempty"`
	// AgentRunID is the provider/agent execution id. InvocationID remains the
	// dedupe key and is normally equal to it for a step notice.
	AgentRunID               string    `json:"agent_run_id,omitempty"`
	InvocationID             string    `json:"invocation_id"`
	ThreadID                 string    `json:"thread_id,omitempty"`
	TurnID                   string    `json:"turn_id,omitempty"`
	UsageRevision            uint64    `json:"usage_revision,omitempty"`
	RequestedModel           string    `json:"requested_model,omitempty"`
	ObservedModel            string    `json:"observed_model,omitempty"`
	RequestedReasoningEffort string    `json:"requested_reasoning_effort,omitempty"`
	ObservedReasoningEffort  string    `json:"observed_reasoning_effort,omitempty"`
	Kind                     string    `json:"kind"`
	Tool                     string    `json:"tool"`
	Action                   string    `json:"action"`
	Objective                string    `json:"objective"`
	Purpose                  string    `json:"purpose"`
	Markdown                 string    `json:"markdown"`
	At                       time.Time `json:"at"`
}

// NewActivityNotice renders bounded Markdown without consulting a model.
func NewActivityNotice(kind, tool, action, objective, purpose string) ActivityNotice {
	kind = strings.ToUpper(cleanWords(kind, 1))
	if kind != "PLAN" {
		kind = "ATENEA"
	}
	tool = cleanName(tool)
	action = cleanWords(action, 2)
	objective = cleanWords(objective, 5)
	purpose = cleanWords(purpose, 4)
	if action == "" {
		action = "ejecuto"
	}
	if objective == "" {
		objective = "la tarea indicada"
	}
	if purpose == "" {
		purpose = "cumplir el criterio acordado"
	}
	if tool == "" {
		tool = "workflow"
	}
	markdown := "> **" + kind + " · " + tool + "** — " + action + " " + objective + " para " + purpose + ", con alcance autorizado y evidencia verificable."
	return ActivityNotice{Kind: strings.ToLower(kind), Tool: tool, Action: action,
		Objective: objective, Purpose: purpose, Markdown: markdown}
}

func cleanName(raw string) string {
	raw = activitySecret.ReplaceAllString(raw, "redacted")
	raw = contract.RedactRaw(strings.TrimSpace(raw))
	var out strings.Builder
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-", r) {
			out.WriteRune(r)
		} else if unicode.IsSpace(r) {
			out.WriteByte('-')
		}
	}
	value := strings.Trim(out.String(), "-.")
	runes := []rune(value)
	if len(runes) > 80 {
		value = string(runes[:80])
	}
	return value
}

func cleanWords(raw string, limit int) string {
	raw = activitySecret.ReplaceAllString(raw, "redacted")
	raw = contract.RedactRaw(strings.TrimSpace(raw))
	var plain strings.Builder
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) || strings.ContainsRune(".,:;!?-", r) {
			plain.WriteRune(r)
		} else {
			plain.WriteByte(' ')
		}
	}
	raw = plain.String()
	words := strings.Fields(raw)
	kept := words[:0]
	for _, word := range words {
		lower := strings.ToLower(word)
		if strings.ContainsAny(word, `/\`) || len(word) > 48 ||
			strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "ghp-") ||
			strings.HasPrefix(lower, "ghp_") || strings.HasPrefix(lower, "github_pat_") ||
			strings.HasPrefix(lower, "xox") || strings.HasPrefix(word, "AIza") {
			continue
		}
		kept = append(kept, word)
	}
	words = kept
	if len(words) > limit {
		words = words[:limit]
	}
	return strings.Join(words, " ")
}
