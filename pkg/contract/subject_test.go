package contract_test

import (
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func urlKeyed() contract.Capability {
	return contract.Capability{
		ID: "web.fetch", SubjectFrom: "url", SubjectKind: contract.SubjectURLHost,
		Inputs: []contract.Field{{Name: "url", Type: contract.TypeString, Required: true}},
	}
}

// A subject is a grouping key, so two calls that mean the same place have to
// produce the same string. "Whatever the caller typed" does not: case, scheme
// and port all vary without the place varying.
func TestOneSiteIsOneSubjectHoweverItWasTyped(t *testing.T) {
	for _, raw := range []string{
		"https://example.com/",
		"https://Example.COM/a",
		"http://example.com/b?q=1#frag",
		"https://example.com:8443/c",
	} {
		if got := urlKeyed().Subject(map[string]any{"url": raw}); got != "example.com" {
			t.Errorf("Subject(%q) = %q, want example.com", raw, got)
		}
	}
	// And two different places stay apart, which is the half that makes the
	// first half worth having.
	if urlKeyed().Subject(map[string]any{"url": "https://other.example/"}) == "example.com" {
		t.Error("two different hosts collapsed into one subject")
	}
}

// Nothing derivable is the empty subject and not an error.
//
// A subject is a grouping key, not a permission. A call nobody can key is
// filed the way every call was filed before subjects existed -- a worse
// baseline, not a wrong answer. Refusing instead would let a malformed url
// break a fetch that the destination gate is about to refuse anyway, for its
// own better reasons and with a better message.
func TestAnUnreadableSubjectIsEmptyRatherThanAnError(t *testing.T) {
	for _, payload := range []map[string]any{
		nil,
		{},
		{"url": ""},
		{"url": "   "},
		{"url": "://nonsense"},
		{"url": 42},
		{"url": []any{"https://example.com/"}},
		{"other": "https://example.com/"},
	} {
		if got := urlKeyed().Subject(payload); got != "" {
			t.Errorf("Subject(%v) = %q, want empty", payload, got)
		}
	}
}

// Most capabilities have no subject, and they must never grow one by accident
// just because their payload happens to carry a url-shaped field.
func TestACapabilityWithNoDeclarationHasNoSubject(t *testing.T) {
	plain := contract.Capability{
		ID:     "code.search",
		Inputs: []contract.Field{{Name: "url", Type: contract.TypeString}},
	}
	if got := plain.Subject(map[string]any{"url": "https://example.com/"}); got != "" {
		t.Errorf("a capability declaring no subject derived %q", got)
	}
	// Half a declaration derives nothing either. The settings loader refuses
	// that shape outright, and this is the belt under that brace.
	for _, half := range []contract.Capability{
		{ID: "x", SubjectFrom: "url"},
		{ID: "x", SubjectKind: contract.SubjectURLHost},
	} {
		if got := half.Subject(map[string]any{"url": "https://example.com/"}); got != "" {
			t.Errorf("half a declaration derived %q", got)
		}
	}
}

func TestSubjectKindsRoundTripByName(t *testing.T) {
	for _, name := range []string{"", "url_host"} {
		kind, err := contract.ParseSubjectKind(name)
		if err != nil {
			t.Fatalf("ParseSubjectKind(%q): %v", name, err)
		}
		if kind.String() != name {
			t.Errorf("%q parsed to a kind that names itself %q", name, kind.String())
		}
	}
	if _, err := contract.ParseSubjectKind("hostname"); err == nil {
		t.Error("an unknown kind was accepted")
	}
}
