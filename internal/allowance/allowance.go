// Package allowance is the one home for money-to-reading arithmetic: what a
// dollar of a step's own share buys as reading, in the same input-equivalent
// unit a turn's usage is weighed in, and the smallest share that buys any
// reading at all before its own first event outweighs it.
//
// internal/workflow cannot import internal/agent/planner or
// internal/agent/model directly to reach this arithmetic -- model -> core ->
// workflow already, by `go list -deps` -- so this is a stdlib-only leaf both
// sides depend on instead of each other. Every figure below is measured,
// never declared: see each constant's own doc for the receipt it was
// checked against, and MinShareUSD for the admission rule built on them.
package allowance

// readShare is the fraction of a step's own budget spent on reading; the
// rest is held back for the answer, via model.Request.ReadTokens.
//
// Measured 2026-08-14: twelve of twelve real steps spent their whole ceiling
// reading -- code.search, symbol.definition, the tools these two agents are
// handed -- and every one of them hit --max-budget-usd before a single
// result field was written. $3.78 across twelve turns, result_len 0 on all
// twelve. The model was never told to stop reading and answer; it just kept
// paying until the process killed it mid-turn, with the answer it would have
// written nowhere on the record. Reserving part of the grant turns that death
// into a request: read on this share, then answer with what you have.
//
// The fraction is a half rather than three quarters, and that came from a
// measurement too. At 0.75 the same twelve steps still died, and so did a
// single step re-run at $0.90: the finalize pass is not free, and it is the
// most expensive pass of the turn -- it carries the whole grown context,
// ~57,900 input-equivalent tokens on a real explore turn, and the CLI
// overshoots its own --max-budget-usd by up to 1.6x while getting there
// ($0.35 spent against a $0.22 ceiling, measured). A quarter held back is
// swallowed by that overshoot before a word is written.
const readShare = 0.5

// tokensPerUSD converts the reserved dollar share into a token count, in the
// same input-equivalent unit model.Request.ReadTokens is weighed in (input
// x1, cache creation x2 for this CLI's 1-hour cache entries, cache read
// x0.1, output x5 -- see Weigh).
//
// It has to be tokens, not dollars: the CLI prices a turn only once it ends
// -- no mid-turn cost signal exists -- and an explore step does its whole
// job inside turn one, so a dollar-denominated nudge never fires before the
// hard ceiling kills the turn. A same-evening probe confirmed a nudge
// injected mid-turn IS acted on: sent 2.75s in, the model finished its
// in-flight tool call and answered the full schema with completeness 0.05,
// no result event ever seen.
//
// Reconciled 2026-08-14 against two real turns' own receipts, and they do not
// agree -- which is the reason this figure is the lower of the two. A short
// turn (input 4, cache_creation 39,193, cache_read 32,799, output 1,067)
// weighs 87,004 and was charged $0.261685: 332,700 per dollar. A full explore
// turn on the taxiprime backend (input 16, cache_creation 56,921, cache_read
// 356,434, output 5,279) weighs 175,896 and was charged $1.058432: 166,200
// per dollar, half as many. The weighting's ratios are input-relative, so a
// turn's rate moves with which model answered it and with the 1-hour cache
// premium; explore and plan run claude-opus-5 here, and the expensive
// reading-heavy shape is the one this mechanism exists for.
//
// So the estimate is deliberately the pessimistic end of what was measured.
// Being wrong low nudges a turn earlier than it had to be, which costs some
// coverage; being wrong high nudges it after the CLI has already killed it,
// which costs the whole answer. An earlier figure here (333,333) was measured
// on the cheap turn alone, and at $0.90 a step it put the nudge past the
// ceiling: measured, that run spent $1.06 and wrote nothing.
const tokensPerUSD = 166000

// Weigh converts one usage reading into input-equivalent tokens: one number
// that four differently-priced kinds of token can be compared against a
// single allowance in.
//
// The weights are price ratios normalised to input tokens at $3/M: input x1,
// cache creation x2 ($6/M), cache read x0.1 ($0.30/M), output x5 ($15/M). So
// tokensPerUSD is 333,333 by construction, which is the number a caller
// converts a dollar share with -- see Tokens. Kept as integer arithmetic,
// which truncates, because the figure is an ESTIMATE and rounding it
// precisely would dress it up as something it is not.
//
// The cache creation weight is x2 and not the x1.25 of a 5-minute write,
// because this CLI writes 1-hour cache entries. Measured 2026-08-14 against
// one live turn's own receipt: cache_creation reported
// ephemeral_1h_input_tokens 39,193 and ephemeral_5m_input_tokens 0, and these
// weights reproduce the CLI's own total_cost_usd of $0.261685 to within 0.26%,
// and a second turn's $0.536291 to within 0.14%.
// At x1.25 the same arithmetic lands 34% low, which is a nudge that fires a
// third of the way past where it was asked to.
//
// The cache weights are also the only ones that decide anything, which is not
// obvious from the field names: measured on the same turn, input_tokens was
// 2 against 32,799 cache-creation tokens, so a check written on input alone
// would read a turn that had just spent $0.20 as having spent nothing.
//
// It is still an estimate, in one way that matters: the ratios are one
// model's, and the model a turn actually ran on is whatever internal/config
// named. Its only job is to fire the nudge early enough to leave room for an
// answer, and being wrong by a factor makes the nudge early or late, never
// wrong. What the turn may actually spend is still --max-budget-usd, enforced
// by the CLI against its own arithmetic, and what the caller is told it spent
// is still the CLI's own total. This number is never reported to anybody.
func Weigh(input, output, cacheRead, cacheWrite int) int {
	return input + cacheWrite*2 + cacheRead/10 + output*5
}

