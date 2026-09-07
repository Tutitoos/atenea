package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Tutitoos/atenea/internal/acceptance"
)

func main() {
	output := flag.String("output", "", "artifact directory")
	flag.Parse()
	root, err := repositoryRoot()
	if err == nil && *output == "" {
		*output = filepath.Join(root, "benchmarks", "runs", "acceptance-local")
	}
	if err == nil {
		var report acceptance.Report
		report, err = acceptance.Run(context.Background(), root, nil)
		if err == nil {
			err = acceptance.Write(*output, report)
			fmt.Printf("acceptance passed=%d failed=%d output=%s\n", report.Summary["passed"], report.Summary["failed"], *output)
			if report.Summary["failed"] > 0 {
				err = fmt.Errorf("one or more acceptance gates failed")
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func repositoryRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	raw, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return filepath.Clean(string(bytesTrimSpace(raw))), nil
}

func bytesTrimSpace(value []byte) []byte {
	for len(value) > 0 && (value[0] == ' ' || value[0] == '\n' || value[0] == '\t' || value[0] == '\r') {
		value = value[1:]
	}
	for len(value) > 0 {
		last := value[len(value)-1]
		if last != ' ' && last != '\n' && last != '\t' && last != '\r' {
			break
		}
		value = value[:len(value)-1]
	}
	return value
}
