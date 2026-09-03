package clientconfig

// Translation turns "the project asks for MCP server" into "these capabilities,
// behind the funnel, answered by these implementations" -- or into a refusal
// that names what is missing.
//
// The refusal is the half that matters. A translator that quietly drops what
// it cannot map produces a report where absence and satisfaction look the
// same, and the reader learns nothing except that the tool ran. This project
// has paid for that shape before, more than once, so an unmatched request is
// carried through to the end and printed with the same weight as a match.

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Answer is how Atenea satisfies one request, or does not.
type Answer string

const (
	// AnswerFunnel means Atenea has registered implementations for it: the
	// request maps to capabilities the selector will answer.
	AnswerFunnel Answer = "funnel"
	// AnswerVouched means Atenea declares this backend itself and hands it to
	// clients through `atenea wrap`, having checked it first. It is not a
	// capability -- nothing dispatches against it -- but the project does get
	// what it asked for.
	AnswerVouched Answer = "vouched"
	// AnswerNone means nothing here provides it. Named, never dropped.
	AnswerNone Answer = "unmatched"
	// AnswerNotACapability is a skill: prose a client loads. Atenea has no
	// equivalent and inventing one would be a mapping nobody asked for.
	AnswerNotACapability Answer = "prose"
)

// Match is one thing the project asks for and what became of it.
//
// One thing, not one declaration. A project that uses two clients declares the
// same backend twice -- once in `.mcp.json`, once in `opencode.json` -- and it
// is still one backend it is asking for. Counting the declarations would make
// a two-client repository look like it wants twice as much as it does, and the
// unmatched count is the number this report is read for.
type Match struct {
	Request Request
	Answer  Answer
	// Sources are every file that declares it, sorted. The collapse keeps the
	// evidence: two files asking for one backend is worth seeing.
	Sources []string
	// Provider is the registered provider that answers it, when the answer is
	// AnswerFunnel.
	Provider string
	// Capabilities are the capability ids reachable through that provider,
	// sorted.
	Capabilities []string
	// Implementations are the implementation ids behind them, sorted.
	Implementations []string
	// Note says why, for anything that is not a plain match.
	Note string
	// Disagreement is set when two declarations of the same name do not say
	// the same thing. Reported rather than resolved: the collapse is a
	// convenience, and a convenience that hides an inconsistency in somebody's
	// own configuration has stopped being one.
	Disagreement string
}

// Report is the whole translation for one repository.
type Report struct {
	Reading Reading
	Matches []Match
}

// Unmatched returns the requests nothing here provides.
func (r Report) Unmatched() []Match {
	out := make([]Match, 0, len(r.Matches))
	for _, match := range r.Matches {
		if match.Answer == AnswerNone {
			out = append(out, match)
		}
	}
	return out
}

// Catalog is what Atenea has to answer with. Passed in rather than reached
// for: this package holds no settings and opens nothing, which is what lets
// the command that prints a report avoid building a Core -- and a Core would
// open the measurement base and may start a managed backend, both of which
// are the opposite of read-only.
type Catalog struct {
	// Implementations are the registered implementations, each naming its
	// provider and the capability it answers.
	Implementations []contract.Implementation
	// Vouched are the ids of the MCP backends Atenea declares itself and
	// hands to clients through `atenea wrap`.
	Vouched []string
}

