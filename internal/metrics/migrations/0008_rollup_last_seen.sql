-- A folded bucket has to remember when it was last written to.
--
-- "Which version is running now" is decided by the newest attempt, because
-- version strings are vendor prose and cannot be ordered while timestamps can.
-- The cost query read that instant out of `bucket` for folded rows -- and
-- bucket is the hour the attempt landed in, not the attempt. Two versions of
-- the same tool used inside one hour therefore folded into two rows carrying
-- the identical last_seen, the tie fell through to the `tool_version DESC`
-- tiebreak, and an upgrade from 9.9.0 to 10.0.0 answered with the old binary's
-- numbers for ever after: exactly the string comparison the design ruled out.
--
-- last_seen is a maximum, so it survives every fold on the ladder the same way
-- duration_us_max does: two buckets merging keep the later instant.
ALTER TABLE rollup ADD COLUMN last_seen TIMESTAMP;

-- Buckets folded before this column existed have nothing better available:
-- the attempts behind them are gone and the hour is the only instant left. It
-- is never later than the truth, so a legacy row can lose a tie against a
-- freshly folded one and never win one it should have lost.
UPDATE rollup SET last_seen = bucket;
