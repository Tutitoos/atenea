package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// internalCommands are dispatched but deliberately absent from the operator's
// listing: agent-exec is how a spawned agent re-enters this binary, and
// help/-h/--help are the listing itself.
var internalCommands = map[string]bool{
	"agent-exec": true,
	"help":       true,
	"-h":         true,
	"--help":     true,
}

// dispatched reads the command names out of run()'s own switch.
//
// From the source rather than from a table, because a table is a third place
// that can disagree with the other two. The point of this test is that the
// listing cannot drift from what the binary actually answers, and asking the
// binary means asking the code that answers.
func dispatched(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	run := string(body)
	start := strings.Index(run, "\tswitch command {")
	if start < 0 {
		t.Fatal("main.go has no command switch: this test is reading the wrong thing")
	}
	end := strings.Index(run[start:], "\n\tdefault:")
	if end < 0 {
		t.Fatal("the command switch has no default: this test is reading the wrong thing")
	}
	var out []string
	for _, found := range regexp.MustCompile(`case ("[a-z-]+"(, "[a-z-]+")*):`).
		FindAllStringSubmatch(run[start:start+end], -1) {
		for _, name := range strings.Split(found[1], ", ") {
			name = strings.Trim(name, `"`)
			if !internalCommands[name] {
				out = append(out, name)
			}
		}
	}
	if len(out) < 10 {
		t.Fatalf("found only %d commands in the switch: this test is reading the wrong thing", len(out))
	}
	return out
}

// commandBlock is the Commands: section of usage, and nothing after it. The
// global flags below are two-dash names, and reading them as commands would
// make this test complain that `atenea --config` is not dispatched.
func commandBlock(t *testing.T) string {
	t.Helper()
	commands := strings.SplitN(usage, "Commands:", 2)
	if len(commands) != 2 {
		t.Fatal("usage has no Commands: block")
	}
	return strings.SplitN(commands[1], "Global flags:", 2)[0]
}

// A command the binary answers and the listing does not mention is a command
// nobody finds.
//
// `workflow` was exactly that for its whole life: run() dispatched it, it had
// a full commandHelp entry, settings.md gave it a section -- and `atenea` with
// no arguments never named it, so the only way to discover the entire graph
// subsystem was to already know it existed. The existing help test iterates
// commandHelp, so it could not notice: the gap was between the switch and the
// prose.
func TestEveryDispatchedCommandIsInTheListing(t *testing.T) {
	listing := commandBlock(t)
	for _, command := range dispatched(t) {
		if !regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(command) + `\b`).MatchString(listing) {
			t.Errorf("`atenea %s` is dispatched and the listing never names it", command)
		}
	}
}

// And the reverse: a listing that advertises a command the binary does not
// answer sends the reader to an error.
func TestTheListingAdvertisesNothingTheBinaryRefuses(t *testing.T) {
	answered := map[string]bool{}
	for _, command := range dispatched(t) {
		answered[command] = true
	}
	for _, line := range strings.Split(commandBlock(t), "\n") {
		found := regexp.MustCompile(`^  ([a-z-]+)\s`).FindStringSubmatch(line)
		if found == nil {
			continue
		}
		if !answered[found[1]] {
			t.Errorf("the listing offers `atenea %s`, which the binary does not dispatch", found[1])
		}
	}
}

// Every listed command also has its own help. The three lists -- the switch,
// the listing and commandHelp -- are the same set or one of them is lying.
func TestEveryDispatchedCommandHasItsOwnHelp(t *testing.T) {
	for _, command := range dispatched(t) {
		if _, ok := commandHelp[command]; !ok {
			t.Errorf("`atenea %s --help` has nothing to print", command)
		}
	}
}
