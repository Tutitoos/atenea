package remoteprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	protocolv1 "github.com/Tutitoos/atenea/protocol/atenea.remote.v1"
)

var invalidFixtureFileCategories = []ErrorCategory{
	CategoryMalformed,
	CategoryDuplicate,
	CategoryTrailing,
	CategoryNonObject,
	CategorySchema,
}

var invalidFixtureProgrammaticCategories = []ErrorCategory{
	CategoryEmpty,
	CategoryOversize,
	CategoryDepth,
	CategoryNumeric,
	CategoryUnavailable,
}

// request-id-over-maximum intentionally violates both maxLength and the
// requestId pattern's embedded 128-character bound; the schema does not
// isolate those two keyword failures.
const expectedInvalidFixtureCount = 86

type fixtureIsolationCase struct {
	filename      string
	validFilename string
	pointer       string
}

var schemaIsolationCases = []fixtureIsolationCase{
	{filename: "binary-invalid-hash.json", validFilename: "binary-frame.json", pointer: "/payload/sha256"},
	{filename: "binary-privacy-persistence-mismatch.json", validFilename: "binary-frame-persistent.json", pointer: "/payload/privacy/artifact_persistence_authorized"},
	{filename: "binary-size-over-maximum.json", validFilename: "binary-frame-max-size.json", pointer: "/payload/size_bytes"},
	{filename: "binary-size-zero.json", validFilename: "binary-frame.json", pointer: "/payload/size_bytes"},
	{filename: "correlation-binary-empty.json", validFilename: "binary-frame.json", pointer: "/request_id"},
	{filename: "correlation-binary-missing.json", validFilename: "binary-frame.json", pointer: "/request_id"},
	{filename: "correlation-error-empty.json", validFilename: "error-missing-capability.json", pointer: "/request_id"},
	{filename: "correlation-error-missing.json", validFilename: "error-invalid-envelope.json", pointer: "/request_id"},
	{filename: "correlation-event-extra.json", validFilename: "event-heartbeat-timeout.json", pointer: "/request_id"},
	{filename: "correlation-heartbeat-extra.json", validFilename: "heartbeat-ping.json", pointer: "/request_id"},
	{filename: "correlation-negotiation-extra.json", validFilename: "negotiation-offer.json", pointer: "/request_id"},
	{filename: "correlation-payload-request-id.json", validFilename: "result-ack.json", pointer: "/payload/request_id"},
	{filename: "correlation-request-empty.json", validFilename: "request-targets.json", pointer: "/request_id"},
	{filename: "correlation-request-missing.json", validFilename: "request-targets.json", pointer: "/request_id"},
	{filename: "correlation-result-empty.json", validFilename: "result-ack.json", pointer: "/request_id"},
	{filename: "correlation-result-missing.json", validFilename: "result-ack.json", pointer: "/request_id"},
	{filename: "error-details-mismatch.json", validFilename: "error-missing-capability.json", pointer: "/payload/details/capability"},
	{filename: "error-retryable-mismatch.json", validFilename: "error-out-of-order.json", pointer: "/payload/retryable"},
	{filename: "error-status-mismatch.json", validFilename: "error-expired.json", pointer: "/payload/status"},
	{filename: "event-data-kind-mismatch.json", validFilename: "event-mode-changed.json", pointer: "/payload/data/kind"},
	{filename: "heartbeat-ping-missing-interval.json", validFilename: "heartbeat-ping.json", pointer: "/payload/interval_ms"},
	{filename: "heartbeat-pong-missing-echo.json", validFilename: "heartbeat-pong.json", pointer: "/payload/echo_sequence"},
	{filename: "identifier-empty-event.json", validFilename: "event-heartbeat-timeout.json", pointer: "/payload/event_id"},
	{filename: "identifier-invalid-character.json", validFilename: "heartbeat-ping.json", pointer: "/session_id"},
	{filename: "negotiation-phase-mismatch.json", validFilename: "negotiation-offer.json", pointer: "/payload/phase"},
	{filename: "negotiation-role-mismatch.json", validFilename: "negotiation-offer.json", pointer: "/payload/role"},
	{filename: "negotiation-unknown-capability.json", validFilename: "negotiation-offer.json", pointer: "/payload/capabilities"},
	{filename: "negotiation-unknown-mode.json", validFilename: "negotiation-offer.json", pointer: "/payload/modes"},
	{filename: "payload-binding-negotiation-heartbeat.json", validFilename: "heartbeat-ping.json", pointer: "/message_type"},
	{filename: "request-attended-click-missing-safety-gate.json", validFilename: "request-click.json", pointer: "/payload/safety_gate"},
	{filename: "request-attended-gate-consent-non-explicit.json", validFilename: "request-click.json", pointer: "/payload/safety_gate/consent"},
	{filename: "request-attended-gate-interruptible-false.json", validFilename: "request-click.json", pointer: "/payload/safety_gate/interruptible"},
	{filename: "request-attended-gate-local-user-present-false.json", validFilename: "request-click.json", pointer: "/payload/safety_gate/local_user_present"},
	{filename: "request-attended-isolated-gate.json", validFilename: "request-click-isolated.json", pointer: "/payload/mode"},
	{filename: "request-background-direct-input.json", validFilename: "request-click.json", pointer: "/payload/mode"},
	{filename: "request-background-key.json", validFilename: "request-key.json", pointer: "/payload/mode"},
	{filename: "request-background-type.json", validFilename: "request-type.json", pointer: "/payload/mode"},
	{filename: "request-id-over-maximum.json", validFilename: "result-ack.json", pointer: "/request_id"},
	{filename: "request-isolated-attended-gate.json", validFilename: "request-key-isolated.json", pointer: "/payload/safety_gate/kind"},
	{filename: "request-isolated-click-boundary-verified-false.json", validFilename: "request-click-isolated.json", pointer: "/payload/safety_gate/boundary_verified"},
	{filename: "request-missing-authorization.json", validFilename: "request-targets.json", pointer: "/payload/authorization"},
	{filename: "request-sensitive-target.json", validFilename: "request-click.json", pointer: "/payload/target/sensitivity"},
	{filename: "request-target-visible-false-visibility-visible.json", validFilename: "request-click.json", pointer: "/payload/target/visible"},
	{filename: "request-target-visible-true-visibility-hidden.json", validFilename: "request-click.json", pointer: "/payload/target/visibility"},
	{filename: "request-type-text-over-maximum.json", validFilename: "request-type-text-max.json", pointer: "/payload/arguments/text"},
	{filename: "request-unfocused-target.json", validFilename: "request-click.json", pointer: "/payload/target/focus"},
	{filename: "result-kind-mismatch.json", validFilename: "result-ack.json", pointer: "/payload/data/kind"},
	{filename: "result-persistence-mismatch.json", validFilename: "result-screenshot.json", pointer: "/payload/data/binary/privacy/artifact_persistence_authorized"},
	{filename: "result-screenshot-binary-size-over-maximum.json", validFilename: "result-screenshot.json", pointer: "/payload/data/binary/size_bytes"},
	{filename: "result-screenshot-visible-true-visibility-hidden.json", validFilename: "result-screenshot.json", pointer: "/payload/data/visibility"},
	{filename: "result-sensitive-privacy.json", validFilename: "result-screenshot.json", pointer: "/payload/data/binary/privacy/sensitivity"},
	{filename: "result-status-mismatch.json", validFilename: "result-ack.json", pointer: "/payload/status"},
	{filename: "result-visibility-mismatch.json", validFilename: "result-inspect.json", pointer: "/payload/data/visibility"},
	{filename: "sequence-negative.json", validFilename: "heartbeat-ping.json", pointer: "/sequence"},
	{filename: "sequence-over-maximum.json", validFilename: "heartbeat-ping.json", pointer: "/sequence"},
	{filename: "session-id-over-maximum.json", validFilename: "negotiation-offer.json", pointer: "/session_id"},
	{filename: "timestamp-negative.json", validFilename: "heartbeat-ping.json", pointer: "/sent_at"},
	{filename: "timestamp-over-maximum.json", validFilename: "heartbeat-ping.json", pointer: "/sent_at"},
	{filename: "unknown-envelope-field.json", validFilename: "heartbeat-ping.json", pointer: "/unknown"},
	{filename: "unknown-nested-data-field.json", validFilename: "result-ack.json", pointer: "/payload/data/unknown"},
	{filename: "unknown-payload-field.json", validFilename: "heartbeat-ping.json", pointer: "/payload/unknown"},
	{filename: "unsupported-message-type.json", validFilename: "heartbeat-ping.json", pointer: "/message_type"},
	{filename: "unsupported-protocol.json", validFilename: "heartbeat-ping.json", pointer: "/protocol"},
	{filename: "unsupported-version.json", validFilename: "heartbeat-ping.json", pointer: "/version"},
}

