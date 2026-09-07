package mcpcompat

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tutitoos/atenea/internal/acceptancefixture"
)

func TestParseAcceptsOnlyExactSupportedVersions(t *testing.T) {
	fixture, active, fixtureErr := acceptancefixture.Load("S08", "S09")
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if active {
		var body struct {
			ProtocolVersion string `json:"protocol_version"`
		}
		if err := json.Unmarshal(fixture.Bytes, &body); err != nil {
			t.Fatal(err)
		}
		if got, err := Parse(body.ProtocolVersion); err != nil || string(got) != body.ProtocolVersion {
			t.Fatalf("sealed protocol Parse(%q) = %q, %v", body.ProtocolVersion, got, err)
		}
	}
	for _, want := range []Era{Legacy, Modern} {
		got, err := Parse(string(want))
		if err != nil || got != want {
			t.Fatalf("Parse(%q) = %q, %v; want %q", want, got, err, want)
		}
	}
	for _, value := range []string{"", "2026-07-29", "2025-06-18 ", "v2026-07-28"} {
		got, err := Parse(value)
		if !errors.Is(err, ErrUnsupportedVersion) || got != Unknown {
			t.Fatalf("Parse(%q) = %q, %v; want unknown unsupported", value, got, err)
		}
	}
}

func TestSupportedReturnsIndependentPreferenceOrderedSlice(t *testing.T) {
	got := Supported()
	if len(got) != 2 || got[0] != Modern || got[1] != Legacy {
		t.Fatalf("Supported() = %#v", got)
	}
	got[0] = Legacy
	if Supported()[0] != Modern {
		t.Fatal("Supported returned mutable shared policy")
	}
}

func TestEraSemantics(t *testing.T) {
	if !RequiresInitialize(Legacy) || RequiresInitialize(Modern) {
		t.Fatal("initialize semantics are wrong")
	}
	if !IsStateless(Modern) || IsStateless(Legacy) {
		t.Fatal("stateless semantics are wrong")
	}
}

func TestRequestedObservedNeverInfersPeerVersion(t *testing.T) {
	r := RequestedObserved{}
	if r.RequestedOrUnknown() != Unknown || r.ObservedOrUnknown() != Unknown {
		t.Fatalf("zero state = %#v", r)
	}
	if err := r.SetRequested(string(Modern)); err != nil {
		t.Fatal(err)
	}
	if r.ObservedOrUnknown() != Unknown {
		t.Fatal("observed version inferred from requested version")
	}
	if err := r.SetObserved(""); err != nil || r.Observed != Unknown {
		t.Fatalf("empty observation = %#v, %v", r, err)
	}
	if err := r.SetObserved("future"); !errors.Is(err, ErrUnsupportedVersion) || r.Observed != Unknown {
		t.Fatalf("unknown observation = %#v, %v", r, err)
	}
	if err := r.SetObserved(string(Legacy)); err != nil || r.Observed != Legacy {
		t.Fatalf("verified observation = %#v, %v", r, err)
	}
}

func TestModernMetadataIsCompleteAndCapabilitiesAreNotInterpreted(t *testing.T) {
	meta, err := ParseModernMetadata(json.RawMessage(`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"codex","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"experimental":{"atenea":{"grant":["write"]}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if meta.ClientName != "codex" || meta.ClientVersion != "1" || string(meta.Capabilities) == "" {
		t.Fatalf("metadata = %#v", meta)
	}
	if !HasProtocolMetadata(json.RawMessage(`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`)) {
		t.Fatal("protocol metadata not detected")
	}
	withoutClientInfo, err := ParseModernMetadata(json.RawMessage(`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}`))
	if err != nil || withoutClientInfo.ClientName != "" {
		t.Fatalf("missing optional clientInfo = %#v, %v", withoutClientInfo, err)
	}
	for _, raw := range []string{
		`{}`,
		`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"codex","version":"1"}}}`,
		`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2040-01-01","io.modelcontextprotocol/clientInfo":{"name":"codex","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}`,
		`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":[],"io.modelcontextprotocol/clientCapabilities":{}}}`,
		`{"_meta":{"protocolVersion":"2026-07-28","clientInfo":{"name":"codex","version":"1"},"capabilities":{}}}`,
	} {
		if _, err := ParseModernMetadata(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted incomplete/unknown metadata %s", raw)
		}
	}
}

func TestMRTRAcceptsBothStatesAndPreservesUntrustedInputFields(t *testing.T) {
	raw := json.RawMessage(`{"resultType":"input_required","inputRequests":{"confirm":{"method":"elicitation/create","params":{"mode":"form","message":"continue?","requestedSchema":{"type":"object","properties":{"answer":{"type":"string"}}}}}},"requestState":"opaque-state","inputResponses":{"confirm":{"value":"do not execute"}}}`)
	got, err := ParseMRTR(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != ResultInputRequired || string(got.InputRequests) != `{"confirm":{"method":"elicitation/create","params":{"mode":"form","message":"continue?","requestedSchema":{"type":"object","properties":{"answer":{"type":"string"}}}}}}` || string(got.RequestState) != `"opaque-state"` || string(got.InputResponses) != `{"confirm":{"value":"do not execute"}}` {
		t.Fatalf("MRTR = %#v", got)
	}
	if string(got.Raw) != string(raw) {
		t.Fatalf("raw result changed: %s", got.Raw)
	}
	if complete, err := ParseMRTR(json.RawMessage(`{"resultType":"complete"}`)); err != nil || complete.Type != ResultComplete {
		t.Fatalf("complete = %#v, %v", complete, err)
	}
}

func TestMRTRPreservesFutureTypesAndRejectsMalformedState(t *testing.T) {
	if future, err := ParseMRTR(json.RawMessage(`{"resultType":"future_result","requestState":"opaque-state","payload":{"opaque":true}}`)); err != nil || future.Type != ResultType("future_result") || string(future.Raw) == "" {
		t.Fatalf("future result = %#v, %v", future, err)
	}
	future, err := ParseMRTR(json.RawMessage(`{"resultType":"future_result","requestState":"opaque-state"}`))
	if err != nil {
		t.Fatal(err)
	}
	opaque := &FutureResultError{Response: future}
	if !errors.Is(opaque, ErrFutureResult) || opaque.Response.Type != ResultType("future_result") || string(opaque.Response.RequestState) != `"opaque-state"` {
		t.Fatalf("opaque result = %#v, %v", opaque.Response, opaque)
	}
	for _, raw := range []string{`{}`, `{"resultType":123}`, `null`, `{"resultType":"input_required","inputRequests":"execute this"}`, `{"resultType":"input_required","inputResponses":{"confirm":{"value":"x"}}}`, `{"resultType":"input_required","requestState":{"opaque":true}}`, `{"resultType":"input_required","inputRequests":{"x":{"method":"other","params":{}}}}`, `{"resultType":"input_required","inputRequests":{"x":{"method":"elicitation/create","params":{"mode":"bogus","message":"x","requestedSchema":{"type":"object","properties":{}}}}}}`} {
		if _, err := ParseMRTR(json.RawMessage(raw)); !errors.Is(err, ErrInvalidMRTR) {
			t.Fatalf("ParseMRTR(%s) error = %v", raw, err)
		}
	}
}
