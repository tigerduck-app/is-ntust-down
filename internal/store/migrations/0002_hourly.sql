-- One row per check per local hour, for the recent-detail strip.
--
-- Separate from check_daily rather than derived from it: a day bucket cannot
-- answer "was it down this morning", and answering that from raw results
-- instead would tie the strip to RAW_RESULT_RETENTION_DAYS and make it a scan
-- on every page load.
--
-- `hour` is a naive local timestamp (Asia/Taipei by default), matching how
-- check_daily stores a local date, so a bar labelled 05 時 is the hour a
-- reader in Taiwan actually lived through.
CREATE TABLE IF NOT EXISTS check_hourly (
    check_key      TEXT      NOT NULL,
    hour           TIMESTAMP NOT NULL,
    up_count       INTEGER   NOT NULL DEFAULT 0,
    degraded_count INTEGER   NOT NULL DEFAULT 0,
    down_count     INTEGER   NOT NULL DEFAULT 0,
    unknown_count  INTEGER   NOT NULL DEFAULT 0,
    retired_count  INTEGER   NOT NULL DEFAULT 0,
    uptime_pct     NUMERIC(5,2),
    PRIMARY KEY (check_key, hour)
);

CREATE INDEX IF NOT EXISTS idx_check_hourly_hour ON check_hourly (hour DESC);
