package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const claudeSkillContents = `---
name: atenea
description: Run a read-only Atenea status, metrics, traces, catalog, doctor, detect, incidents, floor, config or intent command.
disable-model-invocation: true
---

Interpret the arguments after /atenea as one Atenea read-only command.
Call only the Atenea MCP tool atenea.command, passing the command and its typed options.
Present the Markdown returned by Atenea unchanged.
Never use Bash, native Computer Use, or another tool to implement this command.

Arguments: $ARGUMENTS
`

func claudeSkillPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "skills", "atenea", "SKILL.md"), nil
}

func installClaudeSkill(replace bool) (bool, error) {
	path, err := claudeSkillPath()
	if err != nil {
		return false, err
	}
	contents := []byte(claudeSkillContents)
	current, readErr := os.ReadFile(path)
	switch {
	case readErr == nil && bytes.Equal(current, contents):
		return false, nil
	case readErr == nil && !replace:
		return false, errors.New("la skill ~/.claude/skills/atenea ya existe y no pertenece a Atenea; usa --replace")
	case readErr != nil && !os.IsNotExist(readErr):
		return false, readErr
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	if readErr == nil {
		if err := os.WriteFile(backupPath(path), current, 0o600); err != nil {
			return false, err
		}
	}
	if err := atomicDesktopWrite(path, contents, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func removeClaudeSkill() error {
	path, err := claudeSkillPath()
	if err != nil {
		return err
	}
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(contents, []byte(claudeSkillContents)) {
		return fmt.Errorf("la skill %s no pertenece a Atenea", path)
	}
	return os.Remove(path)
}
