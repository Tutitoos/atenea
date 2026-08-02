-- The four retention tiers, all in one table keyed by grain.
--
-- Each tier waits longer than the last before it is folded into the next, so
-- there is always fine detail about the recent past and only shapes about the
-- distant one: hour for a week, day for a month, week for six months, month
-- forever.
--
-- Only mergeable figures live here. Sums and counts survive being folded into a
-- coarser bucket; a percentile does not, and inventing one out of merged
-- buckets would produce a number nobody measured. Exact percentiles come from
-- the attempt table while it still holds the week.
CREATE TABLE rollup (
    grain           VARCHAR   NOT NULL,  -- hour | day | week | month
    bucket          TIMESTAMP NOT NULL,
    capability      VARCHAR   NOT NULL,
    implementation  VARCHAR   NOT NULL,
    provider        VARCHAR   NOT NULL,
    repository      VARCHAR   NOT NULL,
    tool_version    VARCHAR   NOT NULL,
    attempts        BIGINT    NOT NULL,
    failures        BIGINT    NOT NULL,
    duration_us_sum BIGINT    NOT NULL,
    duration_us_max BIGINT    NOT NULL,
    tokens_sum      BIGINT    NOT NULL,
    -- Max rather than a sum: the question memory answers is "how big did this
    -- get", which does not add up across calls.
    peak_rss_max    BIGINT,
    -- How many of those attempts could actually be weighed, so an average is
    -- taken over the measured ones instead of counting the gaps as zero.
    rss_samples     BIGINT    NOT NULL,
    PRIMARY KEY (grain, bucket, capability, implementation, repository, tool_version)
);
