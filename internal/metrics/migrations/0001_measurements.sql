-- One row per attempt: the fine detail, kept for a week.
--
-- Failed attempts are recorded exactly like successful ones. A tool that is
-- fast when it works and fails half the time is not fast, and a baseline built
-- only from the successes would say it is.
CREATE TABLE measurement (
    happened_at     TIMESTAMP NOT NULL,
    run_id          VARCHAR   NOT NULL,
    step_id         VARCHAR   NOT NULL,
    capability      VARCHAR   NOT NULL,
    implementation  VARCHAR   NOT NULL,
    provider        VARCHAR   NOT NULL,
    repository      VARCHAR   NOT NULL,
    -- What the far side answered when asked its version, not what the settings
    -- file claims. An upgrade starts a new baseline instead of averaging the
    -- old numbers in.
    tool_version    VARCHAR   NOT NULL,
    -- Microseconds, not milliseconds. The band where the funnel has the most
    -- to decide is the cheap one -- a warm text search over a small repository
    -- runs in tens of microseconds -- and in milliseconds every provider in
    -- that band records a zero and becomes indistinguishable from the others.
    duration_us     BIGINT    NOT NULL,
    tokens          BIGINT    NOT NULL,
    -- NULL means nobody could weigh it -- an adapter talking to a server has no
    -- process of its own -- as opposed to a process that used no memory.
    peak_rss_bytes  BIGINT,
    ok              BOOLEAN   NOT NULL,
    -- Both empty when ok. The bin is the sortable half and the reason is the
    -- half a human reads.
    failure_kind    VARCHAR   NOT NULL,
    failure         VARCHAR   NOT NULL,
    -- Set once the row has been counted into its hourly bucket. Folding is
    -- separate from deleting so that re-running compaction cannot count the
    -- same attempt twice.
    folded          BOOLEAN   NOT NULL DEFAULT FALSE
);
