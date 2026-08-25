package orchestrator

import (
	"math"
	"sync"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// grant is a commission's spending ceiling while the commission is running.
//
// It exists because money is the one permission that is consumed. Effects are
// copied down to every child and stay true; a ceiling copied down to every
// child is spent once per child, which is how four steps used to spend one
// ceiling four times over.
//
// # Why splitting is enough
//
// A wave asks for as many shares as it has steps, and gets what is left
// divided evenly among them. The sum of the shares handed out is therefore
// exactly what was left, so even if every step spends its share to the last
// cent the wave cannot draw more than the commission had. Waves run one after
// another, so the next wave divides whatever the last one did not touch.
//
// That is the whole guarantee, and it holds without reserving anything:
// nothing has to be given back, because nothing was taken away in advance. A
// step that spends nothing simply never calls spend, and the money is still
// there for the wave behind it.
//
// # What it is not
//
// It is not a cost model and it never ranks anybody. The funnel decides who
// answers on duration, tokens and memory; this decides only whether the answer
// is allowed to cost money at all. A commission that runs out keeps working
// through whoever charges nothing.
type grant struct {
	mu   sync.Mutex
	left float64
}

// validGrant refuses a ceiling that is not a number of dollars, and it is the
// one place every entry point asks -- Run, Ask, Resume, RunPlan and New.
//
// The three refusals are one rule, not three special cases: a ceiling has to
// be a finite amount somebody could be charged. A negative grant is a typo,
// and clamping it to zero would switch off every paid provider for the run so
// the operator reads their own mistake as an outage. NaN compares false
// against everything, so a NaN ceiling refuses nothing and permits nothing in
// a way that depends on which side of the comparison it lands. +Inf is the
// dangerous one and the reason this function exists: it propagates into
// contract.Permission.BudgetUSD, and every check at the edge is a comparison
// against a ceiling nothing can exceed, so the provider's charge stops being
// verified at all. RunPlan already refused all three; the other three doors
// let +Inf through, which made the strictest entry point the one nobody
// calls from a terminal.
//
// The caller passes its own name, because "budget must be finite" with no
// subject in front of it is a line an operator cannot act on: the same
// sentence has to say whether the number came from --budget, from a resumed
// run, or from the settings file.
func validGrant(what string, usd float64) error {
	if usd < 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return contract.Fail(contract.FailureInvalidInput,
			"%s: budget must be a finite amount and not negative, got %v", what, usd)
	}
	return nil
}

// newGrant opens a ceiling. A total of zero is a commission that may not spend,
// which is a legitimate state and not an error: it is what a depleted grant
// looks like, and what a machine with no paid provider attached wants anyway.
func newGrant(total float64) *grant {
	if total < 0 {
		total = 0
	}
	return &grant{left: total}
}

// shares divides what is left among n steps, evenly.
//
// It reads rather than reserves. The division is the guarantee: n shares of
// left/n add up to left, so a wave holding every one of them still cannot
// exceed the commission. Handing back an unspent reservation would move the
// same money twice and buy nothing.
//
// An even split is deliberately blind to which steps will cost anything, and
// it has to be: which implementation answers a step is settled by the funnel
// at dispatch, after the shares are cut, and no implementation declares that
// it charges. A free step simply never spends its share, and the wave behind
// it divides the money again.
func (g *grant) shares(n int) []float64 {
	if n <= 0 {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]float64, n)
	if g.left <= 0 {
		return out
	}
	share := g.left / float64(n)
	for i := range out {
		out[i] = share
	}
	return out
}

// spend draws what a step was actually charged, as it closes.
//
// Charges arrive from steps of the same wave at the same time, which is the
// only concurrency here: waves themselves are sequential, so a share is never
// cut while a charge from the wave before it is still landing.
func (g *grant) spend(usd float64) {
	if usd <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.left -= usd
	if g.left < 0 {
		// Reachable only if a far side billed past the ceiling it was handed.
		// The books stop at zero because the alternative is a negative share
		// on the next wave, which would read as a debt Atenea intends to
		// collect. What actually happened is on the receipt, in full.
		g.left = 0
	}
}

// left reports what the commission may still draw.
func (g *grant) remaining() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.left
}
