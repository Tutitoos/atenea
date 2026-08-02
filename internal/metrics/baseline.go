package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// FaultStreak is how many failures in a row make a provider unhealthy.
//
// Three rather than one because a single fault is ordinary -- a timeout, a
// racing index, a file that moved -- and rather than two because the funnel
// hands an unmeasured implementation the break-in turn twice before its
// numbers are believed, and a provider should not be condemned by the very
// calls that were buying its baseline.
const FaultStreak = 3

// FaultWindow is how long the newest failure of a streak keeps counting.
//
// Without it a provider condemned once is condemned forever: health drops it
// from the funnel, so nothing calls it, so the streak that damns it can never
// be broken by a success. The window is the way back. After it passes the
// streak lapses, the next call goes through, and one failure is enough to
// close it again -- the older failures are still on record, so recovery costs
// one call and relapse costs one call, not three either way.
const FaultWindow = 5 * time.Minute

// SuccessWindow is how recent a successful call must be to count as evidence
// that a provider is healthy now.
//
// A success is evidence with a shelf life. "It worked" is a statement about
// the moment it worked: the index was warm, the server was up, the user was
// logged in. None of that is still being claimed a day later, and a screen
// that reported alive because something worked last Tuesday would be reporting
// the past.
//
// An hour, because it has to survive a working session -- two commands ten
// minutes apart should not disagree about whether the machine is well -- and
// must not survive a night. It is also comfortably inside the seven days of
// per-attempt rows the store keeps, so the evidence is always still there to
// read.
const SuccessWindow = time.Hour

// Fault is an unbroken run of failures at the newest end of the record: every
// attempt since the last successful one, or every attempt there has ever been
// when none succeeded.
//
// It is a fact, not a verdict. What it means for the funnel is decided by
// Health below, which is where the thresholds live.
type Fault struct {
	// Streak is how many attempts in a row failed.
	Streak int
	// Kind is the failure bin shared by all of them, and empty when they do
	// not share one. One bin repeated is an outage with a name; a scatter of
	// different bins is a provider in trouble without a diagnosis, and the two
	// deserve different answers.
	Kind string
	// Reason is the untranslated message of the newest failure, for whoever
	// has to read it.
	Reason string
	// Latest is when that newest failure happened.
	Latest time.Time
}

// Health reads a streak as a state the funnel can act on, or reports that
// there is nothing to say.
//
// A run of the same bin is an outage: named, diagnosable, and worth leaving
// the funnel over. A run of mixed bins is a provider that keeps breaking
// differently every time, which is a real finding but not a single fault, so
// it ranks below the healthy ones instead of being dropped.
func (f Fault) Health(now time.Time) (contract.Health, bool) {
	if f.Streak < FaultStreak || now.Sub(f.Latest) > FaultWindow {
		return contract.Health{}, false
	}
	h := contract.Health{ObservedAt: f.Latest}
	if f.Kind != "" {
		h.State = contract.HealthDown
		h.Reason = fmt.Sprintf("%d %s failures in a row, last one %s",
			f.Streak, f.Kind, said(f.Kind, f.Reason))
		return h, true
	}
	h.State = contract.HealthDegraded
	h.Reason = fmt.Sprintf("%d failures in a row with no single cause, last one %s",
		f.Streak, f.Reason)
	return h, true
}

// said drops the bin from the front of a failure message once the sentence
// around it has already named the bin. The stored text is the untranslated
// error, which by convention leads with its own bin, so quoting it whole reads
// "14 unavailable failures in a row, last one unavailable: not logged in".
func said(kind, reason string) string {
	return strings.TrimPrefix(reason, kind+": ")
}

// Baseline is what one implementation costs on one repository, right now.
//
// It is a different question from Summary, which reports everything the store
// ever saw, per version, for a human to read. This one answers the funnel:
// "what does this cost here, at the version that is running today". So it
// carries averages per call rather than totals, and it never mixes versions.
type Baseline struct {
	// Spent is the average SUCCESSFUL call: total over successes, per axis.
	Spent contract.Sample
	// Successes is how many measurements back Spent, and the only count that
	// divides it.
	//
	// Failures are deliberately not in here. The first version of this store
	// averaged every attempt together on the argument that a tool which hangs
	// before failing has still eaten the wait -- true, but it proves the
	// opposite of what it was used for. The same average also makes an
	// implementation that refuses INSTANTLY look like the fastest thing on the
	// machine, and the funnel then gives it everything. Failing cheaply was
	// rewarded. A failure is not a price: it is the absence of one.
	Successes int
	// Attempts is every try, successful or not, and Failures how many broke.
	// Neither divides Spent. They are the health question, and they are what
	// lets the trace explain an implementation with a long record and no
	// numbers.
	Attempts int
	Failures int
	// ToolVersion is the version those attempts belong to.
	ToolVersion string
	// Success is when the newest successful attempt happened, zero if there
	// has never been one. It is the other half of the health question: the
	// streak below says what has been going wrong, and this says whether
	// anything has gone right lately.
	Success time.Time
	// Fault is the failure streak at the newest end of the record.
	Fault Fault
}

