package core_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// `path = "."` is not a mistake in the shipped settings: it is the mechanism by
// which a fresh install works against whatever tree you are standing in, and
// the CLI is the thing standing somewhere.
//
// A daemon stands nowhere. Its working directory is whatever its unit file left
// it -- $HOME before those units learned to name the state root, Atenea's own
// receipts and measurement base afterwards. Either way it is a tree nobody
// chose, searched under a name somebody trusts.
func TestAServiceRefusesARepositoryDeclaredByARelativePath(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	relative := false
	for _, repo := range cfg.Repositories {
		if repo.Path == "." {
			relative = true
		}
	}
	if !relative {
		t.Skip("the shipped settings no longer declare a repository at \".\"")
	}

	_, err = core.New(cfg, core.Service)
	if err == nil {
		t.Fatal("a service started with a repository it cannot resolve")
	}
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Errorf("kind = %v, want invalid_input", contract.KindOf(err))
	}
	for _, want := range []string{"current", `"."`, "relative"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal = %q, want it to mention %q", err, want)
		}
	}
	// The remedy is the half that makes a refusal actionable, and on the
	// built-in defaults there is no file to edit -- telling somebody to edit
	// one sends them looking for it.
	if !strings.Contains(err.Error(), "config init") {
		t.Errorf("refusal = %q, want it to name the one command that fixes this", err)
	}
}

// The same settings are fine for a command. This is the half that must not
// regress: the convenience is the reason the shipped value is what it is.
func TestACommandKeepsTheConvenienceAServiceRefuses(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	atenea, err := core.New(cfg, core.Command)
	if err != nil {
		t.Fatalf("a command was refused the working directory it is standing in: %v", err)
	}
	_ = atenea.Shutdown()
}

// With a file on disk the remedy is the file, not a command that would refuse
// to overwrite it.
func TestTheRemedyNamesTheFileWhenThereIsOne(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	cfg.Source = "/etc/atenea/atenea.toml"
	for i := range cfg.Repositories {
		cfg.Repositories[i].Path = "./somewhere"
	}

	if _, err := core.New(cfg, core.Service); err == nil {
		t.Fatal("a service started with a repository it cannot resolve")
	} else if !strings.Contains(err.Error(), "/etc/atenea/atenea.toml") {
		t.Errorf("refusal = %q, want it to name the file to edit", err)
	}
}
