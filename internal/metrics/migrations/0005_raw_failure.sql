-- Raw is the untranslated provider text behind a failure: what the far side
-- actually said, kept beside the bin and the sentence Atenea built from it.
--
-- Before this column existed a failure that did not match one of an
-- adapter's known patterns fell into its generic bin with nothing beside it
-- but that adapter's own catch-all sentence -- "serena did not answer" and
-- nothing else, forever, unless somebody happened to be at a terminal to
-- reproduce it by hand. The bin and the sentence stay; this is the evidence
-- that lets a human stop guessing at what produced them.
--
-- Arrives without NOT NULL: DuckDB refuses a constraint on ADD COLUMN. The
-- UPDATE below backfills every existing row so no NULL is reachable, and
-- every insert from here on names the column explicitly.
ALTER TABLE measurement ADD COLUMN raw VARCHAR;
UPDATE measurement SET raw = '';
