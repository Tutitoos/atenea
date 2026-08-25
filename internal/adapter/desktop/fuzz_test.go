package desktop

import (
	"encoding/json"
	"testing"
)

// An accessibility tree comes from whatever application happens to be running,
// which is the repository's own definition of "a parser that reads somebody
// else's bytes" -- the category the CI fuzz gate exists for. A window can
// publish any string it likes as a title, including one that is not valid
// UTF-8, and an application can be replaced under this process between one
// call and the next.
//
// What is asserted is the weakest useful thing and the right one: it does not
// panic. A malformed answer is a provider failure the adapter already sorts
// into a bin; a panic takes the service down with every other chat attached.
func FuzzInspectAnswerNeverPanics(f *testing.F) {
	f.Add(`{"nodes":[{"role":"AXButton","depth":1}],"count":1}`)
	f.Add(`{"nodes":[],"count":0,"truncated":"time budget reached"}`)
	f.Add(`{"nodes":null,"count":-1}`)
	f.Add(`{"nodes":[{"role":"\ud800"}]}`)
	f.Add(`{"count":"not a number"}`)
	f.Add(``)
	f.Add(`[]`)

	f.Fuzz(func(t *testing.T, body string) {
		var answer struct {
			Nodes     []map[string]any `json:"nodes"`
			Count     int              `json:"count"`
			Truncated string           `json:"truncated"`
		}
		// Unmarshalling into the shape the adapter uses, which is the whole of
		// what it trusts from the far side.
		_ = json.Unmarshal([]byte(body), &answer)
		// And walking what came back the way the adapter does, because a nil
		// map inside a non-nil slice is exactly the shape that panics a
		// careless loop.
		for _, node := range answer.Nodes {
			_ = node["role"]
			_ = node["bundle_id"]
			if role, ok := node["role"].(string); ok {
				_ = len(role)
			}
		}
	})
}
