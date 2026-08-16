package main

import (
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// A declared agent type nothing dispatches is a promise with no far side, and
// the settings file cannot notice: `command` and `args` are strings there,
// and the switch that reads them is in this package. The gap only shows up at
// spawn -- after a plan naming the type has been written, compiled, funded
// and accepted -- as `no built-in agent`, which reads like a typo in the plan
// rather than a type this binary forgot to wire.
//
// So the table is the shipped declarations themselves rather than a list
// repeated here: a type added to default.toml is checked the day it is added,
// which a hand-written list is not.
func TestEveryShippedAgentTypeThisBinaryDeclaresIsDispatched(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}

	var checked int
	for _, declared := range cfg.Agents {
		name := declared.Spec.Name
		// Only the ones that name this binary. A settings file may point
		// `command` at any program, and what that program dispatches is not
		// this switch's business.
		if declared.Command != "$atenea" || len(declared.Args) == 0 || declared.Args[0] != "agent-exec" {
			continue
		}
		if len(declared.Args) != 2 {
			t.Errorf("agent %q is declared in %s with args %v: `agent-exec` takes exactly one built-in name",
				name, declaredIn(cfg), declared.Args)
			continue
		}
		checked++

		// A card no agent can read. The refusal under test happens before
		// anything is parsed, so every type fails here on its own terms --
		// and none of them reaches a settings file, a socket or a model.
		kind := declared.Args[1]
		err := cmdAgentRun(kind, strings.NewReader("this is not an assignment"), io.Discard)
		if undispatched(err) {
			t.Errorf("agent %q is declared in %s as `agent-exec %s`, and cmdAgentRun in "+
				"cmd/atenea/agent.go has no case for it: %v",
				name, declaredIn(cfg), kind, err)
		}
	}

	if checked == 0 {
		t.Fatal("no shipped agent type runs this binary; this test would pass for an empty file")
	}
	// Teeth: the check above is only worth making while an unwired name is
	// actually refused this way. If the refusal ever stopped being a
	// not_found, every loop above would pass by saying nothing.
	if err := cmdAgentRun("no-such-built-in", strings.NewReader("{}"), io.Discard); !undispatched(err) {
		t.Errorf("a name this binary does not ship answered %v, so the loop above proves nothing", err)
	}
}

// undispatched reports whether this is the refusal cmdAgentRun's default arm
// hands back, and not some other not_found an agent raised about its own
// work -- a file it could not open, a run it could not find.
func undispatched(err error) bool {
	return contract.KindOf(err) == contract.FailureNotFound &&
		strings.Contains(err.Error(), "no built-in agent")
}

// declaredIn names where the type under test came from, so the failure sends
// a reader to the block to edit rather than to the word "settings". The
// embedded defaults are a real file in this repository, and saying only
// "built-in defaults" would leave the reader looking for it.
func declaredIn(cfg config.Config) string {
	if cfg.Source == config.BuiltIn {
		return "internal/config/default.toml"
	}
	return cfg.Source
}

// truncate's budget n is a byte count, and a naive s[:n-1] can land inside a
// multi-byte rune's encoding -- there is no rule that the cut point falls
// between two runes. Shape that actually trips it: an 8-byte ASCII prefix
// followed by a 3-byte rune, truncated at n=10 so the cut lands after the
// rune's first byte and before its second and third. Under the byte-slice-
// only version of truncate this returned "12345678\xe4…", which
// utf8.ValidString rejects; that is the failure this test defends against.
func TestTruncateNeverCutsAMultiByteRuneInHalf(t *testing.T) {
	s := "12345678" + "世界" + " more text past the cut, so len(s) > n either way"
	got := truncate(s, 10)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate(%q, 10) = %q, which is not valid UTF-8", s, got)
	}
	if want := "12345678…"; got != want {
		t.Errorf("truncate(%q, 10) = %q, want %q", s, got, want)
	}
}
