package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-isatty"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/toolstats"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const statsHelp = `Usage: atenea stats [flags]

Read recorded activity without probing tools. Defaults to all retained history.
  --today             today, from local midnight
  --week              current calendar week, from Monday
  --month             current calendar month, from day 1
  --since DURATION    rolling duration or RFC3339 timestamp
                      choose only one period option
  --repo ID           filter repository
  --provider ID       filter provider
  --tool TEXT         filter tool names by substring
  --used              omit tools with no activity in this period
  --json              structured output
  --color MODE        auto (default), always, never; respects NO_COLOR
  --watch             refresh every two seconds (interactive terminal only)

Examples:
  atenea stats --today
  atenea stats --week --provider kivgraph
  atenea stats --month --json
  atenea stats --today --used --watch
`

type statsOptions struct {
	today, week, month, used, json, watch bool
	since, repo, provider, tool, color    string
}

func parseStats(args []string) (statsOptions, error) {
	var o statsOptions
	f := flag.NewFlagSet("stats", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.BoolVar(&o.today, "today", false, "")
	f.BoolVar(&o.week, "week", false, "")
	f.BoolVar(&o.month, "month", false, "")
	f.StringVar(&o.since, "since", "", "")
	f.StringVar(&o.repo, "repo", "", "")
	f.StringVar(&o.provider, "provider", "", "")
	f.StringVar(&o.tool, "tool", "", "")
	f.BoolVar(&o.used, "used", false, "")
	f.BoolVar(&o.json, "json", false, "")
	f.BoolVar(&o.watch, "watch", false, "")
	f.StringVar(&o.color, "color", "auto", "")
	if err := f.Parse(args); err != nil {
		return o, contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if f.NArg() > 0 {
		return o, contract.Fail(contract.FailureInvalidInput, "stats takes flags only")
	}
	count := 0
	f.Visit(func(v *flag.Flag) {
		switch v.Name {
		case "today", "week", "month", "since":
			count++
		}
	})
	if count > 1 {
		return o, contract.Fail(contract.FailureInvalidInput, "choose only one of --today, --week, --month, --since")
	}
	if o.color != "auto" && o.color != "always" && o.color != "never" {
		return o, contract.Fail(contract.FailureInvalidInput, "--color must be auto, always or never")
	}
	if o.watch && o.json {
		return o, contract.Fail(contract.FailureInvalidInput, "--watch cannot be combined with --json")
	}
	return o, nil
}
func (o statsOptions) query(now time.Time) (toolstats.Query, error) {
	q := toolstats.Query{Until: now, Repository: o.repo, Provider: o.provider, Tool: o.tool, Used: o.used}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch {
	case o.today:
		q.Since = midnight
	case o.week:
		q.Since = midnight.AddDate(0, 0, -(int(now.Weekday())+6)%7)
	case o.month:
		q.Since = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	case o.since != "":
		if duration, err := time.ParseDuration(o.since); err == nil {
			if duration <= 0 {
				return q, contract.Fail(contract.FailureInvalidInput, "--since duration must be positive")
			}
			q.Since = now.Add(-duration)
		} else {
			parsed, e := time.Parse(time.RFC3339, o.since)
			if e != nil {
				return q, contract.Fail(contract.FailureInvalidInput, "--since expects a duration or RFC3339 timestamp")
			}
			q.Since = parsed
		}
	}
	if err := q.Validate(); err != nil {
		return q, contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	return q, nil
}
func statsTTY(out io.Writer) bool {
	f, ok := out.(*os.File)
	return ok && (isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()))
}
func statsColor(mode string, tty bool) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return mode == "always" || (mode == "auto" && tty)
}
func cmdStats(settingsPath string, args []string, out io.Writer) error {
	o, err := parseStats(args)
	if err != nil {
		return err
	}
	if _, err = o.query(time.Now()); err != nil {
		return err
	}
	tty := statsTTY(out)
	if o.watch && !tty {
		return contract.Fail(contract.FailureInvalidInput, "--watch requires an interactive terminal")
	}
	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fetch := func(q toolstats.Query) (toolstats.Snapshot, error) {
		if status, ok := core.Asked(); ok && status.Settings == cfg.Source {
			s, e := core.AskedStats(q)
			if e == nil {
				return s, nil
			}
			s, e2 := core.StatsFromDisk(ctx, cfg, q)
			if e2 != nil {
				return s, e2
			}
			s.Service = "unavailable"
			s.Coverage.Partial = true
			s.Coverage.Notes = append(s.Coverage.Notes, "Live stats unavailable: "+toolstats.Clean(e.Error(), 160)+". Showing persisted data.")
			return s, nil
		}
		return core.StatsFromDisk(ctx, cfg, q)
	}
	if o.watch {
		if _, err = fmt.Fprint(out, "\x1b[?1049h\x1b[?25l"); err != nil {
			return err
		}
		defer func() { _, _ = fmt.Fprint(out, "\x1b[?25h\x1b[?1049l") }()
	}
	for {
		q, e := o.query(time.Now())
		if e != nil {
			return e
		}
		snapshot, e := fetch(q)
		if e != nil {
			return e
		}
		if o.json {
			return json.NewEncoder(out).Encode(snapshot)
		}
		if o.watch {
			if _, e = fmt.Fprint(out, "\x1b[H\x1b[2J"); e != nil {
				return e
			}
		}
		if e = renderStats(out, snapshot, statsColor(o.color, tty), statsWidth(out)); e != nil {
			return e
		}
		if !o.watch {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}
func statsWidth(out io.Writer) int {
	if f, ok := out.(*os.File); ok {
		if n := statsTerminalWidth(f); n > 0 {
			return n
		}
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n >= 40 {
		return n
	}
	return 140
}
func paint(s, code string, color bool) string {
	if !color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func durationCell(us *int64) string {
	if us == nil {
		return "—"
	}
	return (time.Duration(*us) * time.Microsecond).String()
}
func percentCell(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", *p)
}
func lastCell(at *time.Time) string {
	if at == nil {
		return "—"
	}
	return at.Local().Format("01-02 15:04")
}
func rowTone(r toolstats.Row) string {
	if r.Fail > 0 {
		return "31"
	}
	if r.Refused > 0 {
		return "33"
	}
	if r.OK == 0 {
		return "90"
	}
	return "32"
}
func statsCells(r toolstats.Row) []string {
	name := toolstats.Clean(r.Name, 180)
	if r.Calls == 0 && r.Active == 0 {
		name += " [SIN USO]"
	}
	return []string{name, fmt.Sprint(r.Calls), fmt.Sprint(r.OK), fmt.Sprint(r.Refused), fmt.Sprint(r.Fail), fmt.Sprint(r.Cancel), percentCell(r.OKPercent), durationCell(r.MeanUS), durationCell(r.P95US), durationCell(r.MaxUS), lastCell(r.Last)}
}
func asciiTable(headers []string, rows [][]string, tones []string, color bool) string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range rows {
		for i, c := range row {
			if n := utf8.RuneCountInString(c); n > widths[i] {
				widths[i] = n
			}
		}
	}
	var b strings.Builder
	border := func() {
		b.WriteByte('+')
		for _, w := range widths {
			b.WriteString(strings.Repeat("-", w+2))
			b.WriteByte('+')
		}
		b.WriteByte('\n')
	}
	line := func(cells []string, tone string) {
		var line strings.Builder
		line.WriteByte('|')
		for i, c := range cells {
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c))
			if i == 0 {
				line.WriteString(" " + c + pad + " |")
			} else {
				line.WriteString(" " + pad + c + " |")
			}
		}
		b.WriteString(paint(line.String(), tone, color))
		b.WriteByte('\n')
	}
	border()
	line(headers, "36")
	border()
	for i, row := range rows {
		line(row, tones[i])
	}
	border()
	return b.String()
}
func renderStats(out io.Writer, s toolstats.Snapshot, color bool, width int) error {
	var b strings.Builder
	since := "todo el histórico"
	if !s.Query.Since.IsZero() {
		since = s.Query.Since.Local().Format(time.RFC3339)
	}
	fmt.Fprintf(&b, "%s  servicio=%s  fuente=%s\n", paint("ATENEA STATS", "36", color), s.Service, s.Source)
	fmt.Fprintf(&b, "Periodo: %s → %s (%s)\nActualizado: %s\n", since, s.Query.Until.Local().Format(time.RFC3339), time.Local, s.At.Local().Format(time.RFC3339))
	if s.Coverage.Started != nil {
		fmt.Fprintf(&b, "Registro desde: %s | operaciones perdidas: %d\n", s.Coverage.Started.Local().Format(time.RFC3339), s.Coverage.Dropped)
	}
	if s.Query.Repository != "" || s.Query.Provider != "" || s.Query.Tool != "" {
		fmt.Fprintf(&b, "Filtros: repo=%s provider=%s tool=%s\n", toolstats.Clean(s.Query.Repository, 80), toolstats.Clean(s.Query.Provider, 80), toolstats.Clean(s.Query.Tool, 80))
	}
	headers := []string{"TOOL", "CALLS", "OK", "REFUSED", "FAIL", "CANCEL", "OK%", "MEAN", "P95", "MAX", "LAST"}
	for _, level := range []string{"request", "attempt"} {
		title := "SOLICITUDES A ATENEA"
		if level == "attempt" {
			title = "INTENTOS POR HERRAMIENTA / PROVEEDOR"
		}
		fmt.Fprintf(&b, "\n%s\n", paint(title, "36", color))
		var group []toolstats.Row
		provider := ""
		flush := func() {
			if len(group) == 0 {
				return
			}
			fmt.Fprintf(&b, "Proveedor: %s\n", toolstats.Clean(provider, 80))
			var cells [][]string
			var tones []string
			for _, r := range group {
				cells = append(cells, statsCells(r))
				tones = append(tones, rowTone(r))
			}
			table := asciiTable(headers, cells, tones, false)
			firstLine := strings.SplitN(table, "\n", 2)[0]
			if len(firstLine) > width {
				for i := range cells {
					cells[i] = cells[i][:7]
					cells[i][0] = toolstats.Clean(cells[i][0], max(12, width-65))
				}
				b.WriteString(asciiTable(headers[:7], cells, tones, color))
				for _, r := range group {
					fmt.Fprintf(&b, "  %s: mean=%s p95=%s max=%s last=%s active=%d\n", toolstats.Clean(r.Name, max(12, width-70)), durationCell(r.MeanUS), durationCell(r.P95US), durationCell(r.MaxUS), lastCell(r.Last), r.Active)
				}
			} else {
				b.WriteString(asciiTable(headers, cells, tones, color))
				for _, r := range group {
					if r.Active > 0 {
						fmt.Fprintf(&b, "  En curso: %s = %d\n", toolstats.Clean(r.Name, 120), r.Active)
					}
				}
			}
			group = nil
		}
		for _, r := range s.Rows {
			if r.Level != level {
				continue
			}
			if r.Provider != provider {
				flush()
				provider = r.Provider
			}
			group = append(group, r)
		}
		flush()
		for _, r := range s.Totals {
			if r.Level == level {
				fmt.Fprintf(&b, "TOTAL calls=%d ok=%d refused=%d fail=%d cancel=%d active=%d ok=%s\n", r.Calls, r.OK, r.Refused, r.Fail, r.Cancel, r.Active, percentCell(r.OKPercent))
			}
		}
	}
	if len(s.Legacy) > 0 {
		fmt.Fprintln(&b, "\nMÉTRICAS ANTERIORES (intentos; no se suman a solicitudes)")
		var rows [][]string
		var tones []string
		for _, r := range s.Legacy {
			rows = append(rows, []string{toolstats.Clean(r.Tool, 100), toolstats.Clean(r.Provider, 80), toolstats.Clean(r.Repository, 80), fmt.Sprint(r.Calls), fmt.Sprint(r.OK), fmt.Sprint(r.Unclassified), durationCell(r.MeanUS), durationCell(r.MaxUS)})
			tones = append(tones, "90")
		}
		b.WriteString(asciiTable([]string{"TOOL", "PROVIDER", "REPO", "CALLS", "OK", "ERROR SIN CLASIFICAR", "MEAN OK", "MAX"}, rows, tones, color))
	}
	for _, t := range s.Catalog {
		if t.State == "catalog_unknown" {
			fmt.Fprintf(&b, "Catálogo incompleto: %s (aún no descubierto)\n", toolstats.Clean(t.Name, 140))
		}
	}
	if len(s.Errors) > 0 {
		fmt.Fprintln(&b, "\nÚLTIMOS ERRORES / RECHAZOS")
		for _, e := range s.Errors {
			fmt.Fprintf(&b, "%s %s [%s] %s\n", e.At.Local().Format("01-02 15:04:05"), toolstats.Clean(e.Tool, 100), toolstats.Clean(e.Code, 80), paint(toolstats.Clean(e.Reason, 240), "33", color))
		}
	}
	fmt.Fprintln(&b, "\nOK%: llamadas terminadas. Tiempos: terminadas sin cancelaciones. —: sin medición completa.")
	for _, note := range s.Coverage.Notes {
		fmt.Fprintf(&b, "Aviso: %s\n", toolstats.Clean(note, 320))
	}
	_, err := io.WriteString(out, b.String())
	return err
}
