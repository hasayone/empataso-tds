-- One conversion per click and status. The older postbacks table is ordered by
-- cid alone, so a later status replaced the earlier one.
CREATE TABLE IF NOT EXISTS emtds.conversions
(
    ts          DateTime DEFAULT now(),
    domain      String,
    page        String,
    device      LowCardinality(String),
    operator    LowCardinality(String),
    country     LowCardinality(String),
    city        String,
    profit      Float64,
    group_name  LowCardinality(String),
    stream_name LowCardinality(String),
    cid         String,
    status      LowCardinality(String)
)
ENGINE = ReplacingMergeTree(ts)
PARTITION BY toYYYYMM(ts)
ORDER BY (cid, status)
TTL ts + INTERVAL 365 DAY;
