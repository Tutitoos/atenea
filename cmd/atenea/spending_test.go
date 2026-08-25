package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// spendsOutsideACommission names every command that can cause a provider to
// charge with no commission's grant behind it, and where its flags are built.
//
// Money everywhere else in Atenea is a permission: granted per commission,
// split between the steps, refused when it runs out. A step spending its share
// is the design working, and holding `task` or `ask` to the rule below would
// be holding the whole product to it. These are the commands with no grant
// above them, where the only thing bounding the spend is that a person named
// what was about to happen.
//
// A list somebody has to add to is a weak guarantee, so the test below closes
// it from the other end: anything whose help text says it spends real money
// must be in here.
var spendsOutsideACommission = map[string]struct{ file, function string }{
	"floor measure": {"floor.go", "floorMeasure"},
}

// A command that spends outside a commission may not carry a flag default that
// is not the conservative one.
//
// This is the rule the project had as a habit and not as a check. `--agent`
// carried "plan" until 2026-08-16, when a run typed to READ the command's own
// warning text spent $0.3487 on a cold turn: nothing was broken, every guard
// fired as written, and the money went because a flag nobody had set had
// picked what to spend it on. The two flags that gate spending today gate it
// as a side effect of being required values, and the next flag shaped like the
// old default is stopped only by whoever writes it remembering to look.
//
// Read from the source rather than from a built FlagSet, because that is where
// the default is written and where somebody adding one will be standing.
func TestNoFlagOnASpendingCommandDefaultsToSpending(t *testing.T) {
	// flag.String(name, value, usage) and friends. The default is the middle
	// argument, and the conservative one is the type's zero.
	declaration := regexp.MustCompile(
		`flags\.(String|Bool|Int|Int64|Float64|Duration)(?:Var)?\(` +
			`(?:&[A-Za-z0-9_.]+, )?"([a-z0-9-]+)", ([^,]+),`)

	for command, where := range spendsOutsideACommission {
		body := sourceOf(t, where.file, where.function)
		found := declaration.FindAllStringSubmatch(body, -1)
		if len(found) == 0 {
			t.Errorf("%s: no flags found in %s; this test is reading the wrong thing",
				command, where.function)
			continue
		}
		for _, flag := range found {
			kind, name, value := flag[1], flag[2], strings.TrimSpace(flag[3])
			if value == conservative[kind] {
				continue
			}
			t.Errorf("`atenea %s --%s` defaults to %s. %s spends real money with no "+
				"commission's grant behind it, so a default here is money spent on a "+
				"value nobody named -- which is exactly how --agent's \"plan\" cost "+
				"$0.3487 on 2026-08-16. Make it required and refuse before the spend.",
				command, name, value, command)
		}
	}
}

// conservative is the default that spends nothing, per flag type.
var conservative = map[string]string{
	"String": `""`, "Bool": "false", "Int": "0", "Int64": "0",
	"Float64": "0", "Duration": "0",
}

// And the list cannot go stale quietly: a command whose own help says it
// spends real money is a command this rule is about.
func TestEveryCommandThatSaysItSpendsIsHeldToTheRule(t *testing.T) {
	for command, help := range commandHelp {
		if !strings.Contains(help, "spends real money") {
			continue
		}
		covered := false
		for named := range spendsOutsideACommission {
			if strings.HasPrefix(named, command) {
				covered = true
			}
		}
		if !covered {
			t.Errorf("`atenea %s` tells the reader it spends real money and is not in "+
				"spendsOutsideACommission, so nothing checks its flag defaults", command)
		}
	}
}

// sourceOf returns one function's body, read from the file it lives in.
func sourceOf(t *testing.T, file, function string) string {
	t.Helper()
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	source := string(body)
	start := strings.Index(source, "func "+function+"(")
	if start < 0 {
		t.Fatalf("%s has no %s: this test is reading the wrong thing", file, function)
	}
	end := strings.Index(source[start+1:], "\nfunc ")
	if end < 0 {
		return source[start:]
	}
	return source[start : start+1+end]
}
