// Command atenea-dart-coverage writes the deterministic P14 Dart capability
// matrix. It only reads fixtures and the pinned acceptance corpus.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tutitoos/atenea/internal/benchmark/corpus"
	"github.com/Tutitoos/atenea/internal/dartcoverage"
)

type runOptions struct {
	FixtureRoot string
	CorpusRoot  string
	CorpusHash  string
	Date        string
	Output      string
}

func main() {
	repository := repositoryRoot()
	flags := flag.NewFlagSet("atenea-dart-coverage", flag.ExitOnError)
	fixtureRoot := flags.String("fixture-root", filepath.Join(repository, "internal", "dartcoverage", "testdata"), "independent Dart fixture root")
	corpusRoot := flags.String("corpus-root", filepath.Join(repository, "benchmarks", "corpus", "v1"), "fixed P01 acceptance corpus root")
	corpusHash := flags.String("corpus-sha256", corpus.CanonicalCorpusSHA256, "fixed corpus SHA-256")
	date := flags.String("date", "2026-09-06", "observation date (YYYY-MM-DD)")
	output := flags.String("output", filepath.Join(repository, "benchmarks", "runs", "dart-coverage-2026-09-06", "report.json"), "JSON report path")
	_ = flags.Parse(os.Args[1:])
	if err := generate(runOptions{FixtureRoot: *fixtureRoot, CorpusRoot: *corpusRoot, CorpusHash: *corpusHash, Date: *date, Output: *output}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("dart coverage report=%s\n", filepath.Base(*output))
}

func generate(options runOptions) error {
	report, err := dartcoverage.Run(dartcoverage.Options{
		FixtureRoot: options.FixtureRoot, CorpusRoot: options.CorpusRoot,
		CorpusSHA256: options.CorpusHash, Date: options.Date,
	})
	if err != nil {
		return fmt.Errorf("generate Dart coverage: %w", err)
	}
	data, err := report.JSON()
	if err != nil {
		return fmt.Errorf("encode Dart coverage: %w", err)
	}
	if strings.TrimSpace(options.Output) == "" {
		return errors.New("dart coverage output is required")
	}
	if err := os.MkdirAll(filepath.Dir(options.Output), 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	if err := os.WriteFile(options.Output, data, 0o644); err != nil {
		return fmt.Errorf("write Dart coverage JSON: %w", err)
	}
	markdown, err := report.Markdown()
	if err != nil {
		return fmt.Errorf("render Dart coverage Markdown: %w", err)
	}
	markdownPath := strings.TrimSuffix(options.Output, filepath.Ext(options.Output)) + ".md"
	if err := os.WriteFile(markdownPath, []byte(markdown), 0o644); err != nil {
		return fmt.Errorf("write Dart coverage Markdown: %w", err)
	}
	return nil
}

func repositoryRoot() string {
	directory, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "."
		}
		directory = parent
	}
}
