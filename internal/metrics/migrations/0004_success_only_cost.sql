-- A failure is not a price. It is the absence of one.
--
-- Until now the cost base averaged every attempt together, so an
-- implementation that refused instantly -- no login, no index, no server --
-- recorded a stream of very fast, very cheap calls and became the cheapest
-- thing on the board. The funnel then handed it everything, and every
-- commission failed. Failing cheaply was rewarded, which is exactly backwards.
--
-- The attempt table can already tell the two apart: it carries `ok`. The
-- rollups could not, because they folded successes and failures into one sum,
-- so these three columns carry the successful half separately. `attempts` and
-- `failures` stay as they are: how often something was tried and how often it
-- broke is the health question, and it is still worth answering.
--
-- The columns arrive without a NOT NULL: DuckDB refuses a constraint on ADD
-- COLUMN. The UPDATE below writes every existing row and every insert names
-- all three, so no NULL is reachable either way.
ALTER TABLE rollup ADD COLUMN ok_attempts        BIGINT;
ALTER TABLE rollup ADD COLUMN ok_duration_us_sum BIGINT;
ALTER TABLE rollup ADD COLUMN ok_tokens_sum      BIGINT;

-- A bucket with no failures in it was already all-success, so its old sums are
-- exactly the new ones and nothing is lost.
--
-- A bucket that mixed the two cannot be split after the fact, and the tempting
-- repair -- keep the count, zero the sums -- would invent an average of zero
-- and re-create the bug this migration exists to remove. So a mixed legacy
-- bucket contributes nothing to cost and says so by holding zero attempts. It
-- still counts in `attempts` and `failures`, so no history is erased, only its
-- claim to be a price.
UPDATE rollup SET
    ok_attempts        = CASE WHEN failures = 0 THEN attempts        ELSE 0 END,
    ok_duration_us_sum = CASE WHEN failures = 0 THEN duration_us_sum ELSE 0 END,
    ok_tokens_sum      = CASE WHEN failures = 0 THEN tokens_sum      ELSE 0 END;
