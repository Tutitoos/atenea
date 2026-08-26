package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// withCapability writes a settings file holding one capability and nothing
// else, which is a complete and valid file: the catalog is replaced, not
// merged, so an empty one is an Atenea that knows nothing.
func withCapability(t *testing.T, body string) (config.Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atenea.toml")
	file := "contract = \"" + contract.Version{Major: 3, Minor: 5}.String() + "\"\n\n" + body
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return config.Load(path)
}

const subjectCapability = `[[capability]]
id = "web.fetch"
version = "1.0.0"
summary = "Fetch one web page."
semantics = "One page, one answer."
effects = ["read", "external"]
%s

  [[capability.input]]
  name = "url"
  type = "string"
  required = true
  summary = "The page to fetch."

  [[capability.input]]
  name = "depth"
  type = "int"
  required = false
  summary = "Not a string."

  [[capability.output]]
  name = "page"
  type = "string"
  required = true
  summary = "The body."
`

func capabilityWith(declaration string) string {
	return strings.Replace(subjectCapability, "%s", declaration, 1)
}

// A subject key that means nothing does not fail at call time. It files health
// and cost under nonsense and lets the funnel rank as if that were fine --
// there is no run to inspect and no error anybody would notice. So the whole
// declaration is checked where a mistake is still cheap.
func TestAMeaninglessSubjectIsRefusedAtTheDoor(t *testing.T) {
	cases := []struct {
		name, declaration, want string
	}{
		{
			"an input that does not exist",
			"subject_from = \"host\"\nsubject_kind = \"url_host\"",
			"not one of its inputs",
		},
		{
			"an input that is not a string",
			"subject_from = \"depth\"\nsubject_kind = \"url_host\"",
			"a subject is read from a string",
		},
		{
			"a kind nobody implements",
			"subject_from = \"url\"\nsubject_kind = \"hostname\"",
			"unknown subject kind",
		},
		{
			"only the input",
			"subject_from = \"url\"",
			"only half a subject",
		},
		{
			"only the kind",
			"subject_kind = \"url_host\"",
			"only half a subject",
		},
		{
			"an empty kind beside a named input",
			"subject_from = \"url\"\nsubject_kind = \"\"",
			"only half a subject",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := withCapability(t, capabilityWith(tc.declaration))
			if err == nil {
				t.Fatal("the settings loaded with a subject that means nothing")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestADeclaredSubjectSurvivesTheLoad(t *testing.T) {
	cfg, err := withCapability(t, capabilityWith("subject_from = \"url\"\nsubject_kind = \"url_host\""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Capabilities) != 1 {
		t.Fatalf("capabilities = %d, want 1", len(cfg.Capabilities))
	}
	got := cfg.Capabilities[0]
	if got.SubjectFrom != "url" || got.SubjectKind != contract.SubjectURLHost {
		t.Fatalf("subject = (%q, %v), want (url, url_host)", got.SubjectFrom, got.SubjectKind)
	}
	// And it derives, which is the only reason the declaration is worth
	// keeping across the load.
	if host := got.Subject(map[string]any{"url": "https://Example.COM/a"}); host != "example.com" {
		t.Errorf("Subject = %q, want example.com", host)
	}
}

// Declaring nothing is the ordinary case and must stay silent.
func TestNoSubjectDeclarationIsNotAnError(t *testing.T) {
	cfg, err := withCapability(t, capabilityWith(""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Capabilities[0]; got.SubjectFrom != "" || got.SubjectKind != contract.SubjectNone {
		t.Errorf("subject = (%q, %v), want nothing", got.SubjectFrom, got.SubjectKind)
	}
}
