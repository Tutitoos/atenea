package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
)

func TestLayaClassifierUsesSystemOneAndAnswerConfidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		var request layaRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.State["body"] != "Do not make changes yet; plan the migration." {
			t.Errorf("state = %v", request.State)
		}
		question, ok := request.Questions["intent"]
		if !ok || question.Type != "choice" || len(question.Criteria) != 4 {
			t.Errorf("question = %+v", question)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"laya-multilingual","answers":{"intent":{"type":"choice","choice":"plan","confidence":0.99,"answer_confidence":0.84}},"routing":{"model":"multilingual","reason":"Spanish text"}}`))
	}))
	defer server.Close()
	t.Setenv("ATENEA_TEST_LAYA_KEY", "test-key")

	classifier := NewLayaClassifier(config.DecisionSettings{
		LayaEndpoint: server.URL + "/v1/systemone", LayaAPIKeyEnv: "ATENEA_TEST_LAYA_KEY", Timeout: time.Second,
	})
	got, err := classifier.Classify(t.Context(), "Do not make changes yet; plan the migration.")
	if err != nil {
		t.Fatal(err)
	}
	if got.Intent != KindPlan || got.Confidence != 0.84 || got.Model != "laya-multilingual" || got.RoutingModel != "multilingual" {
		t.Fatalf("classification = %+v", got)
	}
}

func TestLayaClassifierRequiresTheConfiguredCredential(t *testing.T) {
	classifier := NewLayaClassifier(config.DecisionSettings{
		LayaEndpoint: "http://127.0.0.1:1/v1/systemone", LayaAPIKeyEnv: "ATENEA_TEST_MISSING_LAYA_KEY",
	})
	if _, err := classifier.Classify(t.Context(), "plan this"); err == nil || !strings.Contains(err.Error(), "environment variable") {
		t.Fatalf("Classify error = %v", err)
	}
}

func TestLayaClassifierRejectsBadStatusMalformedAndOversizedResponses(t *testing.T) {
	responses := map[string]struct {
		status int
		body   string
	}{
		"unauthorized": {status: http.StatusUnauthorized, body: `{"detail":"unauthorized"}`},
		"malformed":    {status: http.StatusOK, body: `{"answers":`},
		"oversized":    {status: http.StatusOK, body: strings.Repeat(" ", maxLayaResponseBytes+1)},
	}
	for name, response := range responses {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(response.status)
				_, _ = w.Write([]byte(response.body))
			}))
			defer server.Close()
			classifier := NewLayaClassifier(config.DecisionSettings{LayaEndpoint: server.URL + "/v1/systemone", Timeout: time.Second})
			if _, err := classifier.Classify(t.Context(), "plan this"); err == nil {
				t.Fatal("Classify accepted an invalid service response")
			}
		})
	}
}

func TestLayaClassifierDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Bool
	final := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	defer final.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", final.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()
	classifier := NewLayaClassifier(config.DecisionSettings{LayaEndpoint: redirect.URL + "/v1/systemone", Timeout: time.Second})
	if _, err := classifier.Classify(t.Context(), "plan this"); err == nil {
		t.Fatal("Classify accepted a redirect")
	}
	if followed.Load() {
		t.Fatal("Laya endpoint redirect received commission text")
	}
}

func TestLayaClassifierHonorsTheRequestDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	classifier := NewLayaClassifier(config.DecisionSettings{LayaEndpoint: server.URL + "/v1/systemone", Timeout: 10 * time.Millisecond})
	if _, err := classifier.Classify(context.Background(), "plan this"); err == nil {
		t.Fatal("Classify outlived its configured timeout")
	}
}

func TestLayaClassifierRejectsOversizedTextBeforeSendingIt(t *testing.T) {
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called.Store(true) }))
	defer server.Close()
	classifier := NewLayaClassifier(config.DecisionSettings{LayaEndpoint: server.URL + "/v1/systemone", Timeout: time.Second})
	if _, err := classifier.Classify(t.Context(), strings.Repeat("a", maxLayaStateCharacters+1)); err == nil {
		t.Fatal("Classify accepted text beyond Laya's documented state limit")
	}
	if called.Load() {
		t.Fatal("oversized commission text was sent to Laya")
	}
}

func TestLayaClassifierRefusesTrailingJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"model":"laya","answers":{"intent":{"type":"choice","choice":"plan","answer_confidence":0.9}}} {}`)
	}))
	defer server.Close()
	classifier := NewLayaClassifier(config.DecisionSettings{LayaEndpoint: server.URL + "/v1/systemone", Timeout: time.Second})
	if _, err := classifier.Classify(t.Context(), "plan this"); err == nil {
		t.Fatal("Classify accepted trailing JSON")
	}
}