// Health reads the newest end of the record as a state the funnel can act on,
// or reports that the record has nothing to say.
//
// It answers in both directions, and the two are not symmetric on purpose.
//
// Downwards, a streak is a verdict: see Fault.Health above.
//
// Upwards, the bar is "the last call here worked, and it worked recently".
// Both halves are load-bearing. Recently, because a success is a statement
// about the moment it happened and SuccessWindow is how long that moment is
// allowed to speak for. Last, because a failure with nothing after it means
// the newest thing anybody knows is that this broke -- not enough to condemn a
// provider, since one fault is ordinary, but far too much to call it well. The
// honest state there is unknown, and unknown is what it gets.
//
// The rule this replaces was "no streak means nothing to say", which sounds
// the same and is not: it treated silence and success as one fact. Silence
// really is no evidence. A run of successful calls is the strongest evidence
// there is, and it was being thrown away -- so a machine where everything
// worked reported unknown forever, and the light never went green.
func (b Baseline) Health(now time.Time) (contract.Health, bool) {
	if h, hurt := b.Fault.Health(now); hurt {
		return h, true
	}
	if b.Fault.Streak > 0 || b.Success.IsZero() || now.Sub(b.Success) > SuccessWindow {
		return contract.Health{}, false
	}
	return contract.Health{
		State:      contract.HealthAlive,
		Reason:     fmt.Sprintf("last call here worked, %s ago", since(now, b.Success)),
		ObservedAt: b.Success,
		// Score is deliberately left at zero rather than set to 1.
		//
		// Score breaks ties between two providers in the same state, and the
		// tie-break that matters between two working providers is what they
		// cost -- a real number, measured on both. Awarding a full score for
		// having worked would put every promoted provider above every other
		// on a figure invented here, and cost would never be reached.
	}, true
}

// since renders how long ago something happened, at a grain a person reads.
func since(now, then time.Time) time.Duration {
	d := now.Sub(then)
	if d < time.Second {
		return d.Truncate(time.Millisecond)
	}
	return d.Truncate(time.Second)
}

// costs reads the current figures for one capability on one repository.
//
// # Only successes are a price
//
// The cost columns count `ok` rows and divide by `ok` rows. Attempts and
// failures are still totalled, because how often something was tried and how
// often it broke is worth knowing -- just never as a number that makes a
// broken provider look cheap.
//
// # Only the version running now
//
// An upgraded tool starts a fresh baseline rather than averaging into the old
// binary's numbers: otherwise the slow numbers of the version before drag the
// average down and the funnel takes weeks to notice the improvement. "Current"
// is the version of the most recent attempt, because that is the one the
// machine actually has installed -- version strings are vendor prose and
// cannot be ordered, but timestamps can.
//
// The consequence is deliberate: an upgrade puts that implementation back into
// break-in, and it earns its numbers again.
const costs = `WITH parts AS (
	SELECT implementation, tool_version,
	       count(*) AS attempts,
	       count(*) FILTER (WHERE NOT ok) AS failures,
	       count(*) FILTER (WHERE ok) AS wins,
	       coalesce(sum(duration_us) FILTER (WHERE ok), 0) AS dsum,
	       coalesce(sum(tokens) FILTER (WHERE ok), 0) AS tsum,
	       max(peak_rss_bytes) FILTER (WHERE ok) AS rmax,
	       max(happened_at) AS last_seen
	FROM measurement
	WHERE NOT folded AND capability = ? AND repository = ?
	GROUP BY 1, 2
	UNION ALL
	SELECT implementation, tool_version,
	       sum(attempts), sum(failures), sum(ok_attempts),
	       sum(ok_duration_us_sum), sum(ok_tokens_sum),
	       max(peak_rss_max), max(bucket)
	FROM rollup
	WHERE capability = ? AND repository = ?
	GROUP BY 1, 2
), merged AS (
	SELECT implementation, tool_version, sum(attempts) AS attempts,
	       sum(failures) AS failures, sum(wins) AS wins, sum(dsum) AS dsum,
	       sum(tsum) AS tsum, max(rmax) AS rmax, max(last_seen) AS last_seen
	FROM parts
	GROUP BY 1, 2
)
SELECT implementation, tool_version, attempts, failures, wins, dsum, tsum, rmax
FROM merged
QUALIFY row_number() OVER (
	PARTITION BY implementation ORDER BY last_seen DESC, tool_version DESC
) = 1`