var errorMappingIsolationCases = []fixtureIsolationCase{
	{filename: "error-mapping-capability-denied-mismatch.json", validFilename: "error-capability-denied.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-duplicate-mismatch.json", validFilename: "error-duplicate.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-expired-mismatch.json", validFilename: "error-expired.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-invalid-envelope-mismatch.json", validFilename: "error-invalid-envelope.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-liveness-timeout-mismatch.json", validFilename: "error-liveness-timeout.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-missing-capability-mismatch.json", validFilename: "error-missing-capability.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-mode-required-mismatch.json", validFilename: "error-mode-required.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-out-of-order-mismatch.json", validFilename: "error-out-of-order.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-oversize-mismatch.json", validFilename: "error-oversize.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-revoked-mismatch.json", validFilename: "error-revoked.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-sensitive-surface-mismatch.json", validFilename: "error-sensitive-surface.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-stale-target-mismatch.json", validFilename: "error-stale-target.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-target-unavailable-mismatch.json", validFilename: "error-target-unavailable.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-unknown-field-mismatch.json", validFilename: "error-unknown-field.json", pointer: "/payload/retryable"},
	{filename: "error-mapping-unsupported-version-mismatch.json", validFilename: "error-unsupported-version.json", pointer: "/payload/retryable"},
}

func TestInvalidFixtureCorpus(t *testing.T) {
	fixtures, err := discoverInvalidFixtureCorpus(protocolv1.FS, "fixtures/invalid", invalidFixtureFileCategories)
	if err != nil {
		t.Fatalf("discover invalid fixture corpus: %v", err)
	}
	if len(fixtures) != expectedInvalidFixtureCount {
		t.Fatalf("invalid fixture count = %d, want %d", len(fixtures), expectedInvalidFixtureCount)
	}

	validator := MustNewValidator()
	seen := make(map[string]struct{}, len(fixtures))
	for _, fixture := range fixtures {
		if _, duplicate := seen[fixture.caseID]; duplicate {
			t.Errorf("duplicate case ID reached validator: %s", fixture.caseID)
		}
		seen[fixture.caseID] = struct{}{}
		err := validator.Validate(fixture.raw)
		if err == nil {
			t.Errorf("invalid fixture %s/%s was accepted", fixture.category, fixture.name)
			continue
		}
		if got := CategoryOf(err); got != fixture.category {
			t.Errorf("invalid fixture %s/%s category = %q, want exactly %q", fixture.category, fixture.name, got, fixture.category)
		}
	}
}

