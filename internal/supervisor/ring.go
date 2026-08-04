package supervisor

import (
	"strings"
	"sync"
)

// outputLimit bounds how much of a child's stdout and stderr a ring keeps.
// It is not user-configurable: this is an implementation detail of how a
// crash reason gets its evidence, not a product decision, and a few
// kilobytes is plenty for the last thing an MCP server said before it died.
const outputLimit = 8 * 1024

// ring keeps the tail of everything written to it, bounded to limit bytes.
// stdout and stderr are both pointed at the same ring, so a traceback and the
// line before it stay in the order the process actually wrote them.
type ring struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func newRing(limit int) *ring {
	return &ring{limit: limit}
}

// Write never fails: a crash reason that lost the tail of the log because
// the log itself could not be written would be a second fault hiding the
// first.
func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if over := len(r.buf) - r.limit; over > 0 {
		r.buf = r.buf[over:]
	}
	return len(p), nil
}

// String returns the tail captured so far, trimmed of surrounding blank
// lines a subprocess tends to leave around its one useful sentence.
func (r *ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(string(r.buf))
}
