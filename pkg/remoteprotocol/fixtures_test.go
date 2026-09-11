package remoteprotocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"

	protocolv1 "github.com/Tutitoos/atenea/protocol/atenea.remote.v1"
)

const expectedValidFixtureCount = 70

func TestValidFixtureCorpus(t *testing.T) {
	fixtures, err := discoverFixtureCorpus(protocolv1.FS, "fixtures/valid")
	if err != nil {
		t.Fatalf("discover valid fixture corpus: %v", err)
	}
	validator := MustNewValidator()
	expected := expectedValidFixtureCases()
	var diagnostics []string
	expectedByFile := make(map[string]fixtureExpectation, len(expected))
	expectedByName := make(map[string]fixtureExpectation, len(expected))
	var manifestProblems []string
	if len(fixtures) != expectedValidFixtureCount {
		manifestProblems = append(manifestProblems, fmt.Sprintf("valid fixture entry count = %d, want %d", len(fixtures), expectedValidFixtureCount))
	}
	if len(expected) != expectedValidFixtureCount {
		manifestProblems = append(manifestProblems, fmt.Sprintf("valid fixture inventory count = %d, want %d", len(expected), expectedValidFixtureCount))
	}
	for _, fixture := range expected {
		if fixture.name == "" || fixture.file == "" || len(fixture.schemaPointers) == 0 || len(fixture.instanceValues) == 0 {
			manifestProblems = append(manifestProblems, fmt.Sprintf("incomplete fixture witness %q (%s)", fixture.name, fixture.file))
		}
		if previous, ok := expectedByFile[fixture.file]; ok {
			manifestProblems = append(manifestProblems, fmt.Sprintf("manifest duplicate fixture %q (%s and %s)", fixture.file, previous.name, fixture.name))
		}
		if previous, ok := expectedByName[fixture.name]; ok {
			manifestProblems = append(manifestProblems, fmt.Sprintf("manifest duplicate witness %q (%s and %s)", fixture.name, previous.file, fixture.file))
		}
		expectedByFile[fixture.file] = fixture
		expectedByName[fixture.name] = fixture
	}
	var seen []string
	var unexpected []string
	var witnessProblems []string
	for _, fixtureFile := range fixtures {
		name := fixtureFile.name
		raw := fixtureFile.raw
		if err := validator.Validate(raw); err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("fixture %s rejected: %v", name, err))
			continue
		}

		fixture, ok := expectedByFile[name]
		if !ok {
			unexpected = append(unexpected, name)
			continue
		}
		if previous, ok := expectedByName[fixture.name]; !ok || previous.file != name {
			witnessProblems = append(witnessProblems, fmt.Sprintf("fixture %s is not uniquely mapped to witness %q", name, fixture.name))
			continue
		}
		seen = append(seen, fixture.name)
		witnessProblems = append(witnessProblems, fixture.verify(protocolv1.FS, raw)...)
	}

	seenSet := make(map[string]bool, len(seen))
	for _, name := range seen {
		if seenSet[name] {
			witnessProblems = append(witnessProblems, fmt.Sprintf("duplicate witnessed semantic case %q", name))
		}
		seenSet[name] = true
	}
	var missing []string
	for _, fixture := range expected {
		if !seenSet[fixture.name] {
			missing = append(missing, fixture.name)
		}
	}
	sort.Strings(manifestProblems)
	sort.Strings(missing)
	sort.Strings(unexpected)
	sort.Strings(witnessProblems)
	diagnostics = append(diagnostics, manifestProblems...)
	for _, name := range missing {
		diagnostics = append(diagnostics, fmt.Sprintf("missing valid fixture witness: %s", name))
	}
	for _, name := range unexpected {
		diagnostics = append(diagnostics, fmt.Sprintf("unexpected valid fixture: %s", name))
	}
	diagnostics = append(diagnostics, witnessProblems...)
	if len(seenSet) != len(expectedByName) {
		diagnostics = append(diagnostics, fmt.Sprintf("valid fixture witness count = %d, want %d", len(seenSet), len(expectedByName)))
	}
	sort.Strings(diagnostics)
	for _, diagnostic := range diagnostics {
		t.Errorf("%s", diagnostic)
	}
}

func decodeFixtureInstance(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var instance any
	if err := decoder.Decode(&instance); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, fmt.Errorf("trailing JSON value: %w", err)
	}
	return instance, nil
}