func TestSchemaFixtureIsolationCases(t *testing.T) {
	if len(schemaIsolationCases) != 64 {
		t.Fatalf("schema isolation table has %d entries, want 64", len(schemaIsolationCases))
	}
	if len(errorMappingIsolationCases) != 15 {
		t.Fatalf("error mapping isolation table has %d entries, want 15", len(errorMappingIsolationCases))
	}

	entries, err := fs.ReadDir(protocolv1.FS, "fixtures/invalid/schema")
	if err != nil {
		t.Fatalf("read schema fixture directory: %v", err)
	}
	current := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		current[entry.Name()] = struct{}{}
	}

	covered := make(map[string]string, len(schemaIsolationCases)+len(errorMappingIsolationCases))
	for source, cases := range map[string][]fixtureIsolationCase{
		"schema":        schemaIsolationCases,
		"error-mapping": errorMappingIsolationCases,
	} {
		for _, test := range cases {
			if previous, duplicate := covered[test.filename]; duplicate {
				t.Fatalf("fixture %s is covered by both %s and %s tables", test.filename, previous, source)
			}
			covered[test.filename] = source
		}
	}
	for filename := range current {
		if _, ok := covered[filename]; !ok {
			t.Errorf("schema fixture is missing from isolation tables: %s", filename)
		}
	}
	for filename := range covered {
		if _, ok := current[filename]; !ok {
			t.Errorf("isolation table names nonexistent schema fixture: %s", filename)
		}
	}
	if len(current) != len(covered) {
		t.Fatalf("schema fixture closure has %d current files and %d table entries", len(current), len(covered))
	}

	validator := MustNewValidator()
	for _, test := range schemaIsolationCases {
		t.Run(test.filename, func(t *testing.T) {
			assertFixtureIsolationCase(t, validator, test)
		})
	}
}

func assertFixtureIsolationCase(t *testing.T, validator *Validator, test fixtureIsolationCase) {
	t.Helper()
	candidateRaw, err := fs.ReadFile(protocolv1.FS, "fixtures/invalid/schema/"+test.filename)
	if err != nil {
		t.Fatalf("read candidate: %v", err)
	}
	validRaw, err := fs.ReadFile(protocolv1.FS, "fixtures/valid/"+test.validFilename)
	if err != nil {
		t.Fatalf("read valid counterpart: %v", err)
	}
	candidate, err := decodeFixtureInstance(candidateRaw)
	if err != nil {
		t.Fatalf("decode candidate: %v", err)
	}
	valid, err := decodeFixtureInstance(validRaw)
	if err != nil {
		t.Fatalf("decode valid counterpart: %v", err)
	}
	if err := validator.Validate(validRaw); err != nil {
		t.Fatalf("valid counterpart rejected: %v", err)
	}
	if got := CategoryOf(validator.Validate(candidateRaw)); got != CategorySchema {
		t.Fatalf("candidate validation category = %q, want exactly %q", got, CategorySchema)
	}
	if got := diffJSONPointers(valid, candidate); !reflect.DeepEqual(got, []string{test.pointer}) {
		t.Fatalf("candidate differs from valid at %v, want only %s", got, test.pointer)
	}
	candidate, err = restoreJSONPointer(candidate, valid, test.pointer)
	if err != nil {
		t.Fatalf("restore %s: %v", test.pointer, err)
	}
	if !reflect.DeepEqual(candidate, valid) {
		t.Fatalf("restored candidate differs from valid fixture")
	}
	restoredRaw, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal restored candidate: %v", err)
	}
	if err := validator.Validate(restoredRaw); err != nil {
		t.Fatalf("restored candidate rejected: %v", err)
	}
}

func TestSchemaFixtureIsolationCounterexamples(t *testing.T) {
	validator := MustNewValidator()
	for _, test := range schemaIsolationCases {
		t.Run(test.filename, func(t *testing.T) {
			validRaw, err := fs.ReadFile(protocolv1.FS, "fixtures/valid/"+test.validFilename)
			if err != nil {
				t.Fatal(err)
			}
			valid, err := decodeFixtureInstance(validRaw)
			if err != nil {
				t.Fatal(err)
			}
			empty := map[string]any{}
			if got := diffJSONPointers(valid, empty); reflect.DeepEqual(got, []string{test.pointer}) {
				t.Fatalf("{} substitution impersonated declared pointer %s", test.pointer)
			}
			if got := CategoryOf(validator.Validate([]byte(`{}`))); got != CategorySchema {
				t.Fatalf("{} substitution category = %q, want %q", got, CategorySchema)
			}

			unrelatedPointer := "/protocol"
			if test.pointer == unrelatedPointer {
				unrelatedPointer = "/version"
			}
			mutated := cloneFixtureValue(valid)
			mutated, err = setJSONPointer(mutated, unrelatedPointer, "counterexample")
			if err != nil {
				t.Fatalf("mutate unrelated pointer: %v", err)
			}
			if got := diffJSONPointers(valid, mutated); reflect.DeepEqual(got, []string{test.pointer}) {
				t.Fatalf("unrelated mutation at %s impersonated declared pointer %s", unrelatedPointer, test.pointer)
			}
		})
	}
}

func TestInvalidFixtureCategoryInventoryIsClosed(t *testing.T) {
	all := append(append([]ErrorCategory{}, invalidFixtureFileCategories...), invalidFixtureProgrammaticCategories...)
	seen := make(map[ErrorCategory]struct{}, len(all))
	for _, category := range all {
		if _, duplicate := seen[category]; duplicate {
			t.Fatalf("duplicate category inventory entry: %q", category)
		}
		seen[category] = struct{}{}
	}
	known := map[ErrorCategory]struct{}{
		CategoryEmpty: {}, CategoryOversize: {}, CategoryMalformed: {}, CategoryDuplicate: {},
		CategoryTrailing: {}, CategoryNonObject: {}, CategoryDepth: {}, CategoryNumeric: {},
		CategorySchema: {}, CategoryUnavailable: {},
	}
	if len(seen) != len(known) {
		t.Fatalf("category inventory size = %d, want %d", len(seen), len(known))
	}
	for category := range known {
		if _, ok := seen[category]; !ok {
			t.Errorf("category inventory omits %q", category)
		}
	}
}

