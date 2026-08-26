-- What a call was about, beyond the repository it named.
--
-- Health and cost were recorded per repository, which is the only dimension a
-- code capability has. A capability that ignores the repository entirely --
-- web.fetch, which reaches the open web and never looks at a checkout -- had
-- nowhere to hang them, so every site landed in one bucket.
--
-- The cost of that was measured before this column existed: one page behind
-- Cloudflare marked the cheapest implementation of web.fetch unhealthy, and
-- the next fetch of an unrelated site skipped that implementation too, with a
-- drop reason still quoting the first site's url. One protected page paid for
-- a browser on every page after it, for as long as health_stale_after stood.
--
-- Added rather than folded into `repository`. Every row written before this
-- exists is about no subject in particular, which is what the empty string
-- says -- and a capability that declares no subject keeps writing it that way,
-- so nothing that ranked yesterday ranks differently today.
--
-- Two statements and no NOT NULL, the same shape as 0008: DuckDB answers
-- "Adding columns with constraints not yet supported" to a constrained ADD
-- COLUMN, so the column is added plain and the existing rows are filled. Every
-- insert after this supplies a value, so the only NULLs possible are the ones
-- the UPDATE below has already taken.
ALTER TABLE measurement ADD COLUMN subject VARCHAR;

UPDATE measurement SET subject = '' WHERE subject IS NULL;