// recency finds the newest end of each implementation's record on one
// repository: the run of failures since the last success, and when that last
// success was.
//
// Both witnesses come from one pass because they are the same question asked
// from two sides, and because a second query could see a different newest row
// -- the store is being written to while this runs.
//
// The join is a LEFT join for the case that matters most: an implementation
// whose newest attempt succeeded has no run at all, and it is exactly the one
// the promotion rule is looking for. An inner join would return no row for it
// and the record would stay mute about everything that works.
//
// Unlike costs this reads folded rows too. Folding is a summary of what things
// cost, and it throws the ordering away; the attempt rows outlive it for the
// whole fine window precisely so questions like this one can still be asked.
// Nothing here crosses tool versions either way: a streak is about the binary
// installed right now, and an upgrade should be given the chance to be better.
// The partition is (capability, repository, implementation) whichever way it
// is asked, because health is a per-repository fact: the same provider can be
// warm on one repository and dead on another, and merging them would report a
// state that is true nowhere.
const recencyTemplate = `WITH recent AS (
	SELECT capability, repository, implementation, ok, failure_kind, failure, happened_at,
	       row_number() OVER (
	           PARTITION BY capability, repository, implementation ORDER BY happened_at DESC
	       ) AS rn
	FROM measurement
	%s
), ends AS (
	SELECT capability, repository, implementation,
	       coalesce(min(rn) FILTER (WHERE ok), 999999999) AS first_ok,
	       max(happened_at) FILTER (WHERE ok) AS last_ok
	FROM recent
	GROUP BY 1, 2, 3
), run AS (
	SELECT r.capability, r.repository, r.implementation,
	       r.failure_kind, r.failure, r.happened_at, r.rn
	FROM recent r JOIN ends e
	  ON e.capability = r.capability AND e.repository = r.repository
	 AND e.implementation = r.implementation
	WHERE r.rn < e.first_ok
)
SELECT e.capability, e.repository, e.implementation,
       count(run.rn) AS streak,
       count(DISTINCT run.failure_kind) AS bins,
       arg_min(run.failure_kind, run.rn) AS kind,
       arg_min(run.failure, run.rn) AS reason,
       max(run.happened_at) AS latest,
       any_value(e.last_ok) AS last_ok
FROM ends e LEFT JOIN run
  ON run.capability = e.capability AND run.repository = e.repository
 AND run.implementation = e.implementation
GROUP BY 1, 2, 3`

// The two scopes the record is ever asked for. Built once from one template so
// a fix to the shape cannot land in only half of them, which is exactly how
// the funnel and the status screen would drift apart.
var (
	recencyHere = fmt.Sprintf(recencyTemplate, "WHERE capability = ? AND repository = ?")
	recencyAll  = fmt.Sprintf(recencyTemplate, "")
)

