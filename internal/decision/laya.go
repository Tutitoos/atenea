package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tutitoos/atenea/internal/config"
)

const maxLayaResponseBytes = 1 << 20
const maxLayaStateCharacters = 50_000

// LayaClassifier calls a locally or explicitly configured Laya service using
// its /v1/systemone protocol. It does not own or launch the Python service.
type LayaClassifier struct {
	endpoint  string
	apiKeyEnv string
	client    *http.Client
}

// NewLayaClassifier creates an HTTP client with bounded time and no redirect
// following, so the commission text cannot be forwarded to another host by a
// service redirect.
func NewLayaClassifier(settings config.DecisionSettings) *LayaClassifier {
	timeout := settings.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &LayaClassifier{
		endpoint:  strings.TrimSpace(settings.LayaEndpoint),
		apiKeyEnv: strings.TrimSpace(settings.LayaAPIKeyEnv),
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

type layaRequest struct {
	State     map[string]string             `json:"state"`
	Questions map[string]layaChoiceQuestion `json:"questions"`
}

type layaChoiceQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type layaResponse struct {
	Model   string                `json:"model"`
	Answers map[string]layaAnswer `json:"answers"`
	Routing layaRouting           `json:"routing"`
}

type layaAnswer struct {
	Type             string   `json:"type"`
	Choice           string   `json:"choice"`
	AnswerConfidence *float64 `json:"answer_confidence"`
}

type layaRouting struct {
	Model  string `json:"model"`
	Reason string `json:"reason"`
}

// Classify requests one typed intent decision from the configured Laya service.
func (c *LayaClassifier) Classify(ctx context.Context, text string) (IntentClassification, error) {
	if c == nil || c.endpoint == "" {
		return IntentClassification{}, errors.New("laya endpoint is not configured")
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > maxLayaStateCharacters {
		return IntentClassification{}, errors.New("commission text is invalid UTF-8 or exceeds laya's 50,000-character state limit")
	}
	requestBody := layaRequest{
		State: map[string]string{"body": text},
		Questions: map[string]layaChoiceQuestion{
			"intent": {
				Type:         "choice",
				Instructions: "Which intent best describes the action explicitly requested by the user? Distinguish planning a change from carrying it out.",
				Criteria: map[string]string{
					string(KindUnderstand): "Explain, summarize, or understand existing information without asking to search a specific target or make a plan.",
					string(KindSearch):     "Find, locate, or inspect existing information, code, or behavior.",
					string(KindPlan):       "Design, plan, or recommend future work without asking to implement it now.",
					string(KindChange):     "Implement, add, edit, fix, or refactor something now.",
				},
			},
		},
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return IntentClassification{}, fmt.Errorf("encode Laya request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return IntentClassification{}, fmt.Errorf("build Laya request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKeyEnv != "" {
		key := strings.TrimSpace(os.Getenv(c.apiKeyEnv))
		if key == "" {
			return IntentClassification{}, fmt.Errorf("laya API key environment variable %s is empty", c.apiKeyEnv)
		}
		req.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return IntentClassification{}, fmt.Errorf("send laya request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return IntentClassification{}, fmt.Errorf("laya returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxLayaResponseBytes+1))
	if err != nil {
		return IntentClassification{}, fmt.Errorf("read laya response: %w", err)
	}
	if len(raw) > maxLayaResponseBytes {
		return IntentClassification{}, errors.New("laya response exceeds 1 MiB")
	}
	var decoded layaResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&decoded); err != nil {
		return IntentClassification{}, fmt.Errorf("decode laya response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return IntentClassification{}, errors.New("laya response contains trailing data")
	}
	answer, ok := decoded.Answers["intent"]
	if !ok || answer.Type != "choice" || answer.AnswerConfidence == nil {
		return IntentClassification{}, errors.New("laya response is missing a typed intent answer")
	}
	return IntentClassification{
		Intent:        Kind(answer.Choice),
		Confidence:    *answer.AnswerConfidence,
		Model:         decoded.Model,
		RoutingModel:  decoded.Routing.Model,
		RoutingReason: decoded.Routing.Reason,
	}, nil
}
