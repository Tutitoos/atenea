// Package toolpath resolves one of the supported command-line surfaces of a
// client without making the configuration depend on one installation path.
package toolpath

import (
	"fmt"
	"os/exec"
	"strings"
)

// Candidate is one executable surface of a provider.
type Candidate struct {
	Source string
	Binary string
}

// Resolved is the executable selected for one invocation.
type Resolved struct {
	Source string
	Path   string
}

// ValidateSource checks the selector vocabulary without requiring the
// executable to be installed. Missing tools are runtime availability; an
// unknown source is a configuration error.
func ValidateSource(source string, candidates []Candidate) error {
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" || source == "auto" {
		return nil
	}
	for _, candidate := range candidates {
		if candidate.Source == source {
			return nil
		}
	}
	return fmt.Errorf("unknown executable source %q", source)
}

// Resolve selects a candidate. source may be "auto" or the Source of one
// candidate. In auto mode candidates are tried in declaration order, which
// makes the preference visible in configuration rather than hidden in code.
func Resolve(source string, candidates []Candidate) (Resolved, error) {
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		source = "auto"
	}
	if source != "auto" {
		found := false
		for _, candidate := range candidates {
			if candidate.Source == source {
				found = true
				if path, err := executable(candidate.Binary); err == nil {
					return Resolved{Source: candidate.Source, Path: path}, nil
				}
			}
		}
		if !found {
			return Resolved{}, fmt.Errorf("unknown executable source %q", source)
		}
		return Resolved{}, fmt.Errorf("no executable available for source %q", source)
	}

	for _, candidate := range candidates {
		if path, err := executable(candidate.Binary); err == nil {
			return Resolved{Source: candidate.Source, Path: path}, nil
		}
	}
	return Resolved{}, fmt.Errorf("no executable available")
}

func executable(binary string) (string, error) {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		return "", fmt.Errorf("empty executable")
	}
	return exec.LookPath(binary)
}
