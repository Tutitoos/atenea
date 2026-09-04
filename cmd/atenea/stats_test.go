package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/toolstats"
)

func TestStatsCalendarPeriods(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		now  string
		o    statsOptions
		want string
	}{
		{"2026-09-04T15:12:00+02:00", statsOptions{today: true}, "2026-09-04T00:00:00+02:00"},
		{"2026-09-04T15:12:00+02:00", statsOptions{week: true}, "2026-08-31T00:00:00+02:00"},
		{"2026-09-04T15:12:00+02:00", statsOptions{month: true}, "2026-09-01T00:00:00+02:00"},
		{"2026-01-01T00:12:00+01:00", statsOptions{week: true}, "2025-12-29T00:00:00+01:00"},
		{"2026-03-29T23:12:00+02:00", statsOptions{today: true}, "2026-03-29T00:00:00+01:00"},
		{"2026-10-25T23:12:00+01:00", statsOptions{today: true}, "2026-10-25T00:00:00+02:00"},
	}
	for _, c := range cases {
		now, _ := time.Parse(time.RFC3339, c.now)
		q, err := c.o.query(now.In(loc))
		if err != nil || q.Since.Format(time.RFC3339) != c.want {
			t.Errorf("%s got %s err %v", c.now, q.Since, err)
		}
	}
	before := time.Date(2026, 8, 31, 23, 59, 59, 0, loc)
	a, _ := (statsOptions{month: true}).query(before)
	b, _ := (statsOptions{month: true}).query(before.Add(time.Second))
	if a.Since.Equal(b.Since) {
		t.Fatal("watch month did not advance")
	}
}
func TestStatsRejectsInvalidOptions(t *testing.T) {
	for _, args := range [][]string{{"--today", "--week"}, {"--month", "--since", "1h"}, {"--watch", "--json"}, {"--color", "bad"}, {"clear"}} {
		if _, err := parseStats(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, since := range []string{"bad", "0s", "-1h", "2999-01-01T00:00:00Z"} {
		if _, err := (statsOptions{since: since}).query(time.Now()); err == nil {
			t.Fatalf("accepted %s", since)
		}
	}
	if err := cmdStats("", []string{"--watch"}, &bytes.Buffer{}); err == nil {
		t.Fatal("watch accepted redirected output")
	}
}
func TestStatsRenderingAndColors(t *testing.T) {
	at := time.Now()
	s := toolstats.Snapshot{At: at, Query: toolstats.Query{Until: at}, Rows: []toolstats.Row{{Tool: toolstats.Tool{Level: "request", Name: strings.Repeat("long", 20), Provider: "p"}, Calls: 1, Fail: 1}, {Tool: toolstats.Tool{Level: "request", Name: "unused", Provider: "p"}}}}
	for _, width := range []int{90, 300} {
		var b bytes.Buffer
		if err := renderStats(&b, s, false, width); err != nil {
			t.Fatal(err)
		}
		body := b.String()
		for _, want := range []string{"CALLS", "REFUSED", "CANCEL", "SIN USO", "ATENEA STATS"} {
			if !strings.Contains(body, want) {
				t.Fatalf("missing %s", want)
			}
		}
		if strings.Contains(body, "\x1b") {
			t.Fatal("redirect contains ANSI")
		}
		if width == 90 && !strings.Contains(body, "mean=") {
			t.Fatal("missing narrow detail")
		}
	}
	t.Setenv("NO_COLOR", "1")
	if statsColor("always", true) {
		t.Fatal("NO_COLOR ignored")
	}
}
func TestStatsDiskAndSocketDoNotGenerateCalls(t *testing.T) {
	path, _ := isolated(t)
	privateStatsFixture(t, path)
	cfg, err := config.LoadEffective(path)
	if err != nil {
		t.Fatal(err)
	}
	var before bytes.Buffer
	if err = cmdStats(path, []string{"--json"}, &before); err != nil {
		t.Fatal(err)
	}
	var initial toolstats.Snapshot
	if err = json.Unmarshal(before.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Coverage.Started != nil {
		t.Fatal("reading started recording")
	}
	stop := serveFrom(t, path)
	defer stop()
	one, err := core.AskedStats(toolstats.Query{})
	if err != nil {
		t.Fatal(err)
	}
	two, err := core.AskedStats(toolstats.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if one.Source != "service" || len(two.Errors) != 0 {
		t.Fatalf("%+v", two)
	}
	for _, r := range two.Totals {
		if r.Calls != 0 {
			t.Fatal("stats generated calls")
		}
	}
	disk, err := core.StatsFromDisk(context.Background(), cfg, toolstats.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if disk.Coverage.Started != nil {
		t.Fatal("stats created new history")
	}
}
func TestStatsJSONAndUsedFilter(t *testing.T) {
	path, _ := isolated(t)
	privateStatsFixture(t, path)
	cfg, err := config.LoadEffective(path)
	if err != nil {
		t.Fatal(err)
	}
	base := cfg.Metrics.Path
	if base == "" {
		t.Skip("fixture does not set metrics path")
	}
	s := toolstats.New(toolstats.Path(base))
	_, c := s.Begin(context.Background(), toolstats.Event{Level: "request", Tool: "code.search", Provider: "atenea", Repository: "api"})
	c.End(nil)
	_ = s.Close()
	var b bytes.Buffer
	if err = cmdStats(path, []string{"--today", "--used", "--json"}, &b); err != nil {
		t.Fatal(err)
	}
	var out toolstats.Snapshot
	if err = json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 1 || out.Rows[0].Calls != 1 {
		t.Fatalf("%+v", out.Rows)
	}
}
func TestStatsNoDatabaseCreation(t *testing.T) {
	path, _ := isolated(t)
	privateStatsFixture(t, path)
	cfg, err := config.LoadEffective(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metrics.Path == "" {
		return
	}
	_ = os.Remove(toolstats.Path(cfg.Metrics.Path))
	var b bytes.Buffer
	if err = cmdStats(path, []string{"--json"}, &b); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(toolstats.Path(cfg.Metrics.Path) + "*")
	if len(matches) != 0 {
		t.Fatalf("created %v", matches)
	}
}

func TestStatsCountsCLIValidationWithoutDispatch(t *testing.T) {
	path, _ := isolated(t)
	privateStatsFixture(t, path)
	var b bytes.Buffer
	if err := cmdAsk(path, []string{"missing", "--repo", "work"}, &b); err == nil {
		t.Fatal("unknown capability accepted")
	}
	cfg, err := config.LoadEffective(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := core.StatsFromDisk(context.Background(), cfg, toolstats.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Totals {
		if r.Level == "request" && (r.Calls != 1 || r.Fail != 1) {
			t.Fatalf("request %+v", r)
		}
		if r.Level == "attempt" && r.Calls != 0 {
			t.Fatalf("validation dispatched %+v", r)
		}
	}
}

// privateStatsFixture makes only the isolated test's metrics directory private.
func privateStatsFixture(t *testing.T, path string) {
	t.Helper()
	cfg, err := config.LoadEffective(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metrics.Path != "" {
		if err = os.Chmod(filepath.Dir(cfg.Metrics.Path), 0700); err != nil {
			t.Fatal(err)
		}
	}
}