// Baselines reports what each implementation of a capability has done on one
// repository: what its successful calls cost, and whether its recent ones
// failed.
//
// Cost is asked per repository because it is not a property of the tool: the
// same provider is cheap with a warm index and expensive without one. An
// implementation absent from the answer has never been tried here, which is
// not the same as being free.
func (s *Store) Baselines(ctx context.Context, capability, repository string) (map[string]Baseline, error) {
	// Flushing first matters more here than anywhere else: a funnel reading a
	// store with its own batch still in memory would rank on a baseline stale
	// by exactly the calls that just ran.
	if err := s.Flush(ctx); err != nil {
		return nil, err
	}
	db, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	out := make(map[string]Baseline)
	if err := s.readCosts(ctx, db, capability, repository, out); err != nil {
		return nil, err
	}
	if err := s.readRecency(ctx, db, capability, repository, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) readCosts(ctx context.Context, db *sql.DB, capability, repository string,
	out map[string]Baseline) error {
	rows, err := db.QueryContext(ctx, costs, capability, repository, capability, repository)
	if err != nil {
		return fmt.Errorf("metrics: costs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id string
		var b Baseline
		var attempts, failures, wins, dsum, tsum int64
		var rmax *int64
		if err := rows.Scan(&id, &b.ToolVersion, &attempts, &failures, &wins,
			&dsum, &tsum, &rmax); err != nil {
			return fmt.Errorf("metrics: costs: %w", err)
		}
		b.Attempts = int(attempts)
		b.Failures = int(failures)
		b.Successes = int(wins)
		// A row with attempts but no successes is the whole point of the
		// split: it keeps its record and holds no price at all, so the funnel
		// falls back to the declared estimate instead of believing a number
		// made of refusals.
		if wins > 0 {
			b.Spent = contract.Sample{
				Duration: time.Duration(dsum/wins) * time.Microsecond,
				Tokens:   int(tsum / wins),
			}
			if rmax != nil {
				b.Spent.PeakRSS = *rmax
			}
		}
		out[id] = b
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("metrics: costs: %w", err)
	}
	return nil
}

// recencyRow is one implementation's newest end on one repository, exactly as
// the query returns it. Both readers scan this shape; they differ only in what
// they do with the capability and repository that came with it.
type recencyRow struct {
	Capability string
	Repository string
	ID         string
	Baseline   Baseline
}

// scanRecency reads the rows of either scope.
//
// Every column but the three keys is nullable, because a row comes back for an
// implementation whose last call WORKED and that one has no streak to
// describe. Scanning those into plain strings would fail on the first healthy
// provider in the base -- which, before this query existed, was a case that
// never arrived.
func scanRecency(rows *sql.Rows) ([]recencyRow, error) {
	var out []recencyRow
	for rows.Next() {
		var r recencyRow
		var streak, bins int64
		var kind, reason *string
		var latest, lastOK *time.Time
		if err := rows.Scan(&r.Capability, &r.Repository, &r.ID,
			&streak, &bins, &kind, &reason, &latest, &lastOK); err != nil {
			return nil, fmt.Errorf("metrics: recency: %w", err)
		}
		r.Baseline.Fault = Fault{Streak: int(streak)}
		if reason != nil {
			r.Baseline.Fault.Reason = *reason
		}
		if latest != nil {
			r.Baseline.Fault.Latest = *latest
		}
		// One bin repeated is an outage that can be named. Several bins is a
		// provider breaking differently every time, and naming one of them
		// would be picking a favorite out of a bag.
		if bins == 1 && kind != nil {
			r.Baseline.Fault.Kind = *kind
		}
		if lastOK != nil {
			r.Baseline.Success = *lastOK
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metrics: recency: %w", err)
	}
	return out, nil
}

// readRecency fills in each implementation's newest end for one capability on
// one repository.
func (s *Store) readRecency(ctx context.Context, db *sql.DB, capability, repository string,
	out map[string]Baseline) error {
	rows, err := db.QueryContext(ctx, recencyHere, capability, repository)
	if err != nil {
		return fmt.Errorf("metrics: recency: %w", err)
	}
	defer func() { _ = rows.Close() }()

	read, err := scanRecency(rows)
	if err != nil {
		return err
	}
	for _, r := range read {
		b := out[r.ID]
		b.Fault = r.Baseline.Fault
		b.Success = r.Baseline.Success
		out[r.ID] = b
	}
	return nil
}

// Verdict is what the record concluded about one implementation, and where.
//
// The location travels with it because a health state without a repository is
// unreadable on a workspace of forty: "down" is a different instruction
// depending on whether it is down everywhere or down on the one repository
// nobody has indexed.
type Verdict struct {
	Health     contract.Health
	Capability string
	Repository string
}

// Health reads the whole record and reports, per implementation, the worst
// state it reached anywhere.
//
// Worst rather than newest, because this feeds a light and a light is a
// summary of everything under it. A provider that is fine on one repository
// and dead on another is not fine.
//
// Implementations the record says nothing about are absent from the answer.
// That is not the same as healthy and not the same as broken: it is the
// caller's job to leave those exactly as the catalog declared them.
func (s *Store) Health(ctx context.Context, now time.Time) (map[string]Verdict, error) {
	if err := s.Flush(ctx); err != nil {
		return nil, err
	}
	db, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx, recencyAll)
	if err != nil {
		return nil, fmt.Errorf("metrics: recency: %w", err)
	}
	defer func() { _ = rows.Close() }()
	read, err := scanRecency(rows)
	if err != nil {
		return nil, err
	}

	out := make(map[string]Verdict)
	for _, r := range read {
		health, said := r.Baseline.Health(now)
		if !said {
			continue
		}
		if worse, ok := out[r.ID]; ok &&
			worse.Health.State.Rank() >= health.State.Rank() {
			continue
		}
		out[r.ID] = Verdict{Health: health, Capability: r.Capability, Repository: r.Repository}
	}
	return out, nil
}

// Reconcile settles what the record concluded against what the catalog already
// says, letting each move only in the direction it is entitled to.
//
// It is exported and shared because there are two callers -- the funnel and
// the status screen -- and a screen that reported health by a different rule
// than the one the funnel selects on would be worse than no screen at all.
func Reconcile(declared, recorded contract.Health) contract.Health {
	switch {
	case recorded.State != contract.HealthAlive:
		// Downwards the record may overrule anything: a streak of real
		// failures on a real repository beats any opinion about the provider
		// in general, including a cheerful one in the settings file.
		if recorded.State.Rank() > declared.State.Rank() {
			return recorded
		}
	case declared.State == contract.HealthUnknown:
		// Upwards it may only lift unknown, which carries no claim -- nobody
		// looked. A down or degraded was put there by something that looked
		// more recently than a file can, and promoting over it would hide the
		// outage the operator is standing in front of.
		//
		// The declared score rides along rather than being replaced: promotion
		// is a statement about the state, and inventing a figure for it would
		// outrank a real one somebody wrote.
		recorded.Score = declared.Score
		return recorded
	}
	return declared
}

// measured lists every implementation the base can put a real price on: one
// that has at least one successful call still on record, in either table.
//
// Both tables, because the two age differently. Attempt rows are dropped after
// a week while their hour survives for months, so an implementation used
// steadily last month and not since is absent from one and present in the
// other -- and it does have a measured cost, which is the question being
// asked.
const measured = `SELECT DISTINCT implementation FROM measurement WHERE ok
UNION
SELECT DISTINCT implementation FROM rollup WHERE ok_attempts > 0`

// Measured reports which implementations rank on real numbers rather than on
// the estimate somebody typed into the settings file.
//
// It exists so a screen can say which of the two it is showing. An estimate
// read as a measurement is the specific misunderstanding this whole store was
// built to prevent, and a caption that claims one while displaying the other
// causes it rather than preventing it.
func (s *Store) Measured(ctx context.Context) ([]string, error) {
	if err := s.Flush(ctx); err != nil {
		return nil, err
	}
	db, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx, measured)
	if err != nil {
		return nil, fmt.Errorf("metrics: measured: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("metrics: measured: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metrics: measured: %w", err)
	}
	return out, nil
}

// Apply fills the measured half of each candidate's cost from the base, and
// its health from the base's newest calls.
//
// The declared estimate is left exactly as it was written. That is the hybrid:
// an implementation the base has never seen here keeps ranking on its guess,
// and one it has seen enough of ranks on what actually happened.
//
// Health is filled the same way and for the same reason. A probe answers "is
// this provider up" in the abstract; the record answers "did it work here,
// just now", which is the question the funnel is actually asking. The
// orchestrator already marks a provider down the moment one call reports
// itself unavailable, but that mark lives in a catalog held in memory, and
// Atenea is a CLI at least as often as it is a service: a fresh process starts
// with a clean catalog and forgets every call before it. This is the half that
// survives, because it is on disk.
//
// # Which witness wins
//
// Downwards the record may overrule anything, because a streak of real
// failures on this repository beats any opinion about the provider in general,
// including a cheerful one written in the settings file.
//
// Upwards it may only lift UNKNOWN to alive. It never touches a down or
// degraded that something else put there. The reason is that the two marks are
// not the same age: a probe marks a provider down inside this process, seconds
// ago, while the record is a file that may have been written before the outage
// started. Letting yesterday's successes overrule a live probe would hide
// exactly the failure the operator is standing in front of. Unknown carries no
// such claim -- it means nobody looked -- so there is nothing to overrule.
//
// Candidates are filled in place. They are the caller's own copies out of the
// catalog -- which stays declarative, and never learns a number from a run.
//
// The returned lines are for the trace. An implementation with a long record
// and no price is the one thing a reader cannot work out from the numbers
// alone -- the funnel shows it ranking on an estimate while the base plainly
// holds attempts for it -- so it is said out loud rather than left to look
// like a bug in the ranking.
func Apply(base map[string]Baseline, candidates []contract.Implementation, now time.Time) []string {
	var notices []string
	for i := range candidates {
		b, ok := base[candidates[i].ID]
		if !ok {
			continue
		}
		candidates[i].Cost.Measured = b.Spent
		candidates[i].Cost.Samples = b.Successes
		candidates[i].Cost.ToolVersion = b.ToolVersion
		if health, said := b.Health(now); said {
			candidates[i].Health = Reconcile(candidates[i].Health, health)
		}
		if b.Attempts > 0 && b.Successes == 0 {
			notices = append(notices, fmt.Sprintf(
				"%s: %s here, none of them successful, so it has no measured cost and ranks on its declared estimate",
				candidates[i].ID, plural(b.Attempts, "attempt")))
		}
	}
	return notices
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
