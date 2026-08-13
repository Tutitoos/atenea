// Package filereader is the minimal agent: it reads one file and answers.
//
// It exists to be a real far side. Everything above it -- the declared type,
// the assignment, the spawn, the report, the schema check, the trace -- is
// exercised end to end by a process that actually starts, actually reads a
// file and actually dies, with no model, no network and no key. A stub inside
// the test binary would exercise none of the spawn.
//
// It is also the worked example. An agent is a program that reads one JSON
// object on stdin and writes one on stdout; this is the shortest honest
// implementation of that, and a model-backed agent differs from it only in
// what happens between the two.
package filereader

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// maxFile is the ceiling on what this agent will read into a result. A file
// above it is answered with a reason rather than a truncated body dressed up
// as an answer.
const maxFile = 256 << 10

// assignment is the half of the wire this agent reads. It deliberately does
// not model every field: an agent parses what it needs and ignores the rest,
// which is what makes an added field additive on the wire.
type assignment struct {
	Task struct {
		Objective string   `json:"objective"`
		Files     []string `json:"files"`
		Criterion string   `json:"criterion"`
	} `json:"task"`
	Context map[string]json.RawMessage `json:"context"`
}

type report struct {
	Result     map[string]any `json:"result"`
	Verdict    string         `json:"verdict"`
	Reason     *reason        `json:"reason,omitempty"`
	Discovered []discovery    `json:"discovered,omitempty"`
}

type reason struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type discovery struct {
	Level string `json:"level"`
	Note  string `json:"note"`
}

// Main runs the agent: read stdin, answer on stdout, exit.
//
// It exits zero on every path it controls, including the ones that answer
// `failed`. The exit status is not the channel -- the report is -- and an
// agent that signaled failure twice, once in the verdict and once in the
// status, would leave Atenea deciding which to believe.
func Main(stdin io.Reader, stdout io.Writer) error {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading the assignment: %w", err)
	}
	var in assignment
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("the assignment is not readable: %w", err)
	}
	return json.NewEncoder(stdout).Encode(answer(in))
}

func answer(in assignment) report {
	if len(in.Task.Files) == 0 {
		return report{
			Verdict: "failed",
			Reason: &reason{
				Kind: "invalid_input",
				Text: "no file was named in the task",
			},
		}
	}
	name := in.Task.Files[0]
	if root := repositoryRoot(in); root != "" && !filepath.IsAbs(name) {
		name = filepath.Join(root, name)
	}

	info, err := os.Stat(name)
	switch {
	case os.IsNotExist(err):
		return report{
			Verdict: "failed",
			Reason:  &reason{Kind: "not_found", Text: "no such file: " + in.Task.Files[0]},
		}
	case err != nil:
		return report{
			Verdict: "failed",
			Reason:  &reason{Kind: "permission_denied", Text: err.Error()},
		}
	case info.IsDir():
		return report{
			Verdict: "failed",
			Reason:  &reason{Kind: "invalid_input", Text: in.Task.Files[0] + " is a directory"},
		}
	}

	body, err := os.ReadFile(name)
	if err != nil {
		return report{
			Verdict: "failed",
			Reason:  &reason{Kind: "permission_denied", Text: err.Error()},
		}
	}

	out := report{
		Result: map[string]any{
			"path":  in.Task.Files[0],
			"lines": countLines(body),
			"bytes": len(body),
		},
		Verdict: "ok",
		Discovered: []discovery{{
			Level: "repository",
			Note:  fmt.Sprintf("%s is %d bytes over %d lines", in.Task.Files[0], len(body), countLines(body)),
		}},
	}
	// Over the ceiling the answer is still true -- the count is a count --
	// but the body is not carried, and saying so is the difference between a
	// short answer and a wrong one.
	if len(body) > maxFile {
		out.Result["content"] = ""
		out.Verdict = "incomplete"
		out.Reason = &reason{
			Kind: "invalid_input",
			Text: fmt.Sprintf("counted the file but did not carry its body: %d bytes is over the %d ceiling",
				len(body), maxFile),
		}
		return out
	}
	if !utf8.Valid(body) {
		out.Result["content"] = ""
		out.Verdict = "incomplete"
		out.Reason = &reason{
			Kind: "invalid_input",
			Text: "counted the file but did not carry its body: it is not text",
		}
		return out
	}
	out.Result["content"] = string(body)
	return out
}

// repositoryRoot reads the one context level this agent uses. A level it was
// not served is simply absent, and absent means relative paths stay relative
// to wherever Atenea put the process.
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