func TestInvalidFixtureFamilyInventory(t *testing.T) {
	fixtures, err := discoverInvalidFixtureCorpus(protocolv1.FS, "fixtures/invalid", invalidFixtureFileCategories)
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]struct{}, len(fixtures))
	for _, fixture := range fixtures {
		ids[fixture.caseID] = struct{}{}
	}
	families := map[string][]string{
		"correlation identifiers":    {"correlation-"},
		"payload binding":            {"payload-binding-"},
		"unknown fields":             {"unknown-"},
		"unsupported discriminators": {"unsupported-"},
		"negotiation contradictions": {"negotiation-"},
		"heartbeat requirements":     {"heartbeat-"},
		"request safety":             {"request-"},
		"result contradictions":      {"result-"},
		"error mappings":             {"error-mapping-", "error-status-mismatch", "error-retryable-mismatch", "error-details-mismatch"},
		"event discriminator":        {"event-"},
		"binary metadata":            {"binary-"},
		"timestamps":                 {"timestamp-"},
		"identifiers":                {"identifier-"},
		"sequence bounds":            {"sequence-"},
		"session identifier bounds":  {"session-id-"},
		"attended safety gates":      {"request-attended-"},
		"isolated safety gates":      {"request-isolated-"},
		"request visibility binding": {"request-target-"},
		"screenshot result binding":  {"result-screenshot-"},
	}
	for family, prefixes := range families {
		found := false
		for id := range ids {
			for _, prefix := range prefixes {
				if strings.HasPrefix(id, prefix) {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("invalid fixture inventory has no case for %s", family)
		}
	}
	errorCodes := []string{
		"expired", "revoked", "duplicate", "out-of-order", "missing-capability", "unknown-field",
		"oversize", "stale-target", "unsupported-version", "mode-required", "capability-denied",
		"target-unavailable", "sensitive-surface", "invalid-envelope", "liveness-timeout",
	}
	for _, code := range errorCodes {
		if _, ok := ids["error-mapping-"+code+"-mismatch"]; !ok {
			t.Errorf("error mapping inventory omits code %s", code)
		}
	}
}

func TestErrorMappingMismatchFixturesAreSingleScalarNegatives(t *testing.T) {
	errorMappingCodes := map[string]string{
		"error-mapping-capability-denied-mismatch.json":   "capability_denied",
		"error-mapping-duplicate-mismatch.json":           "duplicate",
		"error-mapping-expired-mismatch.json":             "expired",
		"error-mapping-invalid-envelope-mismatch.json":    "invalid_envelope",
		"error-mapping-liveness-timeout-mismatch.json":    "liveness_timeout",
		"error-mapping-missing-capability-mismatch.json":  "missing_capability",
		"error-mapping-mode-required-mismatch.json":       "mode_required",
		"error-mapping-out-of-order-mismatch.json":        "out_of_order",
		"error-mapping-oversize-mismatch.json":            "oversize",
		"error-mapping-revoked-mismatch.json":             "revoked",
		"error-mapping-sensitive-surface-mismatch.json":   "sensitive_surface",
		"error-mapping-stale-target-mismatch.json":        "stale_target",
		"error-mapping-target-unavailable-mismatch.json":  "target_unavailable",
		"error-mapping-unknown-field-mismatch.json":       "unknown_field",
		"error-mapping-unsupported-version-mismatch.json": "unsupported_version",
	}
	if len(errorMappingIsolationCases) != 15 || len(errorMappingCodes) != len(errorMappingIsolationCases) {
		t.Fatalf("error mapping mismatch table has %d entries, want 15", len(errorMappingIsolationCases))
	}

	validator := MustNewValidator()
	for _, test := range errorMappingIsolationCases {
		t.Run(test.filename, func(t *testing.T) {
			expectedCode, ok := errorMappingCodes[test.filename]
			if !ok {
				t.Fatalf("missing expected error code for %s", test.filename)
			}
			candidateRaw, err := fs.ReadFile(protocolv1.FS, "fixtures/invalid/schema/"+test.filename)
			if err != nil {
				t.Fatalf("read candidate: %v", err)
			}
			validRaw, err := fs.ReadFile(protocolv1.FS, "fixtures/valid/"+test.validFilename)
			if err != nil {
				t.Fatalf("read valid counterpart: %v", err)
			}

			candidate, err := decodeFixtureInstance(candidateRaw)
			if err != nil {
				t.Fatalf("decode candidate: %v", err)
			}
			valid, err := decodeFixtureInstance(validRaw)
			if err != nil {
				t.Fatalf("decode valid counterpart: %v", err)
			}
			for name, instance := range map[string]any{"candidate": candidate, "valid": valid} {
				actualCode, present := jsonPointer(instance, "/payload/code")
				if !present || actualCode != expectedCode {
					t.Fatalf("%s code = %v, want %q", name, actualCode, expectedCode)
				}
			}

			validScalar, ok := jsonPointer(valid, test.pointer)
			if !ok {
				t.Fatalf("valid counterpart is missing %s", test.pointer)
			}
			candidateScalar, ok := jsonPointer(candidate, test.pointer)
			if !ok {
				t.Fatalf("candidate is missing %s", test.pointer)
			}
			if _, ok := validScalar.(bool); !ok {
				t.Fatalf("valid scalar at %s has type %T, want bool", test.pointer, validScalar)
			}
			if _, ok := candidateScalar.(bool); !ok {
				t.Fatalf("candidate scalar at %s has type %T, want bool", test.pointer, candidateScalar)
			}
			if reflect.DeepEqual(candidateScalar, validScalar) {
				t.Fatalf("candidate scalar at %s was not mutated", test.pointer)
			}
			if got := diffJSONPointers(valid, candidate); !reflect.DeepEqual(got, []string{test.pointer}) {
				t.Fatalf("candidate differs from valid at %v, want only %s", got, test.pointer)
			}
			if err := validator.Validate(validRaw); err != nil {
				t.Fatalf("valid counterpart rejected: %v", err)
			}
			if err := validator.Validate(candidateRaw); !errors.Is(err, ErrSchema) {
				t.Fatalf("candidate validation error = %v, want schema", err)
			}

			candidate, err = setJSONPointer(candidate, test.pointer, validScalar)
			if err != nil {
				t.Fatalf("restore %s: %v", test.pointer, err)
			}
			if !reflect.DeepEqual(candidate, valid) {
				t.Fatalf("restored candidate differs structurally from valid counterpart")
			}
			restoredRaw, err := json.Marshal(candidate)
			if err != nil {
				t.Fatalf("marshal restored candidate: %v", err)
			}
			if err := validator.Validate(restoredRaw); err != nil {
				t.Fatalf("restored candidate rejected: %v", err)
			}
		})
	}
}

func diffJSONPointers(want, got any) []string {
	return diffJSONPointersAt("", want, got)
}

func cloneFixtureValue(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	clone, err := decodeFixtureInstance(raw)
	if err != nil {
		panic(err)
	}
	return clone
}

func restoreJSONPointer(candidate, valid any, pointer string) (any, error) {
	if pointer == "" {
		return cloneFixtureValue(valid), nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return candidate, fmt.Errorf("pointer %q does not start with slash", pointer)
	}
	validValue, validPresent, err := lookupJSONPointer(valid, pointer)
	if err != nil {
		return candidate, err
	}
	_, candidatePresent, err := lookupJSONPointer(candidate, pointer)
	if err != nil {
		return candidate, err
	}
	if !validPresent && !candidatePresent {
		return candidate, fmt.Errorf("pointer %q is missing from both documents", pointer)
	}

	tokens := strings.Split(pointer[1:], "/")
	current := candidate
	for component, rawToken := range tokens[:len(tokens)-1] {
		token := unescapeJSONPointerToken(rawToken)
		switch value := current.(type) {
		case map[string]any:
			next, ok := value[token]
			if !ok {
				return candidate, fmt.Errorf("pointer %q is missing at component %d", pointer, component)
			}
			current = next
		case []any:
			arrayIndex, err := parseJSONPointerArrayIndex(rawToken, pointer, component, len(value))
			if err != nil {
				return candidate, err
			}
			current = value[arrayIndex]
		default:
			return candidate, fmt.Errorf("pointer %q traverses %T at component %d", pointer, current, component)
		}
	}

	lastRawToken := tokens[len(tokens)-1]
	lastToken := unescapeJSONPointerToken(lastRawToken)
	switch value := current.(type) {
	case map[string]any:
		if validPresent {
			value[lastToken] = cloneFixtureValue(validValue)
		} else if candidatePresent {
			delete(value, lastToken)
		} else {
			return candidate, fmt.Errorf("pointer %q is missing from candidate", pointer)
		}
	case []any:
		arrayIndex, err := parseJSONPointerArrayIndex(lastRawToken, pointer, len(tokens)-1, len(value))
		if err != nil {
			return candidate, err
		}
		if !validPresent {
			return candidate, fmt.Errorf("pointer %q cannot remove array element at component %d", pointer, len(tokens)-1)
		}
		value[arrayIndex] = cloneFixtureValue(validValue)
	default:
		return candidate, fmt.Errorf("pointer %q parent is %T", pointer, current)
	}
	return candidate, nil
}

func diffJSONPointersAt(pointer string, want, got any) []string {
	if reflect.DeepEqual(want, got) {
		return nil
	}
	wantObject, wantIsObject := want.(map[string]any)
	gotObject, gotIsObject := got.(map[string]any)
	if wantIsObject || gotIsObject {
		if !wantIsObject || !gotIsObject {
			return []string{pointer}
		}
		keys := make(map[string]struct{}, len(wantObject)+len(gotObject))
		for key := range wantObject {
			keys[key] = struct{}{}
		}
		for key := range gotObject {
			keys[key] = struct{}{}
		}
		sortedKeys := make([]string, 0, len(keys))
		for key := range keys {
			sortedKeys = append(sortedKeys, key)
		}
		sort.Strings(sortedKeys)
		var differences []string
		for _, key := range sortedKeys {
			childPointer := pointer + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
			wantValue, wantOK := wantObject[key]
			gotValue, gotOK := gotObject[key]
			if !wantOK || !gotOK {
				// A whole added or removed object subtree is one causal delta at
				// the field where that subtree is attached.
				differences = append(differences, childPointer)
				continue
			}
			differences = append(differences, diffJSONPointersAt(childPointer, wantValue, gotValue)...)
		}
		return differences
	}
	wantArray, wantIsArray := want.([]any)
	gotArray, gotIsArray := got.([]any)
	if wantIsArray || gotIsArray {
		if !wantIsArray || !gotIsArray || len(wantArray) != len(gotArray) {
			return []string{pointer}
		}
		var differences []string
		for index := range wantArray {
			differences = append(differences, diffJSONPointersAt(fmt.Sprintf("%s/%d", pointer, index), wantArray[index], gotArray[index])...)
		}
		return differences
	}
	return []string{pointer}
}

// setJSONPointer and restoreJSONPointer support pointers emitted by
// diffJSONPointers; they do not claim to accept arbitrary external JSON
// Pointer input.
func setJSONPointer(document any, pointer string, value any) (any, error) {
	if pointer == "" {
		return cloneFixtureValue(value), nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return document, fmt.Errorf("pointer %q does not start with slash", pointer)
	}
	current := document
	tokens := strings.Split(pointer[1:], "/")
	for component, rawToken := range tokens {
		token := unescapeJSONPointerToken(rawToken)
		last := component == len(tokens)-1
		switch container := current.(type) {
		case map[string]any:
			if last {
				container[token] = cloneFixtureValue(value)
				return document, nil
			}
			next, ok := container[token]
			if !ok {
				return document, fmt.Errorf("pointer %q is missing at component %d", pointer, component)
			}
			current = next
		case []any:
			arrayIndex, err := parseJSONPointerArrayIndex(rawToken, pointer, component, len(container))
			if err != nil {
				return document, err
			}
			if last {
				container[arrayIndex] = cloneFixtureValue(value)
				return document, nil
			}
			current = container[arrayIndex]
		default:
			return document, fmt.Errorf("pointer %q traverses %T at component %d", pointer, current, component)
		}
	}
	return document, fmt.Errorf("pointer %q is empty", pointer)
}

func lookupJSONPointer(document any, pointer string) (any, bool, error) {
	if pointer == "" {
		return document, true, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false, fmt.Errorf("pointer %q does not start with slash", pointer)
	}
	current := document
	for component, rawToken := range strings.Split(pointer[1:], "/") {
		token := unescapeJSONPointerToken(rawToken)
		switch value := current.(type) {
		case map[string]any:
			next, ok := value[token]
			if !ok {
				return nil, false, nil
			}
			current = next
		case []any:
			arrayIndex, err := parseJSONPointerArrayIndex(rawToken, pointer, component, len(value))
			if err != nil {
				return nil, false, err
			}
			current = value[arrayIndex]
		default:
			return nil, false, fmt.Errorf("pointer %q traverses %T at component %d", pointer, current, component)
		}
	}
	return current, true, nil
}

func unescapeJSONPointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
}