// Translate maps everything the project asks for onto the catalog.
//
// Declarations of the same thing collapse into one match; the reading keeps
// the per-file record untouched, because what each file says is a fact and
// what the project wants is a reading of those facts.
func Translate(reading Reading, catalog Catalog) Report {
	report := Report{Reading: reading, Matches: make([]Match, 0, len(reading.Requests))}

	byProvider := make(map[string][]contract.Implementation, len(catalog.Implementations))
	for _, impl := range catalog.Implementations {
		key := normalize(impl.Provider)
		byProvider[key] = append(byProvider[key], impl)
	}
	vouched := make(map[string]string, len(catalog.Vouched))
	for _, id := range catalog.Vouched {
		vouched[normalize(id)] = id
	}

	// Same rule as Reading.Asks, so the count a caller prints and the rows it
	// prints under it can never disagree.
	at := make(map[string]int, len(reading.Requests))
	for _, request := range reading.Requests {
		key := identity(request)
		index, seen := at[key]
		if !seen {
			match := translateOne(request, byProvider, vouched)
			match.Sources = []string{request.Source}
			at[key] = len(report.Matches)
			report.Matches = append(report.Matches, match)
			continue
		}
		merge(&report.Matches[index], request)
	}
	sort.Slice(report.Matches, func(i, j int) bool {
		a, b := report.Matches[i], report.Matches[j]
		if a.Request.Kind != b.Request.Kind {
			return a.Request.Kind < b.Request.Kind
		}
		return a.Request.Name < b.Request.Name
	})
	return report
}

// merge folds a second declaration of something already seen into its match.
//
// Enabled is a union on purpose: a backend switched off for one client and on
// for another is on for this project, and reporting it as off would describe
// a machine nobody is running.
func merge(match *Match, request Request) {
	if !slices.Contains(match.Sources, request.Source) {
		match.Sources = append(match.Sources, request.Source)
		sort.Strings(match.Sources)
	}
	if request.Enabled && !match.Request.Enabled {
		match.Request.Enabled = true
		// Only the funnel's note is about being switched off, and only that
		// one stops being true here. Every other answer's note explains what
		// the match is -- "nothing registered here provides it", or that
		// `atenea wrap` vouches for it -- and clearing those left the row with
		// no explanation at all, for no reason other than that a second file
		// happened to declare the same backend enabled.
		if match.Answer == AnswerFunnel {
			match.Note = ""
		}
	}
	if request.Transport != match.Request.Transport && match.Disagreement == "" {
		match.Disagreement = fmt.Sprintf("declared %s in %s and %s in %s",
			match.Request.Transport, match.Request.Source, request.Transport, request.Source)
	}
}

func translateOne(request Request, byProvider map[string][]contract.Implementation, vouched map[string]string) Match {
	match := Match{Request: request, Answer: AnswerNone}

	if request.Kind == KindSkill {
		match.Answer = AnswerNotACapability
		match.Note = "prose for a client to load; Atenea answers capabilities, not instructions"
		return match
	}

	key := normalize(request.Name)

	if impls := byProvider[key]; len(impls) > 0 {
		match.Answer = AnswerFunnel
		match.Provider = impls[0].Provider
		for _, impl := range impls {
			match.Capabilities = append(match.Capabilities, impl.Capability)
			match.Implementations = append(match.Implementations, impl.ID)
		}
		sort.Strings(match.Capabilities)
		match.Capabilities = slices.Compact(match.Capabilities)
		sort.Strings(match.Implementations)
		if !request.Enabled {
			match.Note = "the project's own settings switch this off; Atenea answers it anyway when asked"
		}
		return match
	}

	if id, ok := vouched[key]; ok {
		match.Answer = AnswerVouched
		match.Provider = id
		match.Note = "declared by Atenea and handed to clients by `atenea wrap`, checked first; not a funnel capability"
		return match
	}

	match.Note = "nothing registered here provides it"
	return match
}

// normalize makes two names for the same backend compare equal.
//
// Mechanical only: case, surrounding space, and the `-mcp` / `mcp-` decoration
// that the same tool is packaged with in one client and without in another --
// MCP packaging decorations are one backend. Anything
// beyond that would be a policy about which tool means which capability, and
// a policy belongs in the settings file where it can be read, not in a table
// compiled into the binary where it cannot.
func normalize(name string) string {
	out := strings.ToLower(strings.TrimSpace(name))
	out = strings.TrimSuffix(out, "-mcp")
	out = strings.TrimSuffix(out, "_mcp")
	out = strings.TrimPrefix(out, "mcp-")
	out = strings.TrimPrefix(out, "mcp_")
	return out
}
