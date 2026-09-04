// Package reviewer is the shipped auditor: it re-reads what another agent
// answered about and says whether the answer holds up.
//
// It is the second worked example, and it is deliberately not a model. A
// review that cannot be checked is an opinion, so this one only makes
// claims it can prove on the spot. Two shapes of subject get two different
// checks:
//
//   - A subject whose result names a `path` (filereader's contract) is
//     re-read whole: the claimed bytes, lines and content are compared
//     against the file on disk. See check() below.
//   - A subject whose result is prose (`explore`'s and `reader`'s
//     contract: `summary`/`findings`) has no bytes or lines to compare, so
//     the check is narrower: every `path:line` or `Line N of path`
//     citation the prose contains is resolved against disk and, where a
//     quoted excerpt sits beside one, checked against what that line
//     actually holds. See citations.go for exactly what counts as a
//     citation and what "roughly matches" means -- both are deliberately
//     narrow, and every report this mode produces says so in its own
//     text, on an `ok` verdict as much as a refusal, because a
//     prose-shaped answer passing this check has had far less of it
//     looked at than a filereader answer passing the first one.
//
// Everything above either check -- the subject on the wire, the review's
// own trace row, the relaunch carrying the rejection back -- is exercised
// by a process that really re-reads a file and really disagrees when the
// answer is wrong.
//
// A model-backed reviewer differs from either of these only in what
// happens between reading the subject and writing the verdict.
package reviewer

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/Tutitoos/atenea/internal/agent/readscope"
)

// assignment is the half of the wire this agent reads.
type assignment struct {
	Task struct {
		Objective string   `json:"objective"`
		Files     []string `json:"files"`
		Criterion string   `json:"criterion"`
	} `json:"task"`
	Context map[string]json.RawMessage `json:"context"`
	Subject *subject                   `json:"subject"`
}

type subject struct {
	RunID   string         `json:"run_id"`
	Type    string         `json:"type"`
	Attempt int            `json:"attempt"`
	Result  map[string]any `json:"result"`
	Verdict string         `json:"verdict"`
	Reason  *reason        `json:"reason"`
	Task    struct {
		Objective string   `json:"objective"`
		Files     []string `json:"files"`
		Criterion string   `json:"criterion"`
	} `json:"task"`
}

type report struct {
	Result  map[string]any `json:"result"`
	Verdict string         `json:"verdict"`
	Reason  *reason        `json:"reason,omitempty"`
}

type reason struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Main runs the reviewer: read the subject on stdin, judge it, answer on
// stdout, exit zero whatever the verdict. Refusing an answer is this agent
// working, not this agent failing.
func Main(stdin io.Reader, stdout io.Writer) error {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading the assignment: %w", err)
	}
	var in assignment
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("the assignment is not readable: %w", err)
	}
	return json.NewEncoder(stdout).Encode(judge(in))
}

// judge is the whole audit.
//
// The verdicts mean what they mean everywhere else in Atenea. `ok` is the
// answer holds. `failed` is the reviewer's "bad": it looked, and the answer
// is wrong -- that is the verdict that earns a relaunch. `incomplete` is the
// reviewer's own shortfall: it could not check, and it must not launder that
// into either of the other two, because an unchecked answer passed off as
// approved is the exact failure a review exists to prevent.
func judge(in assignment) report {
	if in.Subject == nil {
		return incomplete("nothing to review: this assignment carries no subject")
	}
	s := in.Subject

	// A subject that already reported its own shortfall is not something to
	// re-litigate. The reviewer agrees with it and says which one it is.
	switch s.Verdict {
	case "failed":
		return refuse("the run reported failed itself: " + reasonText(s))
	case "incomplete":
		return incomplete("the run reported incomplete itself: " + reasonText(s))
	case "canceled":
		return incomplete("the run was canceled: " + reasonText(s))
	}

	if path, ok := s.Result["path"].(string); !ok || strings.TrimSpace(path) == "" {
		return judgeCitations(in, s)
	}
	checks, err := check(in, s)
	if err != nil {
		return incomplete(err.Error())
	}
	if len(checks.mismatches) > 0 {
		out := refuse(strings.Join(checks.mismatches, "; "))
		out.Result["checked"] = checks.checked
		return out
	}
	out := report{
		Result:  map[string]any{"checked": checks.checked, "subject": s.RunID},
		Verdict: "ok",
	}
	if checks.checked == 0 {
		// Nothing was verifiable. Saying ok here would be the reviewer
		// approving an answer it never looked at.
		return incomplete("the answer names nothing this reviewer can verify")
	}
	return out
}

type findings struct {
	checked    int
	mismatches []string
}

// check re-reads what the subject claims to have read.
//
// It is deliberately narrow. It knows one shape of answer -- path, bytes,
// lines, content -- and it says so when it is handed anything else, rather
// than inventing a judgement about a result it does not understand.
func check(in assignment, s *subject) (findings, error) {
	var out findings
	path, ok := s.Result["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return out, fmt.Errorf("the result names no path, so there is nothing to re-read")
	}
	name := path
	if root := repositoryRoot(in); root != "" && !filepath.IsAbs(name) {
		name = filepath.Join(root, name)
	}
	body, err := readscope.ReadFile(repositoryRoot(in), name, in.Task.Files)
	if err != nil {
		// The file the answer is about cannot be opened by the reviewer.
		// That is the reviewer's shortfall, not proof the answer is wrong:
		// a permission the child had and this process does not looks
		// exactly like this.
		return out, fmt.Errorf("cannot re-read %s: %v", path, err)
	}

	if claimed, present := number(s.Result, "bytes"); present {
		out.checked++
		if claimed != len(body) {
			out.mismatches = append(out.mismatches,
				fmt.Sprintf("bytes: answered %d, the file has %d", claimed, len(body)))
		}
	}
	if claimed, present := number(s.Result, "lines"); present {
		out.checked++
		if actual := countLines(body); claimed != actual {
			out.mismatches = append(out.mismatches,
				fmt.Sprintf("lines: answered %d, the file has %d", claimed, actual))
		}
	}
	if content, present := s.Result["content"].(string); present && content != "" {
		out.checked++
		if content != string(body) {
			out.mismatches = append(out.mismatches,
				"content: what was answered is not what the file holds")
		}
	}
	return out, nil
}

// number reads an integer the way JSON delivers it. A result crossing a pipe
// arrives as float64, and a reviewer that only understood int would refuse
// every correct answer.
func number(result map[string]any, key string) (int, bool) {
	switch v := result[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}

func refuse(text string) report {
	return report{
		Result:  map[string]any{"checked": 0},
		Verdict: "failed",
		Reason:  &reason{Kind: "invalid_input", Text: text},
	}
}

func incomplete(text string) report {
	return report{
		Result:  map[string]any{"checked": 0},
		Verdict: "incomplete",
		Reason:  &reason{Kind: "unavailable", Text: text},
	}
}

func reasonText(s *subject) string {
	if s.Reason == nil || strings.TrimSpace(s.Reason.Text) == "" {
		return "no reason given"
	}
	return s.Reason.Text
}

func repositoryRoot(in assignment) string {
	raw, ok := in.Context["repository"]
	if !ok {
		return ""
	}
	var repo struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal(raw, &repo); err != nil {
		return ""
	}
	return repo.Root
}

func countLines(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	n := strings.Count(string(body), "\n")
	if !strings.HasSuffix(string(body), "\n") {
		n++
	}
	return n
}
