package statusline_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The atenea widget wins the client's `sidebar_footer` slot, and that slot is
// declared `mode:"single_winner"`: the winner replaces everything the host had
// put there. So this binary now draws three lines the client used to draw itself
// -- the project path, the client's version, and one of ours -- and the client is
// free to change its own footer in any release.
//
// That is the failure this file exists to make loud. A release that adds a line
// to the footer would otherwise delete it from the screen in silence: no error,
// no log, just something that used to be there and is not. So the shape of the
// host's footer is pinned as an inventory and compared against the client
// installed on this machine. When they change it, this fails and names what
// moved, and somebody decides whether to reproduce it or hand the slot back.
//
// It is deliberately blunt: the inventory includes every string literal in that
// component, so a change with no visible effect can fail this too. That costs one
// look at a diff. The other direction costs a line off the user's screen and no
// way to find out.
const (
	// The component is a plugin registration inside the client's bundle, and its
	// id is a literal in that bundle. Everything from there to the registration
	// call that follows it is the component.
	footerMarker   = `"internal:sidebar-footer"`
	footerEnd      = `slots.register(`
	footerWindow   = 16 << 10
	contractGolden = "testdata/opencode-footer.json"
)

var updateGolden = flag.Bool("update-host-footer", false, "rewrite the pinned host footer contract from the installed client")

// footerContract is what the host's footer component is made of, in the terms
// that survive minification. Identifiers in that bundle are renamed on every
// build, so nothing here names one: string literals, property paths reached
// through `.api.`, properties read off a zero-argument accessor, and how many
// elements of each tag get built.
type footerContract struct {
	Client string `json:"client"`
	// The client version this inventory was taken from. Informational: a version
	// bump with an unchanged footer is not a failure.
	MeasuredOn string `json:"measured_on_version"`
	// Every string literal in the component, in source order, duplicates kept: a
	// row added with text that already appears elsewhere still shows up here.
	Literals []string `json:"literals"`
	// Property paths read off the plugin api, e.g. `app.version`.
	APIReads []string `json:"api_reads"`
	// Properties read off a zero-argument accessor: theme keys and the fields of
	// the component's own memos.
	AccessorReads []string `json:"accessor_reads"`
	// Element constructions by tag.
	Elements map[string]int `json:"elements"`
}

var (
	stringLiteral = regexp.MustCompile(`"((?:[^"\\\n]|\\.)*)"`)
	apiRead       = regexp.MustCompile(`\.api\.((?:\w+\.)*\w+)`)
	accessorRead  = regexp.MustCompile(`\w+\(\)\.([A-Za-z]\w*)`)
	elementBuild  = regexp.MustCompile(`\w+\("(box|text|span|b)"\)`)
)

func contractFrom(source string) footerContract {
	contract := footerContract{
		Client:   "opencode",
		Elements: map[string]int{},
	}
	for _, match := range stringLiteral.FindAllStringSubmatch(source, -1) {
		contract.Literals = append(contract.Literals, match[1])
	}
	contract.APIReads = uniqueSorted(apiRead, source)
	contract.AccessorReads = uniqueSorted(accessorRead, source)
	for _, match := range elementBuild.FindAllStringSubmatch(source, -1) {
		contract.Elements[match[1]]++
	}
	return contract
}

