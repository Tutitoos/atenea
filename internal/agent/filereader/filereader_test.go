package filereader

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainReadsARepositoryFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "SPEC.md")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	input := assignment{}
	input.Task.Files = []string{"SPEC.md"}
	input.Context = map[string]json.RawMessage{
		"repository": json.RawMessage(`{"root":"` + root + `"}`),
	}
	var output bytes.Buffer
	if err := Main(bytes.NewReader(mustJSON(t, input)), &output); err != nil {
		t.Fatal(err)
	}

	var got report
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "ok" || got.Reason != nil {
		t.Fatalf("report = %#v, want successful report", got)
	}
	if got.Result["path"] != "SPEC.md" || got.Result["lines"] != float64(2) || got.Result["bytes"] != float64(13) || got.Result["content"] != "first\nsecond\n" {
		t.Fatalf("result = %#v, want path, line, byte and content fields", got.Result)
	}
	if len(got.Discovered) != 1 || !strings.Contains(got.Discovered[0].Note, "13 bytes over 2 lines") {
		t.Fatalf("discovered = %#v, want file summary", got.Discovered)
	}
}

func TestMainReportsControlledFailures(t *testing.T) {
	dir := t.TempDir()
	large := filepath.Join(dir, "large")
	if err := os.WriteFile(large, bytes.Repeat([]byte("x"), maxFile+1), 0o600); err != nil {
		t.Fatal(err)
	}
	badText := filepath.Join(dir, "binary")
	if err := os.WriteFile(badText, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		file        string
		wantVerdict string
		wantKind    string
	}{
		{name: "no file", wantVerdict: "failed", wantKind: "invalid_input"},
		{name: "missing", file: filepath.Join(dir, "missing"), wantVerdict: "failed", wantKind: "not_found"},
		{name: "directory", file: dir, wantVerdict: "failed", wantKind: "invalid_input"},
		{name: "large", file: large, wantVerdict: "incomplete", wantKind: "invalid_input"},
		{name: "binary", file: badText, wantVerdict: "incomplete", wantKind: "invalid_input"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := assignment{}
			if tt.file != "" {
				input.Task.Files = []string{tt.file}
			}
			var output bytes.Buffer
			if err := Main(bytes.NewReader(mustJSON(t, input)), &output); err != nil {
				t.Fatal(err)
			}
			var got report
			if err := json.Unmarshal(output.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Verdict != tt.wantVerdict || got.Reason == nil || got.Reason.Kind != tt.wantKind {
				t.Fatalf("report = %#v, want verdict %q and reason %q", got, tt.wantVerdict, tt.wantKind)
			}
		})
	}
}

func TestMainRejectsUnreadableAssignments(t *testing.T) {
	var output bytes.Buffer
	err := Main(strings.NewReader("{"), &output)
	if err == nil || !strings.Contains(err.Error(), "assignment is not readable") {
		t.Fatalf("error = %v, want malformed assignment error", err)
	}

	var input assignment
	input.Context = map[string]json.RawMessage{"repository": json.RawMessage("not-json")}
	if got := repositoryRoot(input); got != "" {
		t.Fatalf("repositoryRoot = %q, want empty root for malformed context", got)
	}
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		body string
		want int
	}{
		{body: "", want: 0},
		{body: "one", want: 1},
		{body: "one\n", want: 1},
		{body: "one\ntwo", want: 2},
	}
	for _, tt := range tests {
		if got := countLines([]byte(tt.body)); got != tt.want {
			t.Errorf("countLines(%q) = %d, want %d", tt.body, got, tt.want)
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