func TestFixtureInstanceNumberPrecision(t *testing.T) {
	integer, err := decodeFixtureInstance([]byte(`{"value":42}`))
	if err != nil {
		t.Fatalf("decode integer: %v", err)
	}
	precise, err := decodeFixtureInstance([]byte(`{"value":42.0000000000000001}`))
	if err != nil {
		t.Fatalf("decode precise number: %v", err)
	}

	want := json.Number("42")
	integerValue, ok := jsonPointer(integer, "/value")
	if !ok || !reflect.DeepEqual(integerValue, want) {
		t.Fatalf("integer value = %v, want exact %q", integerValue, want)
	}
	preciseValue, ok := jsonPointer(precise, "/value")
	if !ok {
		t.Fatal("precise number is missing value")
	}
	if reflect.DeepEqual(preciseValue, want) {
		t.Fatalf("precise number %v must not match integer %q", preciseValue, want)
	}
}

type fixtureExpectation struct {
	name           string
	file           string
	schemaPointers []string
	instanceValues map[string]any
}

func (f fixtureExpectation) verify(schemaFS fs.FS, raw []byte) []string {
	var problems []string
	instance, err := decodeFixtureInstance(raw)
	if err != nil {
		return []string{fmt.Sprintf("witness %q cannot decode instance: %v", f.name, err)}
	}
	for _, reference := range f.schemaPointers {
		schemaFile, pointer, ok := strings.Cut(reference, "#")
		if !ok || schemaFile == "" || pointer == "" {
			problems = append(problems, fmt.Sprintf("witness %q has malformed schema pointer %q", f.name, reference))
			continue
		}
		rawSchema, err := fs.ReadFile(schemaFS, schemaFile)
		if err != nil {
			problems = append(problems, fmt.Sprintf("witness %q cannot read schema %q: %v", f.name, schemaFile, err))
			continue
		}
		var schema any
		if err := json.Unmarshal(rawSchema, &schema); err != nil {
			problems = append(problems, fmt.Sprintf("witness %q cannot decode schema %q: %v", f.name, schemaFile, err))
			continue
		}
		if _, ok := jsonPointer(schema, pointer); !ok {
			problems = append(problems, fmt.Sprintf("witness %q references missing schema pointer %s#%s", f.name, schemaFile, pointer))
		}
	}
	for pointer, want := range f.instanceValues {
		got, ok := jsonPointer(instance, pointer)
		if !ok {
			problems = append(problems, fmt.Sprintf("witness %q is missing instance pointer %s", f.name, pointer))
			continue
		}
		if !reflect.DeepEqual(got, want) {
			problems = append(problems, fmt.Sprintf("witness %q at %s = %v, want %v", f.name, pointer, got, want))
		}
	}
	return problems
}