func uniqueSorted(pattern *regexp.Regexp, source string) []string {
	seen := map[string]bool{}
	for _, match := range pattern.FindAllStringSubmatch(source, -1) {
		seen[match[1]] = true
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// drift reports what changed between the pinned inventory and the installed one,
// in the order somebody reading a failure wants it: content first, then the api
// surface, then the element count.
func drift(want, got footerContract) []string {
	var found []string
	found = append(found, multisetDiff("literal", want.Literals, got.Literals)...)
	found = append(found, multisetDiff("api read", want.APIReads, got.APIReads)...)
	found = append(found, multisetDiff("accessor read", want.AccessorReads, got.AccessorReads)...)
	for _, tag := range sortedKeys(want.Elements, got.Elements) {
		if want.Elements[tag] != got.Elements[tag] {
			found = append(found, fmt.Sprintf("<%s> count %d -> %d", tag, want.Elements[tag], got.Elements[tag]))
		}
	}
	return found
}

func multisetDiff(kind string, want, got []string) []string {
	counts := map[string]int{}
	for _, value := range want {
		counts[value]++
	}
	for _, value := range got {
		counts[value]--
	}
	var out []string
	for _, value := range sortedKeys(counts) {
		switch delta := counts[value]; {
		case delta > 0:
			out = append(out, fmt.Sprintf("%s gone (x%d): %q", kind, delta, value))
		case delta < 0:
			out = append(out, fmt.Sprintf("%s added (x%d): %q", kind, -delta, value))
		}
	}
	return out
}

func sortedKeys[V any](maps ...map[string]V) []string {
	seen := map[string]bool{}
	for _, m := range maps {
		for key := range m {
			seen[key] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// hostFooterSource pulls the component out of the client's bundle. The binary is
// ~180 MB, so it is scanned in chunks and only the window around the marker is
// kept.
func hostFooterSource(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	const chunk = 4 << 20
	buffer := make([]byte, chunk)
	carry := ""
	for {
		read, err := file.Read(buffer)
		if read > 0 {
			haystack := carry + string(buffer[:read])
			if at := strings.Index(haystack, footerMarker); at >= 0 {
				window := haystack[at:]
				for len(window) < footerWindow {
					more := make([]byte, footerWindow-len(window))
					got, readErr := file.Read(more)
					if got > 0 {
						window += string(more[:got])
					}
					if readErr != nil || got == 0 {
						break
					}
				}
				if end := strings.Index(window, footerEnd); end > 0 {
					window = window[:end]
				}
				return window, nil
			}
			// Keep just enough tail that a marker split across two reads is still
			// found whole.
			if len(haystack) > len(footerMarker) {
				carry = haystack[len(haystack)-len(footerMarker):]
			} else {
				carry = haystack
			}
		}
		if err != nil {
			if err == io.EOF {
				return "", fmt.Errorf("marker %s not found in %s", footerMarker, path)
			}
			return "", err
		}
	}
}

// installedClient finds the client binary the way a user would reach it. CI
// runners have no opencode, and a check that cannot run has to say so out loud
// rather than pass: this skips, and the pre-commit hook runs it on a machine
// where the client exists.
func installedClient(t *testing.T) string {
	t.Helper()
	// An explicit path first, because the machine that most needs this gate is a
	// CI runner that installed the client for one job and has it nowhere a user
	// would look.
	if path := os.Getenv("ATENEA_OPENCODE_BIN"); path != "" {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("ATENEA_OPENCODE_BIN is set to %s but cannot be read: %v", path, err)
		}
		return path
	}
	if path, err := exec.LookPath("opencode"); err == nil {
		return path
	}
	home, err := os.UserHomeDir()
	if err == nil {
		path := filepath.Join(home, ".opencode", "bin", "opencode")
		if _, statErr := os.Stat(path); statErr == nil {
			return path
		}
	}
	t.Skip("opencode is not installed here, so the host's footer cannot be read; set ATENEA_OPENCODE_BIN or run this where the client is")
	return ""
}

func clientVersion(path string) string {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func TestHostFooterStillDrawsWhatWeReplaced(t *testing.T) {
	client := installedClient(t)

	source, err := hostFooterSource(client)
	if err != nil {
		t.Fatalf("reading the host footer: %v", err)
	}
	got := contractFrom(source)
	got.MeasuredOn = clientVersion(client)

	if *updateGolden {
		body, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(contractGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(contractGolden, append(body, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("pinned %s from %s", contractGolden, got.MeasuredOn)
		return
	}

	raw, err := os.ReadFile(contractGolden)
	if err != nil {
		t.Fatalf("reading the pinned contract: %v", err)
	}
	var want footerContract
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("parsing the pinned contract: %v", err)
	}

	changes := drift(want, got)
	if len(changes) == 0 {
		return
	}
	t.Errorf(`the client's own sidebar footer changed, and this binary draws that footer.

  pinned against %s, installed is %s

%s

  Atenea's widget wins that slot outright, so whatever the client added there is
  not on screen for anybody who installed the line. Decide which:
    - reproduce it in internal/statusline/opencode/atenea.tsx, then re-pin with
      go test ./internal/statusline -run HostFooter -update-host-footer
    - or hand the slot back: register sidebar_content at 900 instead, which is
      what the widget already does when the host would be onboarding.`,
		want.MeasuredOn, got.MeasuredOn, "    "+strings.Join(changes, "\n    "))
}

// The half of the gate that needs no client: what the widget must draw because it
// took the slot. Every literal listed here is checked against the pinned
// inventory too, so a host that stops drawing one cannot leave this list quietly
// describing a line nobody has.
func TestFooterWidgetDrawsEveryLineItTookOver(t *testing.T) {
	raw, err := os.ReadFile(contractGolden)
	if err != nil {
		t.Fatalf("reading the pinned contract: %v", err)
	}
	var host footerContract
	if err := json.Unmarshal(raw, &host); err != nil {
		t.Fatal(err)
	}

	widget, err := os.ReadFile(filepath.Join("opencode", "atenea.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(widget)

	// The client's version line, glyph by glyph: a bullet, the two halves of the
	// name it sets in different colors, and the version read off the host.
	for _, literal := range []string{"\\u2022", "Open", "Code"} {
		if !slices.Contains(host.Literals, literal) {
			t.Errorf("the pinned footer no longer contains %q: this list is describing a line the host does not draw", literal)
			continue
		}
		// The bundle escapes non-ASCII; the widget writes the character.
		want := literal
		if literal == "\\u2022" {
			want = "\u2022"
		}
		if !strings.Contains(source, want) {
			t.Errorf("the widget replaced the host's footer but does not draw %q", want)
		}
	}
	if !slices.Contains(host.APIReads, "app.version") {
		t.Error("the pinned footer no longer reads app.version")
	}
	if !strings.Contains(source, "api.app?.version") {
		t.Error("the widget must read the client's version from the host, not carry a copy")
	}

	// The one thing the widget does not reproduce is the host's onboarding card,
	// and the deal is that it declines the slot whenever that card applies. The
	// key its condition turns on is pinned, so a rename breaks this rather than
	// silently disabling the check.
	const kvKey = "dismissed_getting_started"
	if !slices.Contains(host.Literals, kvKey) {
		t.Errorf("the host footer no longer reads %q: the widget's reason for declining the slot is stale", kvKey)
	}
	if !strings.Contains(source, kvKey) || !strings.Contains(source, "hostIsOnboarding") {
		t.Errorf("the widget must decline the footer while the host would draw its %q card", kvKey)
	}
}

// Proof that the comparison fails when it should: the same inventory with one
// line added is a drift, and an untouched one is not.
func TestFooterDriftIsNoticed(t *testing.T) {
	const original = `"internal:sidebar-footer";function A(B){let X=()=>B.api.theme.current;` +
		`var q=m("text"),K=m("span");i(K,S1("\u2022")),U1(q,()=>B.api.app.version),b(K,"style",X().textMuted)`
	pinned := contractFrom(original)

	if changes := drift(pinned, contractFrom(original)); len(changes) != 0 {
		t.Errorf("an unchanged footer must not read as drift, got %v", changes)
	}

	// A release that adds one row: a new element, a new literal, a new read.
	added := original + `;var z=m("text");i(z,S1("Update available")),U1(z,()=>B.api.app.latest)`
	changes := drift(pinned, contractFrom(added))
	if len(changes) == 0 {
		t.Fatal("a footer with an added line must read as drift")
	}
	joined := strings.Join(changes, "\n")
	for _, want := range []string{`literal added (x1): "Update available"`, `api read added (x1): "app.latest"`, "<text> count 1 -> 2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the failure must name %s, got:\n%s", want, joined)
		}
	}

	// And a line taken away is reported as gone, not as nothing. The bundle writes
	// non-ASCII as an escape, so the literal carries a backslash and the failure
	// message escapes it again.
	removed := strings.Replace(original, `i(K,S1("\u2022")),`, "", 1)
	if changes := drift(pinned, contractFrom(removed)); !strings.Contains(strings.Join(changes, "\n"), `literal gone (x1): "\\u2022"`) {
		t.Errorf("a removed literal must be reported, got %v", changes)
	}
}