func parseJSONPointerArrayIndex(token, pointer string, component, length int) (int, error) {
	if token == "" {
		return 0, fmt.Errorf("pointer %q has an empty array index at component %d", pointer, component)
	}
	if len(token) > 1 && token[0] == '0' {
		return 0, fmt.Errorf("pointer %q has non-canonical array index %q at component %d", pointer, token, component)
	}

	maxInt := int(^uint(0) >> 1)
	arrayIndex := 0
	for _, digit := range token {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("pointer %q has invalid array index %q at component %d: want canonical non-negative decimal", pointer, token, component)
		}
		digitValue := int(digit - '0')
		if arrayIndex > (maxInt-digitValue)/10 {
			return 0, fmt.Errorf("pointer %q array index %q overflows int at component %d", pointer, token, component)
		}
		arrayIndex = arrayIndex*10 + digitValue
	}
	if arrayIndex >= length {
		return 0, fmt.Errorf("pointer %q array index %q is out of range at component %d (length %d)", pointer, token, component, length)
	}
	return arrayIndex, nil
}

func TestJSONPointerArrayRegression(t *testing.T) {
	raw, err := fs.ReadFile(protocolv1.FS, "fixtures/valid/negotiation-offer.json")
	if err != nil {
		t.Fatal(err)
	}
	original, err := decodeFixtureInstance(raw)
	if err != nil {
		t.Fatalf("decode negotiation offer: %v", err)
	}
	candidate := cloneFixtureValue(original)
	const pointer = "/payload/modes/0"

	candidate, err = setJSONPointer(candidate, pointer, "sandbox")
	if err != nil {
		t.Fatalf("set %s: %v", pointer, err)
	}
	if got, ok := jsonPointer(candidate, pointer); !ok || got != "sandbox" {
		t.Fatalf("candidate at %s = %v, want sandbox", pointer, got)
	}
	if got := diffJSONPointers(original, candidate); !reflect.DeepEqual(got, []string{pointer}) {
		t.Fatalf("candidate differs at %v, want only %s", got, pointer)
	}

	candidate, err = restoreJSONPointer(candidate, original, pointer)
	if err != nil {
		t.Fatalf("restore %s: %v", pointer, err)
	}
	if !reflect.DeepEqual(candidate, original) {
		t.Fatalf("restored candidate differs structurally from original")
	}
	restoredRaw, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal restored candidate: %v", err)
	}
	if err := MustNewValidator().Validate(restoredRaw); err != nil {
		t.Fatalf("restored negotiation offer rejected: %v", err)
	}
}

