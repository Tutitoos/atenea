package passthrough

import "testing"

func TestHeaderAnnotationsUseStrictSchemaAndEncoding(t *testing.T) {
	annotations, valid := headerAnnotations([]byte(`{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"},"count":{"type":"integer","x-mcp-header":"Count"}}}`))
	if !valid || len(annotations) != 2 {
		t.Fatalf("annotations = %#v valid=%v", annotations, valid)
	}
	values, err := headerValues(annotations, map[string]any{"region": "Madrid centro", "count": float64(3)})
	if err != nil || values["Count"] != "3" || values["Region"] != "Madrid centro" {
		t.Fatalf("values = %#v, %v", values, err)
	}
	if _, valid := headerAnnotations([]byte(`{"type":"object","properties":{"ratio":{"type":"number","x-mcp-header":"Ratio"}}}`)); valid {
		t.Fatal("number annotation accepted")
	}
	if _, valid := headerAnnotations([]byte(`{"type":"object","properties":{"x":{"type":"string","x-mcp-header":"Bad(Header"}}}`)); valid {
		t.Fatal("non-tchar annotation accepted")
	}
	if _, err := headerValues(map[string]string{"count": "Count"}, map[string]any{"count": float64(9007199254740992)}); err == nil {
		t.Fatal("unsafe integer accepted")
	}
}

func TestZeroTTLRetainsHeaderMetadata(t *testing.T) {
	var cache catalogCache
	_, err := cache.get(t.Context(), 1, func() ([]Tool, cacheHint, error) {
		return []Tool{{Name: "demo", HeaderMap: map[string]string{"region": "Region"}}}, cacheHint{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := cache.headerMap("demo")["region"]; got != "Region" {
		t.Fatalf("header metadata = %#v", cache.headerMap("demo"))
	}
}

func TestModernInputSchemaRequiresObject(t *testing.T) {
	for _, schema := range []string{"", `null`, `[]`, `{"type":"array"}`, `{"type":"string"}`, `{"properties":{}}`} {
		if modernInputSchema([]byte(schema)) {
			t.Fatalf("accepted non-object modern inputSchema %s", schema)
		}
	}
	if !modernInputSchema([]byte(`{"type":"object","properties":{}}`)) {
		t.Fatal("rejected object modern inputSchema")
	}
}
