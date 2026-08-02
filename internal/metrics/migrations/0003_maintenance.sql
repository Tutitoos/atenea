-- When each maintenance pass last happened.
--
-- Atenea is a CLI as often as it is a service, and a CLI has no rhythm: it
-- starts, does one thing and dies. Without a mark on disk the roll-up would
-- either never run for anyone who does not keep a core up, or run on every
-- single command. The mark is read and written inside the same transaction as
-- the work it describes, so two Ateneas starting at once cannot both decide
-- they are the one to do it.
CREATE TABLE maintenance (
    job      VARCHAR   NOT NULL PRIMARY KEY,
    last_run TIMESTAMP NOT NULL
);