func TestJSONPointerRootReplacement(t *testing.T) {
	tests := []struct {
		name     string
		before   any
		after    any
		wantDiff []string
	}{
		{name: "scalar", before: "before", after: "after", wantDiff: []string{""}},
		{name: "array length change", before: []any{"before"}, after: []any{"after", "extra"}, wantDiff: []string{""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("root replacement panicked: %v", recovered)
				}
			}()

			original := test.before
			candidate, err := setJSONPointer(original, "", test.after)
			if err != nil {
				t.Fatalf("set root: %v", err)
			}
			if got := diffJSONPointers(original, candidate); !reflect.DeepEqual(got, test.wantDiff) {
				t.Fatalf("root replacement diff = %v, want %v", got, test.wantDiff)
			}
			candidate, err = restoreJSONPointer(candidate, original, "")
			if err != nil {
				t.Fatalf("restore root: %v", err)
			}
			if !reflect.DeepEqual(candidate, original) {
				t.Fatalf("restored root = %#v, want %#v", candidate, original)
			}
			if got := diffJSONPointers(original, candidate); got != nil {
				t.Fatalf("restored root diff = %v, want no differences", got)
			}
		})
	}
}

func TestJSONPointerReplacementOwnsCompositeValue(t *testing.T) {
	tests := []struct {
		name        string
		document    any
		pointer     string
		mutate      func(any)
		want        any
		replacement any
	}{
		{
			name:        "root",
			document:    map[string]any{"original": true},
			pointer:     "",
			replacement: map[string]any{"nested": []any{"before"}},
			mutate: func(value any) {
				replacement := value.(map[string]any)
				replacement["nested"].([]any)[0] = "after"
			},
			want: map[string]any{"nested": []any{"before"}},
		},
		{
			name:        "object member",
			document:    map[string]any{"target": "old"},
			pointer:     "/target",
			replacement: map[string]any{"nested": []any{"before"}},
			mutate: func(value any) {
				replacement := value.(map[string]any)
				replacement["nested"].([]any)[0] = "after"
			},
			want: map[string]any{"target": map[string]any{"nested": []any{"before"}}},
		},
		{
			name:        "array element",
			document:    map[string]any{"items": []any{"old"}},
			pointer:     "/items/0",
			replacement: map[string]any{"nested": []any{"before"}},
			mutate: func(value any) {
				replacement := value.(map[string]any)
				replacement["nested"].([]any)[0] = "after"
			},
			want: map[string]any{"items": []any{map[string]any{"nested": []any{"before"}}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			replacement := test.replacement
			candidate, err := setJSONPointer(test.document, test.pointer, replacement)
			if err != nil {
				t.Fatalf("set %s: %v", test.pointer, err)
			}
			test.mutate(replacement)
			if !reflect.DeepEqual(candidate, test.want) {
				t.Fatalf("candidate after caller mutation = %#v, want %#v", candidate, test.want)
			}
		})
	}
}

func TestJSONPointerMissingObjectMember(t *testing.T) {
	tests := []struct {
		name       string
		want       map[string]any
		candidate  map[string]any
		restoreTo  map[string]any
		setMissing bool
	}{
		{
			name:       "insertion and deletion restoration",
			want:       map[string]any{"a": "before"},
			candidate:  map[string]any{"a": "before", "x": "after"},
			restoreTo:  map[string]any{"a": "before"},
			setMissing: true,
		},
		{
			name:      "deletion and insertion restoration",
			want:      map[string]any{"a": "before", "x": "after"},
			candidate: map[string]any{"a": "before"},
			restoreTo: map[string]any{"a": "before", "x": "after"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("missing object member panicked: %v", recovered)
				}
			}()

			candidate := cloneFixtureValue(test.candidate)
			if test.setMissing {
				var err error
				candidate, err = setJSONPointer(cloneFixtureValue(test.want), "/x", "after")
				if err != nil {
					t.Fatalf("set missing final member: %v", err)
				}
			}
			if got := diffJSONPointers(test.want, candidate); !reflect.DeepEqual(got, []string{"/x"}) {
				t.Fatalf("missing final member diff = %v, want [/x]", got)
			}

			restored, err := restoreJSONPointer(candidate, test.restoreTo, "/x")
			if err != nil {
				t.Fatalf("restore missing final member: %v", err)
			}
			if !reflect.DeepEqual(restored, test.restoreTo) {
				t.Fatalf("restored object = %#v, want %#v", restored, test.restoreTo)
			}
			if got := diffJSONPointers(test.restoreTo, restored); got != nil {
				t.Fatalf("restored object diff = %v, want no differences", got)
			}
		})
	}

	if _, err := setJSONPointer(map[string]any{}, "/missing/x", "value"); err == nil {
		t.Fatal("set created a missing intermediate object")
	}
}

