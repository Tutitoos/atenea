package kivgraph

import (
	"strings"
	"testing"
)

// The JSONL a provider prints is not this repository's data.
//
// parseIndexReport reads whatever `kivgraph index --full --json` puts on
// stdout, line by line, and the one thing already known about that stream is
// that it changed shape once: it used to be truncated to 8 KiB before it got
// here, and every line after the cut was half an object. A parser that can be
// handed a half object can be handed anything, and the answer has to be an
// error rather than a panic in the one capability that mutates a repository.
func FuzzParseIndexReportNeverPanics(f *testing.F) {
	f.Add(`{"event":"result","result":{"generation_id":"000011","passed":true,` +
		`"counts":{"symbols":1,"edges":2}}}`)
	f.Add(`{"event":"progress","file":"a.go"}` + "\n" +
		`{"event":"result","result":{"passed":false,"error":"index refused"}}`)
	f.Add(`{"event":"result","result":{"generation_id":"x","passed":true,"counts":{`)
	f.Add("")
	f.Add("\n\n\n")
	f.Add(`{"event":"result","result":null}`)
	f.Add(strings.Repeat(`{"event":"progress"}`+"\n", 64))

	f.Fuzz(func(t *testing.T, stream string) {
		// What is asserted is narrow on purpose, because most of what a
		// fuzzer produces here SHOULD be accepted: a result event with
		// `passed` and nothing else is a provider reporting an empty index,
		// and the adapter verifies readiness separately through graph_status
		// rather than from these counters. So the contract is: it answers,
		// and the numbers it answers with are numbers a caller can use.
		//
		// (The first run of this target found that encoding/json matches field
		// names without regard to case, so `{"pAssed":true}` is read as
		// `passed`. Left alone: the sender is kivgraph, and refusing it would
		// reject nothing that exists.)
		report, err := parseIndexReport(stream)
		if err != nil {
			return
		}
		if report.Nodes < 0 || report.Edges < 0 {
			t.Fatalf("accepted %d nodes and %d edges from %q: a count below zero "+
				"reaches repository.index's declared output", report.Nodes, report.Edges, stream)
		}
	})
}
