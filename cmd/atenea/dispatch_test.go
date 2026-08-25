package main

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/statusline"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// wrap hands its arguments to the client verbatim; that is the whole promise.
// The global help interceptor used to scan all of them, so `atenea wrap claude
// --help` printed Atenea's page and left no way to ask a wrapped client for
// its own -- and the same flag inside `-p "--help means..."` never reached it
// either. The refusal below proves the dispatch went past the interceptor and
// into cmdWrap: the client name is what it complains about, and an
// unsupported one is used precisely so nothing is ever executed.
func TestHelpAfterTheClientNameBelongsToTheClient(t *testing.T) {
	out, err := cli(t, "wrap", "emacs", "--help")
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v (out %q), want invalid_input from cmdWrap", contract.KindOf(err), out)
	}
	if strings.Contains(out, "Usage: atenea wrap") {
		t.Errorf("the client's --help was answered with Atenea's own page:\n%s", out)
	}
}

// Help about the wrapper itself is still reachable, because there the flag is
// ahead of the client name and the argument list is still Atenea's.
func TestHelpBeforeTheClientNameBelongsToAtenea(t *testing.T) {
	out, err := cli(t, "wrap", "--help")
	if err != nil {
		t.Fatalf("wrap --help: %v", err)
	}
	if !strings.HasPrefix(out, "Usage: atenea wrap") {
		t.Errorf("out = %q, want wrap's own usage", out)
	}
}

// Four commands read no arguments at all. Dropping the leftovers in silence
// is how `atenea status --config other.toml` reports on the default settings
// file: --config is global, parsed only before the command name, so written
// after it that word is simply gone and the answer is about another machine.
func TestTheCommandsThatTakeNoArgumentsRefuseThem(t *testing.T) {
	// `run` is checked through the helper rather than through cli(): it is the
	// service, and invoking it here would block this test until somebody
	// interrupted the suite -- which is also why an argument it silently drops
	// is the worst of the four to get wrong.
	if err := noArguments("run", []string{"--config", "/other.toml"}); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Errorf("run --config PATH was filed as %v", contract.KindOf(err))
	}
	for _, command := range []string{"version", "status", "catalog"} {
		t.Run(command, func(t *testing.T) {
			if _, err := cli(t, command, "ghost"); contract.KindOf(err) != contract.FailureInvalidInput {
				t.Errorf("%s ghost was filed as %v", command, contract.KindOf(err))
			}
			_, err := cli(t, command, "--config", settingsFile(t))
			if contract.KindOf(err) != contract.FailureInvalidInput {
				t.Fatalf("%s --config PATH was filed as %v", command, contract.KindOf(err))
			}
			// A misplaced global flag is not a typo, and saying "unknown
			// argument" would send the reader looking for a flag that exists.
			if !strings.Contains(err.Error(), "before the command") {
				t.Errorf("err = %v, want it to say where --config belongs", err)
			}
		})
	}
}

// The three verbs take a widget name and a fourth subcommand lists them. None
// of that was in the help page or in the refusal, so the only way to learn
// that `atenea statusline install limits` is a thing was to read the source
// -- while `status` printed "atenea statusline install <name>" as the remedy
// for a widget that was not installed.
func TestTheStatuslineHelpNamesEveryWidgetAndTheListingVerb(t *testing.T) {
	help, ok := commandHelp["statusline"]
	if !ok {
		t.Fatal("statusline has no help entry")
	}
	if !strings.Contains(help, "widgets") {
		t.Error("the statusline help never mentions the widgets subcommand")
	}
	if !strings.Contains(help, "WIDGET") {
		t.Error("the statusline help never mentions that its verbs take a widget name")
	}
	names := statusline.Names()
	if len(names) == 0 {
		t.Fatal("this binary carries no widget; this test would pass for an empty help page")
	}
	for _, name := range names {
		if !strings.Contains(help, name) {
			t.Errorf("this binary carries the %q widget and its help page never names it", name)
		}
	}
	// The refusal is the other place a reader meets the list of verbs.
	_, err := cli(t, "statusline")
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
	if !strings.Contains(err.Error(), "widgets") {
		t.Errorf("err = %v, want the widgets subcommand named among the choices", err)
	}
}