func TestJSONPointerArrayIndexValidation(t *testing.T) {
	tests := []struct {
		name      string
		token     string
		errorText string
	}{
		{name: "empty", token: "", errorText: "empty array index"},
		{name: "signed", token: "-1", errorText: "invalid array index"},
		{name: "nondecimal", token: "1.0", errorText: "invalid array index"},
		{name: "leading zero", token: "01", errorText: "non-canonical array index"},
		{name: "overflow", token: strings.Repeat("9", 128), errorText: "overflows int"},
		{name: "out of range", token: "1", errorText: "out of range"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pointer := "/items/" + test.token
			document := map[string]any{"items": []any{"background"}}
			_, err := setJSONPointer(document, pointer, "sandbox")
			if err == nil || !strings.Contains(err.Error(), test.errorText) {
				t.Fatalf("set %s error = %v, want text %q", pointer, err, test.errorText)
			}

			candidate := map[string]any{"items": []any{"sandbox"}}
			valid := map[string]any{"items": []any{"background"}}
			_, err = restoreJSONPointer(candidate, valid, pointer)
			if err == nil || !strings.Contains(err.Error(), test.errorText) {
				t.Fatalf("restore %s error = %v, want text %q", pointer, err, test.errorText)
			}
		})
	}
}

func TestJSONPointerObjectTokenUnescaping(t *testing.T) {
	const pointer = "/object/a~1b~0c"
	original := map[string]any{"object": map[string]any{"a/b~c": "before"}}
	document, err := setJSONPointer(cloneFixtureValue(original), pointer, "after")
	if err != nil {
		t.Fatalf("set escaped object token: %v", err)
	}
	if got := document.(map[string]any)["object"].(map[string]any)["a/b~c"]; got != "after" {
		t.Fatalf("escaped object token = %v, want after", got)
	}
	if got := diffJSONPointers(original, document); !reflect.DeepEqual(got, []string{pointer}) {
		t.Fatalf("escaped object token diff = %v, want [%s]", got, pointer)
	}
	restored, err := restoreJSONPointer(document, original, pointer)
	if err != nil {
		t.Fatalf("restore escaped object token: %v", err)
	}
	if !reflect.DeepEqual(restored, original) {
		t.Fatalf("restored escaped object token = %#v, want %#v", restored, original)
	}
}

