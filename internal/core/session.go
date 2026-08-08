package core

import (
	"cmp"
	"context"
	"slices"
	"sync/atomic"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Session is one chat's view of Atenea.
//
// The unit of isolation is the CHAT, not the client: omp and Claude Code can
// both be open at once, each with several chats, and none of them may read the
// neighbor's context or borrow its permissions. What they do share is the
// floor underneath -- the catalog, the metrics and the history -- because
// what the stack learns is common property and what a chat was allowed is not.
//
// Nothing here is a second brain. A session is a grant and an entitlement; the
// deciding still happens in exactly one place.
type Session struct {
	id     string
	client string
	core   *Core
	// grant is what commissions from this chat may authorize. Reading is not
	// in it because reading is free by default: the grant is the ceiling on
	// everything heavier.
	grant []contract.Effect
	// levels are the context heights this chat may read. A discovery above
	// them is not withheld as a punishment -- the chat simply has no business
	// with it, and handing it over would be the leak.
	levels []contract.ContextLevel
	opened time.Time
	runs   atomic.Int64
}

// SessionOptions describe the chat asking to be let in.
type SessionOptions struct {
	// ID is the chat's own identifier, as its client already knows it. Empty
	// mints one: a client with no id of its own still gets isolation.
	ID string
	// Client names the CLI the chat belongs to. It buys nothing at runtime and
	// exists so the status screen can show two clients at once, which is the
	// only way anybody sees the isolation working.
	Client string
	// Grant is what commissions from this chat may authorize beyond reading.
	// Empty is the honest default: a fresh chat can look and nothing else.
	Grant []contract.Effect
	// Context lists the heights this chat may read. Empty means the repository
	// level only, because that is what a chat needs to be told about the work
	// it just asked for.
	Context []contract.ContextLevel
}

// Open lets a chat in.
//
// Opening is refused once a stop is under way, for the same reason work is:
// a session handed out during a shutdown would be a promise the core is about
// to break.
func (c *Core) Open(opts SessionOptions) (*Session, error) {
	for _, effect := range opts.Grant {
		if !knownEffect(effect) {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"session %s: unknown effect in grant", opts.ID)
		}
	}
	for _, level := range opts.Context {
		if level == contract.ContextUnspecified {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"session %s: the zero context level is not a declaration", opts.ID)
		}
	}
	levels := slices.Clone(opts.Context)
	if len(levels) == 0 {
		levels = []contract.ContextLevel{contract.ContextRepository}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopping {
		return nil, contract.Fail(contract.FailureUnavailable, "atenea is shutting down")
	}
	id := opts.ID
	if id == "" {
		id = checkpoint.NewID(time.Now())
	}
	if _, taken := c.sessions[id]; taken {
		// Silently handing back the existing session would let a second chat
		// inherit the first one's grant by guessing its name.
		return nil, contract.Fail(contract.FailureInvalidInput,
			"session %s is already open", id)
	}
	session := &Session{
		id:     id,
		client: opts.Client,
		core:   c,
		grant:  slices.Clone(opts.Grant),
		levels: levels,
		opened: time.Now(),
	}
	c.sessions[id] = session
	return session, nil
}

// Close ends a chat. Closing one that was never open is not an error: the
// caller wanted it gone and it is gone.
func (c *Core) Close(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, id)
}

// Sessions lists the chats currently open, oldest first, for the status
// screen. The slice is a copy; the sessions in it are not.
func (c *Core) Sessions() []*Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*Session, 0, len(c.sessions))
	for _, session := range c.sessions {
		out = append(out, session)
	}
	slices.SortFunc(out, func(a, b *Session) int {
		if opened := a.opened.Compare(b.opened); opened != 0 {
			return opened
		}
		return cmp.Compare(a.id, b.id)
	})
	return out
}

// ID of the chat.
func (s *Session) ID() string { return s.id }

// Client the chat belongs to.
func (s *Session) Client() string { return s.client }

// Opened reports when the chat was let in.
func (s *Session) Opened() time.Time { return s.opened }

// Runs reports how many commissions this chat has made.
func (s *Session) Runs() int64 { return s.runs.Load() }

// Grant reports what this chat may authorize beyond reading.
func (s *Session) Grant() []contract.Effect { return slices.Clone(s.grant) }

// Context reports the heights this chat may read.
func (s *Session) Context() []contract.ContextLevel { return slices.Clone(s.levels) }

