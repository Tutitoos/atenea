package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/Tutitoos/atenea/internal/recoverypilot"
)

func cmdRecovery(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "pilot" {
		return fmt.Errorf("recovery requires the pilot subcommand")
	}
	flags := flag.NewFlagSet("recovery pilot", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	fixture := flags.Bool("fixture", false, "run the deterministic fixture")
	root := flags.String("root", "", "disposable fixture root")
	sentinel := flags.String("sentinel", "", "pre-existing external sentinel")
	jsonOutput := flags.Bool("json", false, "print JSON instead of Markdown")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("recovery pilot received an unexpected argument %q", flags.Arg(0))
	}
	if !*fixture {
		return fmt.Errorf("recovery pilot requires --fixture; real recovery gates are not enabled")
	}
	report, err := recoverypilot.RunFixture(context.Background(), recoverypilot.FixtureOptions{Root: *root, Sentinel: *sentinel})
	if err != nil {
		return err
	}
	if *jsonOutput {
		data, err := report.JSON()
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintln(out, string(data)); err != nil {
			return err
		}
	} else if _, err = io.WriteString(out, report.Markdown()); err != nil {
		return err
	}
	if report.Status != "pass" {
		return fmt.Errorf("recovery pilot failed: one or more scenarios did not pass")
	}
	return nil
}