func TestDiscoverInvalidFixtureCorpusRejectsManifestDrift(t *testing.T) {
	required := []ErrorCategory{CategoryMalformed, CategorySchema}
	injectedReadError := errors.New("injected invalid fixture read failure")
	injectedInfoError := errors.New("injected invalid fixture info failure")
	injectedCategoryReadError := errors.New("injected invalid fixture category read failure")
	validCategories := fstest.MapFS{
		"fixtures/invalid/malformed_json/a.json": {Data: []byte(`{`), Mode: 0444},
		"fixtures/invalid/schema/b.json":         {Data: []byte(`{}`), Mode: 0444},
	}
	tests := []struct {
		name     string
		fsys     fs.FS
		required []ErrorCategory
		wantText string
		wantErr  error
	}{
		{name: "missing root", fsys: fstest.MapFS{}, required: required, wantText: "read invalid fixture root", wantErr: fs.ErrNotExist},
		{name: "empty root", fsys: fstest.MapFS{
			"fixtures/invalid": {Mode: fs.ModeDir | 0755},
		}, required: required, wantText: "invalid fixture root is empty"},
		{name: "category entry is a non-directory file", fsys: fstest.MapFS{
			"fixtures/invalid/malformed_json": {Data: []byte("not a directory"), Mode: 0444},
			"fixtures/invalid/schema/b.json":  {Data: []byte(`{}`), Mode: 0444},
		}, required: required, wantText: "not a category directory: malformed_json"},
		{name: "missing category", fsys: fstest.MapFS{
			"fixtures/invalid/malformed_json/a.json": {Data: []byte(`{`), Mode: 0444},
		}, required: required, wantText: "missing invalid fixture category directory: schema"},
		{name: "empty category", fsys: fstest.MapFS{
			"fixtures/invalid/malformed_json": {Mode: fs.ModeDir | 0755},
			"fixtures/invalid/schema/b.json":  {Data: []byte(`{}`), Mode: 0444},
		}, required: required, wantText: "invalid fixture category is empty: malformed_json"},
		{name: "unknown category", fsys: fstest.MapFS{
			"fixtures/invalid/malformed_json/a.json": {Data: []byte(`{`), Mode: 0444},
			"fixtures/invalid/new":                   {Mode: fs.ModeDir | 0755},
			"fixtures/invalid/schema/b.json":         {Data: []byte(`{}`), Mode: 0444},
		}, required: required, wantText: "unknown invalid fixture category: new"},
		{name: "duplicate required category allowlist", fsys: validCategories, required: []ErrorCategory{CategoryMalformed, CategoryMalformed}, wantText: `allowlist duplicates category "malformed_json"`},
		{name: "empty required category allowlist entry", fsys: validCategories, required: []ErrorCategory{""}, wantText: "allowlist contains empty category"},
		{name: "non-JSON entry", fsys: fstest.MapFS{
			"fixtures/invalid/malformed_json/a.txt": {Data: []byte(`{`), Mode: 0444},
			"fixtures/invalid/schema/b.json":        {Data: []byte(`{}`), Mode: 0444},
		}, required: required, wantText: "non-JSON invalid fixture entry in malformed_json: a.txt"},
		{name: "empty-name-equivalent entry", fsys: fstest.MapFS{
			"fixtures/invalid/malformed_json/.json": {Data: []byte(`{`), Mode: 0444},
			"fixtures/invalid/schema/b.json":        {Data: []byte(`{}`), Mode: 0444},
		}, required: required, wantText: "non-JSON invalid fixture entry in malformed_json: .json"},
		{name: "file Entry.Info failure", fsys: failingEntryInfoFS{
			FS: validCategories, directory: "fixtures/invalid/malformed_json", entry: "a.json", err: injectedInfoError,
		}, required: required, wantText: "invalid fixture malformed_json/a.json info", wantErr: injectedInfoError},
		{name: "category ReadDir failure", fsys: failingReadDirFS{
			FS: validCategories, directory: "fixtures/invalid/malformed_json", err: injectedCategoryReadError,
		}, required: required, wantText: `read invalid fixture category "malformed_json"`, wantErr: injectedCategoryReadError},
		{name: "ReadFile failure", fsys: failingReadFS{
			FS: validCategories, name: "fixtures/invalid/malformed_json/a.json", err: injectedReadError,
		}, required: required, wantText: "invalid fixture malformed_json/a.json", wantErr: injectedReadError},
		{name: "nonregular fixture", fsys: fstest.MapFS{
			"fixtures/invalid/malformed_json/a.json": {Data: []byte(`{`), Mode: fs.ModeNamedPipe | 0666},
			"fixtures/invalid/schema/b.json":         {Data: []byte(`{}`), Mode: 0444},
		}, required: required, wantText: "invalid fixture malformed_json/a.json is not a regular file"},
		{name: "unreadable fixture", fsys: fstest.MapFS{
			"fixtures/invalid/malformed_json/a.json": {Data: []byte(`{`), Mode: 0000},
			"fixtures/invalid/schema/b.json":         {Data: []byte(`{}`), Mode: 0444},
		}, required: required, wantText: "invalid fixture malformed_json/a.json is unreadable"},
		{name: "empty fixture", fsys: fstest.MapFS{
			"fixtures/invalid/malformed_json/a.json": {Mode: 0444},
			"fixtures/invalid/schema/b.json":         {Data: []byte(`{}`), Mode: 0444},
		}, required: required, wantText: "invalid fixture malformed_json/a.json is empty"},
		{name: "duplicate case ID", fsys: fstest.MapFS{
			"fixtures/invalid/malformed_json/same.json": {Data: []byte(`{`), Mode: 0444},
			"fixtures/invalid/schema/same.json":         {Data: []byte(`{}`), Mode: 0444},
		}, required: required, wantText: `duplicate invalid fixture case ID "same"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := discoverInvalidFixtureCorpus(test.fsys, "fixtures/invalid", test.required)
			if err == nil {
				t.Fatal("manifest drift was accepted")
			}
			if !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %q, want text fragment %q", err, test.wantText)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want identity %v", err, test.wantErr)
			}
		})
	}

	duplicateCategoryFS := duplicateEntryFS{FS: validCategories, directory: "fixtures/invalid", entry: "malformed_json"}
	_, err := discoverInvalidFixtureCorpus(duplicateCategoryFS, "fixtures/invalid", required)
	if err == nil || !strings.Contains(err.Error(), "duplicate invalid fixture category: malformed_json") {
		t.Fatalf("duplicate category error = %v, want duplicate category text", err)
	}
}

func TestInvalidFixtureDiscoveryIsDeterministic(t *testing.T) {
	base := fstest.MapFS{
		"fixtures/invalid/schema/y.json":         {Data: []byte(`{}`), Mode: 0444},
		"fixtures/invalid/malformed_json/z.json": {Data: []byte(`{`), Mode: 0444},
		"fixtures/invalid/schema/b.json":         {Data: []byte(`{}`), Mode: 0444},
		"fixtures/invalid/malformed_json/a.json": {Data: []byte(`{`), Mode: 0444},
	}
	fsys := reversedReadDirFS{ReadDirFS: base}
	rootEntries, err := fs.ReadDir(fsys, "fixtures/invalid")
	if err != nil {
		t.Fatalf("read reversed invalid fixture root: %v", err)
	}
	if got := []string{rootEntries[0].Name(), rootEntries[1].Name()}; !reflect.DeepEqual(got, []string{"schema", "malformed_json"}) {
		t.Fatalf("reversed invalid fixture root order = %v, want [schema malformed_json]", got)
	}
	for category, want := range map[string][]string{
		"schema":         {"y.json", "b.json"},
		"malformed_json": {"z.json", "a.json"},
	} {
		entries, err := fs.ReadDir(fsys, "fixtures/invalid/"+category)
		if err != nil {
			t.Fatalf("read reversed invalid fixture category %q: %v", category, err)
		}
		got := []string{entries[0].Name(), entries[1].Name()}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("reversed invalid fixture %s order = %v, want %v", category, got, want)
		}
	}

	fixtures, err := discoverInvalidFixtureCorpus(fsys, "fixtures/invalid", []ErrorCategory{CategorySchema, CategoryMalformed})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(fixtures))
	for i, fixture := range fixtures {
		got[i] = fmt.Sprintf("%s/%s", fixture.category, fixture.name)
	}
	want := []string{
		"malformed_json/a.json",
		"malformed_json/z.json",
		"schema/b.json",
		"schema/y.json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("invalid fixture order = %v, want %v", got, want)
	}
}