// Allows reports whether this chat may authorize an effect.
func (s *Session) Allows(effect contract.Effect) bool {
	// Reading is free by default, exactly as it is for a step's permission.
	// A chat that could not read could not do anything at all.
	return effect == contract.EffectRead || slices.Contains(s.grant, effect)
}

// Reads reports whether this chat is entitled to a context height.
func (s *Session) Reads(level contract.ContextLevel) bool {
	return slices.Contains(s.levels, level)
}

// Close ends this chat.
func (s *Session) Close() { s.core.Close(s.id) }

// Do hands a commission to the orchestrator on this chat's behalf.
//
// Two things happen here that do not happen in Core.Do, and they are the whole
// of the isolation. Going in, the chat cannot authorize an effect it was not
// granted, whatever the commission says. Coming out, it is told only what it
// is entitled to know. In between, the run is the same run, written to the
// same shared history: the learning is common, the reach is not.
func (s *Session) Do(ctx context.Context, task orchestrator.Task) (*orchestrator.Result, error) {
	if err := s.entitled(task.Effects); err != nil {
		return nil, err
	}
	task.Session = s.id
	s.runs.Add(1)
	result, err := s.core.Do(ctx, task)
	s.told(result)
	return result, err
}

// Ask dispatches one capability against one repository on this chat's behalf.
//
// It is Do's isolation applied to the shape a tools/call arrives in, through
// the same two halves rather than a second copy of them. Core.Ask is the
// console's door and trusts the effects it is handed, because somebody standing
// at a terminal IS the user and there is nobody above them to ask. A chat is
// not: what it may authorize was decided when it was opened, and a client
// speaking for one has to be held to it.
//
// The gate on the dispatch path does not cover this. That one refuses a
// capability whose effects the COMMISSION does not cover -- and the commission
// is built from what the caller asked for, so it compares a request with
// itself. What a chat is ENTITLED to ask for is only known here.
func (s *Session) Ask(ctx context.Context, q orchestrator.Question) (*orchestrator.Result, error) {
	if err := s.entitled(q.Effects); err != nil {
		return nil, err
	}
	q.Session = s.id
	s.runs.Add(1)
	result, err := s.core.Ask(ctx, q)
	s.told(result)
	return result, err
}

// entitled refuses an effect this chat was not granted.
//
// Refused, not asked. A chat asking for more than it holds is the moment to go
// back to the user, and the core has no user: the client that opened the chat
// does.
func (s *Session) entitled(effects []contract.Effect) error {
	for _, effect := range effects {
		if !s.Allows(effect) {
			return contract.Fail(contract.FailurePermissionDenied,
				"session %s may not authorize %s", s.id, effect)
		}
	}
	return nil
}

// told withholds from a result what this chat has no right to read.
//
// Shared with Do rather than repeated, and deliberately: what an Ask discovers
// comes from the runner reporting on its own answer, which no runner in this
// repository does yet, so a copy here would be a copy nothing exercises -- and
// the first adapter that does report would find out whether the copy had drifted
// by leaking a fact to a chat that may not read it.
func (s *Session) told(result *orchestrator.Result) {
	if result == nil {
		return
	}
	result.Discoveries = s.readable(result.Discoveries)
}

// readable drops what this chat has no right to read.
//
// The run still discovered it and the history still records it. Withholding
// happens on the way to one chat, never on the way to disk, because a fact
// nobody wrote down is a fact the next commission pays to learn again.
func (s *Session) readable(found []contract.Discovery) []contract.Discovery {
	out := make([]contract.Discovery, 0, len(found))
	for _, discovery := range found {
		if s.Reads(discovery.Level) {
			out = append(out, discovery)
		}
	}
	return out
}

// knownEffect reports whether an effect is one the contract declares. A grant
// built from a zero value would quietly widen to nothing and read as if it had
// been honored.
//
// Asked of the contract rather than matched against a list retyped here, and
// that is not tidiness. The retyped list is exactly how this broke: it named
// read, write and external, `process` was added to the contract afterwards, and
// a switch does not notice. The result was that no chat could ever be granted
// `process` -- so no chat could run `code.search`, which declares read AND
// process because ripgrep is both, and which is the first thing any client
// calls. An unknown effect has no name to round-trip, so it still fails.
func knownEffect(effect contract.Effect) bool {
	_, err := contract.ParseEffect(effect.String())
	return err == nil
}