// WarmDiscount converts a cold-equivalent price into the warm one: the same
// tokens read out of the provider's cache instead of written into it, x0.1
// against x2 by Weigh's own ratios, so a twentieth.
//
// It is the money form of the split StartWeight and WarmStartWeight
// are the token forms of, and it exists as one exported number because three
// packages would otherwise each divide by their own 20. Measured 2026-08-15
// against two live probes and five loopback runs: the same prefix and the
// same first-tool-call block came back written once and then read at this
// ratio on every run after, cache_read pinned to the token.
//
// A cold price times this is what a step pays; the cold price itself is what
// establishing the cache costs, once, on whichever run of the hour is first.
func WarmDiscount(coldUSD float64) float64 {
	return coldUSD * 0.05
}

// Tokens is readShare of shareUSD, in input-equivalent tokens: what
// model.Request.ReadTokens is given for a step funded at shareUSD. Zero for
// an ungranted share, which makes ReadTokens zero too -- off, the same
// reading ReadTokens gives every zero.
func Tokens(shareUSD float64) int {
	return int(readShare * shareUSD * tokensPerUSD)
}

// StartWeight is the input-equivalent weight of everything a turn pays for
// before it can read anything of its own, from cold: the prefix that arrives
// with the prompt PLUS the block that arrives with its first tool call, all of
// it weighed as cache write.
//
// The first tool call is in here because that is where the money is, and
// leaving it out was the last place a rule on this page still priced turn 1.
// Measured 2026-08-15 on taxiprime-backend/claude-opus-5, one run, message by
// message: the prompt carried 5,647 cache-creation tokens and the block
// arriving with the first tool result carried 41,927 -- 7.4x the prefix, on
// the same turn, before the model had read a second thing. A threshold built
// on the prefix alone asks whether a step can afford to be handed its prompt;
// the question worth asking is whether it can afford to still be reading after
// its first tool call returns, which is where thirty-six reader steps died
// saying they had located nothing.
//
// The earlier reading this replaced, kept because it is the same arithmetic on
// a smaller input: 2026-08-14, one live turn surveying this repository, the
// first assistant event alone weighed 65,625 input-equivalent tokens -- about
// $0.20 -- from 32,799 cache-creation tokens against input_tokens: 2.
//
// "because any step may be the first one to pay for it" used to stand here
// as the reason every step was checked against this number. It was wrong,
// and it was wrong in the expensive direction: measured 2026-08-15, the cold
// write happens once per machine per cache lifetime and every step after it
// reads the same bytes for a twentieth of the price. One step of a plan pays
// this; the rest pay WarmStartWeight. See it for the receipts.
func StartWeight(startTokens, input, output int) int {
	return Weigh(input, output, 0, startTokens)
}

// WarmStartWeight is the same start on a machine whose cache already holds
// both blocks: the identical token count weighed as cache READ (x0.1) instead
// of cache write (x2). Twenty times smaller than the cold reading, on the same
// stored numbers -- which is sound because both counts are cache-state
// invariant by construction, see floor.Measurement.PrefixTokens and
// FirstCallTokens.
//
// Measured 2026-08-15, agent reader on taxiprime-backend/claude-opus-5, two
// matched cold-start runs differing only in whether --max-budget-usd was
// passed: the first tool call read 40,227 cached tokens and wrote 1,674 in
// one arm, 1,707 in the other, and the whole step -- read a file, answer the
// schema -- was billed $0.0913 and $0.0884. The cold reading of that same
// block was paid ONCE, by the first run of the day, at 41,927 cache-write
// tokens and $0.4935; every run since has read it. Five consecutive runs
// against different commissions, different files and different nonces all
// reported cache_read 40,227 exactly, which is what makes the cold write a
// one-time machine-wide cost rather than a per-step one.
//
// So this is the reading an admission rule wants per step, and StartWeight is
// the one a caller wants when asking what establishing the cache costs once.
// Charging every step the cold figure prices twenty steps for a write that
// happens on one of them.
func WarmStartWeight(startTokens, input, output int) int {
	return Weigh(input, output, startTokens, 0)
}

// MinShareUSD is the smallest share, in dollars, that clears the allowance
// rule against a turn whose own start weighs startWeight: the smallest share
// for which Tokens(share) > startWeight, strict -- a share that buys exactly
// the prompt and the first tool call and no more still nudges the model to
// answer before it has read anything of its own.
//
// startWeight+1 because Tokens truncates to an int and the rule is
// strict: the smallest token count that clears "> startWeight" is
// startWeight+1. The 1e-9 cushion is the same last-bit-of-float concern
// internal/workflow's moneyEpsilon exists for: without it, a share computed
// to land exactly on startWeight+1 tokens can truncate one token short
// after the float round trip, refusing the exact number this function just
// handed back. The result is a raw share, not yet rounded to cents -- every
// caller ceilings it to cents before printing it, never floors it, so a
// person who types the printed number is admitted.
func MinShareUSD(startWeight int) float64 {
	return float64(startWeight+1) / (readShare * tokensPerUSD) * (1 + 1e-9)
}
