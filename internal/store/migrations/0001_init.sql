-- Raw observations. Retained for days, not months: this table exists to be
-- rolled up, and the rollup is what the page actually reads.
CREATE TABLE IF NOT EXISTS check_results (
    id          BIGSERIAL PRIMARY KEY,
    check_key   TEXT        NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    state       TEXT        NOT NULL,
    latency_ms  INTEGER,
    http_status INTEGER,
    reason_code TEXT
);

CREATE INDEX IF NOT EXISTS idx_check_results_key_time
    ON check_results (check_key, observed_at DESC);

-- One row per check per local day, and the only table the 90-day strip reads.
CREATE TABLE IF NOT EXISTS check_daily (
    check_key      TEXT    NOT NULL,
    day            DATE    NOT NULL,
    up_count       INTEGER NOT NULL DEFAULT 0,
    degraded_count INTEGER NOT NULL DEFAULT 0,
    down_count     INTEGER NOT NULL DEFAULT 0,
    unknown_count  INTEGER NOT NULL DEFAULT 0,
    retired_count  INTEGER NOT NULL DEFAULT 0,
    uptime_pct     NUMERIC(5,2),
    PRIMARY KEY (check_key, day)
);

-- Current state, denormalised so rendering the page or answering the API is a
-- handful of row lookups rather than a scan over history.
CREATE TABLE IF NOT EXISTS check_current (
    check_key       TEXT PRIMARY KEY,
    state           TEXT        NOT NULL,
    since           TIMESTAMPTZ NOT NULL,
    last_checked_at TIMESTAMPTZ NOT NULL,
    latency_ms      INTEGER,
    reason_code     TEXT
);

-- Circuit breaker state for the credentialed SSO probes.
--
-- This is persisted rather than held in memory on purpose: a container restart
-- must not reset an open breaker and resume attempting logins, which is
-- exactly how a crash-looping deploy would walk the account into a lockout.
CREATE TABLE IF NOT EXISTS breaker_state (
    check_key         TEXT PRIMARY KEY,
    consecutive_fails INTEGER     NOT NULL DEFAULT 0,
    open_until        TIMESTAMPTZ,
    backoff_seconds   INTEGER     NOT NULL DEFAULT 0
);
