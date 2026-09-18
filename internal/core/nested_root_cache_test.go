package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/internal/resultcache"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type fullAuditFileRunner struct {
	*cacheFixtureRunner
	file string
}

func (r *fullAuditFileRunner) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	out, err := r.cacheFixtureRunner.Run(ctx, req)
	if err != nil {
		return out, err
	}
	body, err := os.ReadFile(r.file)
	out.Result["answer"] = string(body)
	return out, err
}

func TestFullAuditNestedRootCacheDoesNotReturnOldContents(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	git("init", "-q")
	sub := filepath.Join(root, "app")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "source.txt")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("initial")
	git("add", ".")
	git("-c", "user.name=Audit", "-c", "user.email=audit@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")
	write("first dirty contents")
	cache, err := resultcache.New(resultcache.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	provider := &fullAuditFileRunner{cacheFixtureRunner: &cacheFixtureRunner{}, file: file}
	runner := cachedRunner{Runner: provider, cache: cache}
	req := cacheFixtureRequest(sub)
	first, err := runner.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Result["answer"] != "first dirty contents" {
		t.Fatalf("invalid fixture: %+v", first)
	}
	write("second dirty contents")
	second, err := runner.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheHit || second.Result["answer"] != "second dirty contents" {
		t.Fatalf("stale result: cache_hit=%v answer=%q physical_calls=%d", second.CacheHit, second.Result["answer"], provider.calls.Load())
	}
}
