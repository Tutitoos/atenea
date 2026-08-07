-- out_of_scope survives the fold.
--
-- Migration 0006 gave the number a column on the attempt table so it would
-- outlive the sentence that reported it. It stopped there: the rollup had no
-- such column, so an attempt that wandered kept its count for exactly as long
-- as it stayed unfolded, and the first compaction pass silently rounded the
-- whole history of it to nothing.
--
-- That was survivable while nothing read the column. A reader turns it into a
-- number that shrinks on a schedule for reasons its reader cannot see, which
-- is worse than no reader at all.
--
-- It is a plain additive sum, exactly like tokens_sum: two buckets merging add
-- their strays. Nothing here scores it -- health and cost never look at this
-- column, deliberately, because a provider that reports its own overreach
-- honestly must not rank below one that hides it.
ALTER TABLE rollup ADD COLUMN out_of_scope BIGINT;

-- Buckets folded before this column existed carry no answer, and zero is the
-- only honest one available: the attempts behind them are gone, so there is
-- nothing left to count. It reads as "none recorded", which is what it is.
UPDATE rollup SET out_of_scope = 0;
