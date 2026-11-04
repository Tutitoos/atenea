package contract

import (
	"math"
	"slices"
	"strings"
)

// realMoney reports whether a dollar figure is one a person could have
// authorized: finite and not below zero.
//
// It exists because every comparison against NaN is false, so a validator
// written as `amount < 0` refuses negatives and waves NaN straight through --
// and a NaN budget compares false against every ceiling downstream too, which
// is a grant that is refused by nothing. Infinity fails the same way in the
// other direction: `< 0` is false for +Inf, and what arrives is a figure that
// no ceiling can be smaller than. Neither can come from a person; both come
// from arithmetic upstream that divided by zero or overflowed, and the place
// to stop them is the boundary that admits the number.
func realMoney(amount float64) bool {
	return amount >= 0 && !math.IsInf(amount, 0)
}

// Charge is what one run of an agent cost, in the units the far side actually
// reported.
//
// Two fields and not one, because the two are not equally trustworthy. Tokens
// are counted by whoever served the request and are the same number whatever
// contract the account is on. A dollar figure is arithmetic somebody did with
// a price list, and on subscription traffic the price list is not what was
// billed -- measured on this machine, a proxy's own ledger and the billed
// figure disagreed by a wide margin for the same request. So USD is optional
// and never travels without PricedBy naming whose price produced it, and a
// reader that cannot say whose price it is looking at should show the tokens.
type Charge struct {
	// InputTokens and OutputTokens are what the turn consumed and produced.
	InputTokens  int
	OutputTokens int
	// CacheReadTokens and CacheWriteTokens are counted separately because
	// they are priced differently everywhere, and folding them into the
	// input count would make a warm repository look cheap rather than
	// cached.
	CacheReadTokens  int
	CacheWriteTokens int
	// USD is nil unless the far side priced the turn itself. Nil is not
	// zero: it means nobody said, and a run that cost real money must never
	// read as one that cost nothing.
	USD *float64
	// PricedBy names where USD came from -- which provider, which list, at
	// what moment. Required whenever USD is set, refused when it is not,
	// because a dollar figure with no provenance is the exact claim this
	// project keeps finding to be false.
	PricedBy string
}

// Measured reports whether this charge says anything at all. A zero Charge is
// what an agent that spends no tokens hands back, and it must read as
// unmeasured rather than as a free run.
func (c Charge) Measured() bool {
	return c.Tokens() > 0 || c.USD != nil
}

// Tokens is everything the turn moved. Cache reads are in it because they
// were paid for, even if cheaply.
func (c Charge) Tokens() int {
	return c.InputTokens + c.OutputTokens + c.CacheReadTokens + c.CacheWriteTokens
}

// Plus adds a charge to this one: what two runs of the same work cost
// together, which is what a person paying for a relaunch actually owes.
//
// The dollar figure survives only when both halves carry one. A priced turn
// added to an unpriced one is not the priced figure -- that would print the
// smaller number as the total, which is the same partial-measurement lie a
// half-measured workflow refuses to tell. Tokens always add: they are counted
// by whoever served the request and never guessed.
func (c Charge) Plus(o Charge) Charge {
	// The two shortcuts return a copy rather than the operand itself, because
	// USD is a pointer and a fold is the usual caller: summing N charges of
	// which one is measured hands the running total that one charge's own
	// address, so writing to the total afterwards edits the report it came
	// from. Copying costs one float per unmeasured operand and removes the
	// only way this function can reach back into its input.
	if !o.Measured() {
		return c.copyAmount()
	}
	if !c.Measured() {
		return o.copyAmount()
	}
	out := Charge{
		InputTokens:      c.InputTokens + o.InputTokens,
		OutputTokens:     c.OutputTokens + o.OutputTokens,
		CacheReadTokens:  c.CacheReadTokens + o.CacheReadTokens,
		CacheWriteTokens: c.CacheWriteTokens + o.CacheWriteTokens,
	}
	if c.USD == nil || o.USD == nil {
		return out
	}
	total := *c.USD + *o.USD
	out.USD = &total
	out.PricedBy = mergePricedBy(c.PricedBy, o.PricedBy)
	return out
}

// copyAmount returns the charge with its own dollar figure, so the caller
// cannot write through to whatever report supplied it.
func (c Charge) copyAmount() Charge {
	if c.USD != nil {
		amount := *c.USD
		c.USD = &amount
	}
	return c
}

// mergePricedBy names every source that priced a sum, once each.
//
// Two sources priced the two halves and the sum belongs to both: naming one
// would attribute a figure to a source that never said it. Deduplicating is
// what makes that survive a fold. Plus is associative in the caller's mind,
// so a total over N charges is built by adding one at a time, and plain
// concatenation grows the provenance by one name per addition -- fifteen turns
// priced by the same list read back as that list fifteen times, which says
// nothing the first mention did not and buries a genuine second source in the
// repetition.
func mergePricedBy(a, b string) string {
	if a == b {
		return a
	}
	// Both sides are split, not just the incoming one: a fold that has
	// already merged twice carries a joined string on the left AND may be
	// handed another on the right, and treating either as a single opaque
	// name reintroduces the duplicate this exists to remove.
	names := make([]string, 0, 4)
	for _, side := range [2]string{a, b} {
		for _, name := range strings.Split(side, pricedBySeparator) {
			if name != "" && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return strings.Join(names, pricedBySeparator)
}

// pricedBySeparator joins the sources of a summed charge. It reads as a
// sentence because PricedBy is shown to a person, not parsed by a machine.
const pricedBySeparator = " and "

// Validate refuses the two shapes that would put an unsupported number on a
// receipt: a negative count, and a price nobody will stand behind.
func (c Charge) Validate() error {
	for _, pair := range []struct {
		name  string
		value int
	}{
		{"input_tokens", c.InputTokens},
		{"output_tokens", c.OutputTokens},
		{"cache_read_tokens", c.CacheReadTokens},
		{"cache_write_tokens", c.CacheWriteTokens},
	} {
		if pair.value < 0 {
			return Fail(FailureInvalidInput, "charge: %s is negative (%d)", pair.name, pair.value)
		}
	}
	if c.USD == nil {
		if strings.TrimSpace(c.PricedBy) != "" {
			return Fail(FailureInvalidInput,
				"charge: priced_by is %q with no amount beside it", c.PricedBy)
		}
		return nil
	}
	if !realMoney(*c.USD) {
		return Fail(FailureInvalidInput,
			"charge: amount is not a real, non-negative figure (%v)", *c.USD)
	}
	if strings.TrimSpace(c.PricedBy) == "" {
		return Fail(FailureInvalidInput,
			"charge: $%.4f with no priced_by: a dollar figure has to say whose price it used", *c.USD)
	}
	return nil
}