func jsonPointer(document any, pointer string) (any, bool) {
	if pointer == "" {
		return document, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := document
	for _, token := range strings.Split(pointer[1:], "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[token]
			if !ok {
				return nil, false
			}
		case []any:
			if token == "" {
				return nil, false
			}
			index := 0
			for _, digit := range token {
				if digit < '0' || digit > '9' {
					return nil, false
				}
				index = index*10 + int(digit-'0')
			}
			if index < 0 || index >= len(value) {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func expectedValidFixtureCases() []fixtureExpectation {
	base := []fixtureExpectation{
		{name: "binary-frame-privacy-ephemeral", file: "binary-frame.json", schemaPointers: []string{"binary-frame.schema.json#/properties/privacy", "definitions.schema.json#/$defs/privacyMetadata/oneOf/0"}, instanceValues: map[string]any{"/message_type": "binary_frame", "/payload/kind": "binary_frame", "/payload/privacy/ephemeral": true, "/payload/privacy/artifact_persistence_authorized": false}},
		{name: "error-capability-denied", file: "error-capability-denied.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/errorMapping"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "refused", "/payload/code": "capability_denied"}},
		{name: "error-duplicate", file: "error-duplicate.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/errorMapping"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "refused", "/payload/code": "duplicate"}},
		{name: "error-expired", file: "error-expired.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/errorMapping"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "refused", "/payload/code": "expired"}},
		{name: "error-invalid-envelope", file: "error-invalid-envelope.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/errorMapping"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "refused", "/payload/code": "invalid_envelope"}},
		{name: "error-liveness-timeout", file: "error-liveness-timeout.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/errorMapping"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "error", "/payload/code": "liveness_timeout"}},
		{name: "error-missing-capability", file: "error-missing-capability.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/missingCapabilityDetails"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "refused", "/payload/code": "missing_capability", "/payload/details/capability": "remote.desktop.targets"}},
		{name: "error-mode-required", file: "error-mode-required.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/errorMapping"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "refused", "/payload/code": "mode_required"}},
		{name: "error-out-of-order", file: "error-out-of-order.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/outOfOrderDetails"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "error", "/payload/code": "out_of_order"}},
		{name: "error-oversize-exact", file: "error-oversize.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/oversizeDetails/oneOf/0"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "refused", "/payload/code": "oversize", "/payload/details/actual_is_lower_bound": false}},
		{name: "error-revoked", file: "error-revoked.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/errorMapping"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "refused", "/payload/code": "revoked"}},
		{name: "error-sensitive-surface", file: "error-sensitive-surface.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/errorMapping"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "refused", "/payload/code": "sensitive_surface"}},
		{name: "error-stale-target", file: "error-stale-target.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/staleTargetDetails"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "error", "/payload/code": "stale_target"}},
		{name: "error-target-unavailable", file: "error-target-unavailable.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/errorMapping"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "error", "/payload/code": "target_unavailable"}},
		{name: "error-unknown-field", file: "error-unknown-field.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/unknownFieldDetails"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "refused", "/payload/code": "unknown_field", "/payload/details/field": "payload.extra"}},
		{name: "error-unsupported-version", file: "error-unsupported-version.json", schemaPointers: []string{"error.schema.json#/properties/code", "definitions.schema.json#/$defs/errorMapping"}, instanceValues: map[string]any{"/message_type": "error", "/payload/status": "refused", "/payload/code": "unsupported_version"}},
		{name: "event-capabilities-changed", file: "event-capabilities-changed.json", schemaPointers: []string{"event.schema.json#/allOf/3/then/properties/data", "definitions.schema.json#/$defs/capabilitiesChangedEvent"}, instanceValues: map[string]any{"/message_type": "event", "/payload/event_type": "capabilities_changed", "/payload/data/kind": "capabilities_changed"}},
		{name: "event-heartbeat-timeout", file: "event-heartbeat-timeout.json", schemaPointers: []string{"event.schema.json#/allOf/4/then/properties/data", "definitions.schema.json#/$defs/heartbeatTimeoutEvent"}, instanceValues: map[string]any{"/message_type": "event", "/payload/event_type": "heartbeat_timeout", "/payload/data/kind": "heartbeat_timeout"}},
		{name: "event-mode-changed", file: "event-mode-changed.json", schemaPointers: []string{"event.schema.json#/allOf/1/then/properties/data", "definitions.schema.json#/$defs/modeChangedEvent"}, instanceValues: map[string]any{"/message_type": "event", "/payload/event_type": "mode_changed", "/payload/data/kind": "mode_changed"}},
		{name: "event-revoked", file: "event-revoked.json", schemaPointers: []string{"event.schema.json#/allOf/0/then/properties/data", "definitions.schema.json#/$defs/revocationEvent", "definitions.schema.json#/$defs/revocationEvent/properties/fence"}, instanceValues: map[string]any{"/message_type": "event", "/payload/event_id": "closure-intent-1", "/payload/event_type": "revoked", "/payload/data/kind": "revoked", "/payload/data/revocation_id": "revocation-1", "/payload/data/fence": json.Number("1")}},
		{name: "event-revocation-ack", file: "event-revocation-ack.json", schemaPointers: []string{"envelope.schema.json#/allOf/6/then/properties/payload", "event-ack.schema.json#/$ref", "definitions.schema.json#/$defs/eventAck"}, instanceValues: map[string]any{"/message_type": "event_ack", "/payload/kind": "event_ack", "/payload/event_type": "revoked", "/payload/event_id": "closure-intent-1", "/payload/fence": json.Number("1"), "/payload/acknowledged_at": json.Number("1")}},
		{name: "event-revocation-ack-fence-maximum", file: "event-revocation-ack-fence-maximum.json", schemaPointers: []string{"event-ack.schema.json#/$ref", "definitions.schema.json#/$defs/revocationFence"}, instanceValues: map[string]any{"/message_type": "event_ack", "/payload/kind": "event_ack", "/payload/event_type": "revoked", "/payload/event_id": "closure-intent-1", "/payload/fence": json.Number("9007199254740991")}},
		{name: "event-session-closed", file: "event-session-closed.json", schemaPointers: []string{"event.schema.json#/allOf/2/then/properties/data", "definitions.schema.json#/$defs/sessionClosedEvent"}, instanceValues: map[string]any{"/message_type": "event", "/payload/event_type": "session_closed", "/payload/data/kind": "session_closed"}},
		{name: "heartbeat-ping", file: "heartbeat-ping.json", schemaPointers: []string{"heartbeat.schema.json#/properties/kind"}, instanceValues: map[string]any{"/message_type": "heartbeat", "/payload/kind": "ping"}},
		{name: "heartbeat-pong", file: "heartbeat-pong.json", schemaPointers: []string{"heartbeat.schema.json#/properties/kind", "heartbeat.schema.json#/properties/acknowledged_sequence"}, instanceValues: map[string]any{"/message_type": "heartbeat", "/payload/kind": "pong", "/payload/echo_sequence": json.Number("0"), "/payload/acknowledged_sequence": json.Number("0")}},
		{name: "negotiation-accept-coordinator", file: "negotiation-accept.json", schemaPointers: []string{"negotiation.schema.json#/oneOf/1", "definitions.schema.json#/$defs/capabilityGrants"}, instanceValues: map[string]any{"/message_type": "negotiation", "/payload/phase": "accept", "/payload/role": "coordinator", "/payload/accepted_version": "1.1.0"}},
		{name: "negotiation-offer-agent", file: "negotiation-offer.json", schemaPointers: []string{"negotiation.schema.json#/oneOf/0", "definitions.schema.json#/$defs/modeArray"}, instanceValues: map[string]any{"/message_type": "negotiation", "/payload/phase": "offer", "/payload/role": "agent", "/payload/modes/0": "background"}},
		{name: "negotiation-reject-coordinator", file: "negotiation-reject.json", schemaPointers: []string{"negotiation.schema.json#/oneOf/2", "definitions.schema.json#/$defs/rejection"}, instanceValues: map[string]any{"/message_type": "negotiation", "/payload/phase": "reject", "/payload/role": "coordinator", "/payload/rejection/code": "unsupported_version"}},
		{name: "request-apps-background-appsArguments", file: "request-apps.json", schemaPointers: []string{"request.schema.json#/oneOf/1", "definitions.schema.json#/$defs/appsArguments"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.apps", "/payload/mode": "background", "/payload/arguments/scope": "policy_visible"}},
		{name: "request-close-background-observationProof", file: "request-close.json", schemaPointers: []string{"request.schema.json#/oneOf/5", "definitions.schema.json#/$defs/observationProof"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.close", "/payload/mode": "background", "/payload/target/target_id": "window-1"}},
		{name: "request-expand-background-observationProof", file: "request-expand.json", schemaPointers: []string{"request.schema.json#/oneOf/10", "definitions.schema.json#/$defs/observationProof"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.expand", "/payload/mode": "background", "/payload/target/target_id": "window-1"}},
		{name: "request-get-background-targetRef", file: "request-get.json", schemaPointers: []string{"request.schema.json#/oneOf/13", "definitions.schema.json#/$defs/getArguments", "definitions.schema.json#/$defs/targetRef"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.get", "/payload/mode": "background", "/payload/target/target_id": "window-1", "/payload/arguments/field": "ready"}},
		{name: "request-inspect-background-targetRef", file: "request-inspect.json", schemaPointers: []string{"request.schema.json#/oneOf/2", "definitions.schema.json#/$defs/emptyArguments", "definitions.schema.json#/$defs/targetRef"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.inspect", "/payload/mode": "background", "/payload/target/target_id": "window-1"}},
		{name: "request-invoke-background-observationProof", file: "request-invoke.json", schemaPointers: []string{"request.schema.json#/oneOf/6", "definitions.schema.json#/$defs/invokeArguments", "definitions.schema.json#/$defs/observationProof"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.invoke", "/payload/mode": "background", "/payload/target/target_id": "window-1", "/payload/arguments/action_name": "refresh"}},
		{name: "request-open-background-observationProof", file: "request-open.json", schemaPointers: []string{"request.schema.json#/oneOf/4", "definitions.schema.json#/$defs/observationProof"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.open", "/payload/mode": "background", "/payload/target/target_id": "window-1"}},
		{name: "request-screenshot-background-targetRef", file: "request-screenshot.json", schemaPointers: []string{"request.schema.json#/oneOf/3", "definitions.schema.json#/$defs/emptyArguments", "definitions.schema.json#/$defs/targetRef"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.screenshot", "/payload/mode": "background", "/payload/target/target_id": "window-1"}},
		{name: "request-scroll-background-observationProof", file: "request-scroll.json", schemaPointers: []string{"request.schema.json#/oneOf/11", "definitions.schema.json#/$defs/scrollArguments", "definitions.schema.json#/$defs/observationProof"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.scroll", "/payload/mode": "background", "/payload/target/target_id": "window-1", "/payload/arguments/delta_x": json.Number("-2000")}},
		{name: "request-select-background-observationProof", file: "request-select.json", schemaPointers: []string{"request.schema.json#/oneOf/8", "definitions.schema.json#/$defs/selectArguments", "definitions.schema.json#/$defs/observationProof"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.select", "/payload/mode": "background", "/payload/target/target_id": "window-1", "/payload/arguments/option_id": "one"}},
		{name: "request-set-background-observationProof", file: "request-set.json", schemaPointers: []string{"request.schema.json#/oneOf/7", "definitions.schema.json#/$defs/setArguments", "definitions.schema.json#/$defs/observationProof"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.set", "/payload/mode": "background", "/payload/target/target_id": "window-1", "/payload/arguments/field": "enabled"}},
		{name: "request-targets-background-targetsArguments", file: "request-targets.json", schemaPointers: []string{"request.schema.json#/oneOf/0", "definitions.schema.json#/$defs/targetsArguments"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.targets", "/payload/mode": "background", "/payload/arguments/scope": "registered"}},
		{name: "request-toggle-background-observationProof", file: "request-toggle.json", schemaPointers: []string{"request.schema.json#/oneOf/9", "definitions.schema.json#/$defs/toggleArguments", "definitions.schema.json#/$defs/observationProof"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.toggle", "/payload/mode": "background", "/payload/target/target_id": "window-1", "/payload/arguments/desired_state": true}},
		{name: "request-type-isolated-isolatedGate", file: "request-type.json", schemaPointers: []string{"request.schema.json#/oneOf/15", "definitions.schema.json#/$defs/typeArguments", "definitions.schema.json#/$defs/isolatedGate"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.type", "/payload/mode": "isolated", "/payload/safety_gate/kind": "isolated"}},
		{name: "request-wait-background-targetRef", file: "request-wait.json", schemaPointers: []string{"request.schema.json#/oneOf/12", "definitions.schema.json#/$defs/waitArguments", "definitions.schema.json#/$defs/targetRef"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.wait", "/payload/mode": "background", "/payload/target/target_id": "window-1", "/payload/arguments/condition": "visible"}},
		{name: "result-ack", file: "result-ack.json", schemaPointers: []string{"result.schema.json#/oneOf/6", "definitions.schema.json#/$defs/ackResult"}, instanceValues: map[string]any{"/message_type": "result", "/payload/status": "succeeded", "/payload/data/kind": "ack", "/payload/data/accepted": true}},
		{name: "result-apps-empty", file: "result-apps.json", schemaPointers: []string{"result.schema.json#/oneOf/1", "definitions.schema.json#/$defs/appsResult/properties/items"}, instanceValues: map[string]any{"/message_type": "result", "/payload/status": "succeeded", "/payload/data/kind": "apps", "/payload/data/items": []any{}}},
		{name: "result-inspect-visible", file: "result-inspect.json", schemaPointers: []string{"result.schema.json#/oneOf/2", "definitions.schema.json#/$defs/inspectionResult/oneOf/0"}, instanceValues: map[string]any{"/message_type": "result", "/payload/status": "succeeded", "/payload/data/kind": "inspect", "/payload/data/visible": true, "/payload/data/visibility": "visible"}},
		{name: "result-screenshot-visible", file: "result-screenshot.json", schemaPointers: []string{"result.schema.json#/oneOf/3", "definitions.schema.json#/$defs/screenshotResult/oneOf/0"}, instanceValues: map[string]any{"/message_type": "result", "/payload/status": "succeeded", "/payload/data/kind": "screenshot", "/payload/data/visible": true, "/payload/data/visibility": "visible"}},
		{name: "result-state", file: "result-state.json", schemaPointers: []string{"result.schema.json#/oneOf/5", "definitions.schema.json#/$defs/stateResult", "definitions.schema.json#/$defs/safeValue/anyOf/3"}, instanceValues: map[string]any{"/message_type": "result", "/payload/status": "succeeded", "/payload/data/kind": "state", "/payload/data/field": "ready", "/payload/data/value": true}},
		{name: "result-targets-empty", file: "result-targets.json", schemaPointers: []string{"result.schema.json#/oneOf/0", "definitions.schema.json#/$defs/targetsResult/properties/items"}, instanceValues: map[string]any{"/message_type": "result", "/payload/status": "succeeded", "/payload/data/kind": "targets", "/payload/data/items": []any{}}},
		{name: "result-wait", file: "result-wait.json", schemaPointers: []string{"result.schema.json#/oneOf/4", "definitions.schema.json#/$defs/waitResult"}, instanceValues: map[string]any{"/message_type": "result", "/payload/status": "succeeded", "/payload/data/kind": "wait", "/payload/data/condition_met": true}},
		{name: "request-click-attended-attendedGate", file: "request-click.json", schemaPointers: []string{"request.schema.json#/oneOf/14", "definitions.schema.json#/$defs/clickArguments", "definitions.schema.json#/$defs/attendedGate"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.click", "/payload/mode": "attended", "/payload/safety_gate/kind": "attended"}},
		{name: "request-key-attended-attendedGate", file: "request-key.json", schemaPointers: []string{"request.schema.json#/oneOf/16", "definitions.schema.json#/$defs/keyArguments", "definitions.schema.json#/$defs/attendedGate"}, instanceValues: map[string]any{"/message_type": "request", "/payload/capability": "remote.desktop.key", "/payload/mode": "attended", "/payload/safety_gate/kind": "attended"}},
	}
	result := make([]fixtureExpectation, 0, len(base)+17)
	result = append(result, base...)
	result = append(result,
		fixtureExpectation{name: "request-click-isolated-isolatedGate", file: "request-click-isolated.json", schemaPointers: []string{"request.schema.json#/oneOf/14", "request.schema.json#/allOf/1/then/properties/safety_gate", "definitions.schema.json#/$defs/isolatedGate"}, instanceValues: map[string]any{"/payload/capability": "remote.desktop.click", "/payload/mode": "isolated", "/payload/safety_gate/kind": "isolated"}},
		fixtureExpectation{name: "request-type-attended-attendedGate", file: "request-type-attended.json", schemaPointers: []string{"request.schema.json#/oneOf/15", "request.schema.json#/allOf/0/then/properties/safety_gate", "definitions.schema.json#/$defs/attendedGate"}, instanceValues: map[string]any{"/payload/capability": "remote.desktop.type", "/payload/mode": "attended", "/payload/safety_gate/kind": "attended"}},
		fixtureExpectation{name: "request-key-isolated-isolatedGate", file: "request-key-isolated.json", schemaPointers: []string{"request.schema.json#/oneOf/16", "request.schema.json#/allOf/1/then/properties/safety_gate", "definitions.schema.json#/$defs/isolatedGate"}, instanceValues: map[string]any{"/payload/capability": "remote.desktop.key", "/payload/mode": "isolated", "/payload/safety_gate/kind": "isolated"}},
		fixtureExpectation{name: "result-inspect-visible-false-hidden", file: "result-inspect-hidden.json", schemaPointers: []string{"definitions.schema.json#/$defs/inspectionResult/oneOf/1"}, instanceValues: map[string]any{"/payload/data/kind": "inspect", "/payload/data/visible": false, "/payload/data/visibility": "hidden"}},
		fixtureExpectation{name: "result-inspect-visible-false-unknown", file: "result-inspect-unknown.json", schemaPointers: []string{"definitions.schema.json#/$defs/inspectionResult/oneOf/1"}, instanceValues: map[string]any{"/payload/data/kind": "inspect", "/payload/data/visible": false, "/payload/data/visibility": "unknown"}},
		fixtureExpectation{name: "result-screenshot-visible-false-hidden", file: "result-screenshot-hidden.json", schemaPointers: []string{"definitions.schema.json#/$defs/screenshotResult/oneOf/1"}, instanceValues: map[string]any{"/payload/data/kind": "screenshot", "/payload/data/visible": false, "/payload/data/visibility": "hidden"}},
		fixtureExpectation{name: "result-screenshot-visible-false-unknown", file: "result-screenshot-unknown.json", schemaPointers: []string{"definitions.schema.json#/$defs/screenshotResult/oneOf/1"}, instanceValues: map[string]any{"/payload/data/kind": "screenshot", "/payload/data/visible": false, "/payload/data/visibility": "unknown"}},
		fixtureExpectation{name: "binary-frame-privacy-persistent", file: "binary-frame-persistent.json", schemaPointers: []string{"definitions.schema.json#/$defs/privacyMetadata/oneOf/1", "binary-frame.schema.json#/properties/privacy"}, instanceValues: map[string]any{"/payload/privacy/ephemeral": false, "/payload/privacy/artifact_persistence_authorized": true}},
		fixtureExpectation{name: "error-oversize-safe-integer-lower-bound", file: "error-oversize-lower-bound.json", schemaPointers: []string{"definitions.schema.json#/$defs/oversizeDetails/oneOf/1"}, instanceValues: map[string]any{"/payload/details/actual": json.Number("9007199254740991"), "/payload/details/actual_is_lower_bound": true}},
		fixtureExpectation{name: "result-inspect-safeValue-string", file: "result-inspect-safe-string.json", schemaPointers: []string{"definitions.schema.json#/$defs/safeValue/anyOf/0", "definitions.schema.json#/$defs/inspectionResult/properties/fields/items/properties/value"}, instanceValues: map[string]any{"/payload/data/fields/0/value": "hello"}},
		fixtureExpectation{name: "result-inspect-safeValue-integer", file: "result-inspect-safe-integer.json", schemaPointers: []string{"definitions.schema.json#/$defs/safeValue/anyOf/1", "definitions.schema.json#/$defs/inspectionResult/properties/fields/items/properties/value"}, instanceValues: map[string]any{"/payload/data/fields/0/value": json.Number("42")}},
		fixtureExpectation{name: "result-inspect-safeValue-fractional-number", file: "result-inspect-safe-fractional.json", schemaPointers: []string{"definitions.schema.json#/$defs/safeValue/anyOf/2", "definitions.schema.json#/$defs/inspectionResult/properties/fields/items/properties/value"}, instanceValues: map[string]any{"/payload/data/fields/0/value": json.Number("1.5")}},
		fixtureExpectation{name: "result-apps-non-empty-item", file: "result-apps-item.json", schemaPointers: []string{"definitions.schema.json#/$defs/appsResult/properties/items/items"}, instanceValues: map[string]any{"/payload/data/items/0/application_id": "app-1"}},
		fixtureExpectation{name: "result-targets-non-empty-item", file: "result-targets-item.json", schemaPointers: []string{"definitions.schema.json#/$defs/targetsResult/properties/items/items", "definitions.schema.json#/$defs/targetRef"}, instanceValues: map[string]any{"/payload/data/items/0/target_id": "target-1"}},
		fixtureExpectation{name: "binary-frame-size-maximum", file: "binary-frame-max-size.json", schemaPointers: []string{"binary-frame.schema.json#/properties/size_bytes", "definitions.schema.json#/$defs/binaryMetadata/properties/size_bytes"}, instanceValues: map[string]any{"/payload/size_bytes": json.Number("16777216")}},
		fixtureExpectation{name: "request-id-length-1", file: "request-id-min.json", schemaPointers: []string{"envelope.schema.json#/properties/request_id", "definitions.schema.json#/$defs/requestId"}, instanceValues: map[string]any{"/request_id": "r"}},
		fixtureExpectation{name: "request-id-length-128", file: "request-id-max.json", schemaPointers: []string{"envelope.schema.json#/properties/request_id", "definitions.schema.json#/$defs/requestId"}, instanceValues: map[string]any{"/request_id": strings.Repeat("r", 128)}},
		fixtureExpectation{name: "request-type-text-maxLength-4096", file: "request-type-text-max.json", schemaPointers: []string{"request.schema.json#/oneOf/15", "definitions.schema.json#/$defs/typeArguments/properties/text"}, instanceValues: map[string]any{"/payload/capability": "remote.desktop.type", "/payload/mode": "isolated", "/payload/arguments/text": strings.Repeat("t", 4096)}},
	)
	return result
}
