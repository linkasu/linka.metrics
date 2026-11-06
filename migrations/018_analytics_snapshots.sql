-- Aggregate-only read models and a serial, single-host build journal.
-- Existing event/identity tables and historical expiry values are unchanged.
CREATE TABLE IF NOT EXISTS linka_metric.analytics_requests_v1
(
    run_id UUID,
    query_hash FixedString(64),
    state LowCardinality(String),
    query_json String,
    reason String,
    created_at DateTime64(3, 'UTC'),
    updated_at DateTime64(3, 'UTC'),
    version UInt64
)
ENGINE = ReplacingMergeTree(version)
ORDER BY run_id;

CREATE TABLE IF NOT EXISTS linka_metric.analytics_request_aliases_v1
(
    request_id UUID,
    query_hash FixedString(64),
    run_id UUID,
    created_at DateTime64(3, 'UTC')
)
ENGINE = ReplacingMergeTree
ORDER BY request_id;

CREATE TABLE IF NOT EXISTS linka_metric.analytics_snapshots_v1
(
    query_hash FixedString(64),
    generation_id UUID,
    report_json String,
    privacy_revision String,
    published_at DateTime64(3, 'UTC'),
    valid_until DateTime64(3, 'UTC'),
    revision UInt64
)
ENGINE = ReplacingMergeTree(revision)
ORDER BY query_hash;
