package main

import "testing"

func TestParseSlashCommand(t *testing.T) {
	got, err := parseSlashCommand(`/atenea metrics --capability "code.search"`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"metrics", "--capability", "code.search"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("word %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseSlashCommandRejectsShellLikeUnclosedInput(t *testing.T) {
	if _, err := parseSlashCommand(`/atenea status "`); err == nil {
		t.Fatal("unclosed quote was accepted")
	}
}
