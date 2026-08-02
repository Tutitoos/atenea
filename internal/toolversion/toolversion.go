// Package toolversion asks a command-line tool who it is, once per process.
//
// The settings file already declares a version for every implementation, and
// that declaration is exactly what cannot be trusted for a baseline: the case
// worth catching is a tool upgraded on disk by somebody who never opened the
// TOML. Measurements filed under a stale version quietly average the new
// binary's numbers into the old one's, and the selector takes weeks to notice
// an improvement it should have seen on the second call.
//
// # Once
//
// The probe is memoised for the life of the process. In a CLI that is a single
// command, so the answer is always fresh. In a long-running core it means an
// upgrade performed underneath a live Atenea is picked up at the next restart
// rather than immediately -- a bounded staleness, chosen over paying for a
// process spawn on every single dispatch.
//
// # Silence is an answer
//
// A tool that is not installed, refuses the flag, or hangs leaves the version
// empty. Empty travels down to the store as "would not say", which is a fact.
// Inventing "unknown" or reusing the declared string would not be.
package toolversion

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout caps the probe. Announcing a version is the cheapest thing a
// CLI does; anything slower than this is a tool that is not going to answer.
const DefaultTimeout = 5 * time.Second

// Probe asks one binary for its version and remembers what it said.
type Probe struct {
	binary  string
	args    []string
	timeout time.Duration

	once    sync.Once
	version string
}

// New returns a probe for binary, invoked with args. Nothing runs until the
// first call to Version.
func New(binary string, args ...string) *Probe {
	return &Probe{binary: binary, args: args, timeout: DefaultTimeout}
}

// Version reports what the tool calls itself, or empty if it would not say.
//
// It never returns an error on purpose. A version is metadata about a
// measurement, and failing a real dispatch because the far side was shy about
// its version number would be the tail wagging the dog.
func (p *Probe) Version(ctx context.Context) string {
	p.once.Do(func() { p.version = p.ask(ctx) })
	return p.version
}

func (p *Probe) ask(ctx context.Context) string {
	if strings.TrimSpace(p.binary) == "" {
		return ""
	}
	// The probe gets its own deadline rather than the caller's: a dispatch
	// running under a generous timeout should not be able to spend all of it
	// here, and one running under a tight deadline that has nearly expired
	// should still get to record its version.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, p.binary, p.args...).Output()
	if err != nil {
		return ""
	}
	return Clean(string(out))
}

// Clean reduces a version banner to something worth storing.
//
// Tools answer this question in every shape there is: a bare number, a name
// and a number, sometimes a paragraph. The first non-empty line is the part
// they all agree on, and a long one is cut rather than kept, because this
// string is a grouping key and a key nobody will ever match twice is not a key.
func Clean(raw string) string {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		const maxKey = 120
		if len(line) > maxKey {
			line = strings.TrimSpace(line[:maxKey])
		}
		return line
	}
	return ""
}
