CREATE TABLE IF NOT EXISTS linka_metric.product_outcomes_v2
(
    product LowCardinality(String),
    product_key Nullable(FixedString(64)),
    subject_key FixedString(64),
    person_key Nullable(FixedString(64)),
    org_key Nullable(FixedString(64)),
    batch_id UUID,
    record_id UUID,
    occurred_at DateTime64(3, 'UTC'),
    kind LowCardinality(String),
    app_session_id UUID,
    app_version String,
    app_build String,
    platform LowCardinality(String),
    os_version String,
    locale LowCardinality(String),
    result LowCardinality(Nullable(String)),
    source LowCardinality(Nullable(String)),
    mode LowCardinality(Nullable(String)),
    count_bucket LowCardinality(Nullable(String)),
    duration_bucket LowCardinality(Nullable(String)),
    failure_code LowCardinality(Nullable(String)),
    ingested_at DateTime64(3, 'UTC'),
    expires_at Nullable(DateTime64(3, 'UTC'))
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMM(ingested_at)
ORDER BY (product, subject_key, record_id);

CREATE VIEW IF NOT EXISTS linka_metric.datalens_outcomes_v1
SQL SECURITY DEFINER
AS
SELECT
    product,
    if(product_key IS NULL, NULL, cityHash64(product, assumeNotNull(product_key))) AS product_key,
    cityHash64(product, subject_key) AS subject_key,
    if(person_key IS NULL, NULL, cityHash64(product, assumeNotNull(person_key))) AS person_key,
    if(org_key IS NULL, NULL, cityHash64(product, assumeNotNull(org_key))) AS org_key,
    cityHash64(product, record_id) AS record_key,
    occurred_at,
    toDate(occurred_at, 'UTC') AS occurred_date,
    kind,
    cityHash64(product, app_session_id) AS app_session_key,
    app_version,
    app_build,
    platform,
    os_version,
    locale,
    result,
    source,
    mode,
    count_bucket,
    duration_bucket,
    failure_code
FROM linka_metric.product_outcomes_v2 AS events FINAL
WHERE (toString(events.product), toString(ifNull(events.product_key, '')), toString(events.subject_key)) NOT IN
(
    SELECT toString(product), toString(ifNull(product_key, '')), toString(subject_key)
    FROM linka_metric.privacy_suppressions_v2 FINAL
    WHERE active = true
)
AND (toString(events.product), toString(ifNull(events.product_key, '')), toString(ifNull(events.person_key, ''))) NOT IN
(
    SELECT toString(product), toString(ifNull(product_key, '')), toString(assumeNotNull(person_key))
    FROM linka_metric.privacy_suppressions_v2 FINAL
    WHERE active = true AND person_key IS NOT NULL
)
AND (toString(events.product), toString(ifNull(events.product_key, '')), toString(ifNull(events.org_key, ''))) NOT IN
(
    SELECT toString(product), toString(ifNull(product_key, '')), toString(assumeNotNull(org_key))
    FROM linka_metric.privacy_suppressions_v2 FINAL
    WHERE active = true AND org_key IS NOT NULL
)
SETTINGS join_use_nulls = 1;

CREATE VIEW IF NOT EXISTS linka_metric.datalens_outcomes_daily_v1
SQL SECURITY DEFINER
AS
SELECT
    occurred_date,
    product,
    kind,
    result,
    source,
    mode,
    platform,
    app_version,
    count() AS event_count,
    uniqExact(subject_key) AS active_subject_count,
    uniqExact(app_session_key) AS active_session_count
FROM linka_metric.datalens_outcomes_v1
GROUP BY occurred_date, product, kind, result, source, mode, platform, app_version;

CREATE VIEW IF NOT EXISTS linka_metric.datalens_tts_operations_daily_v1
SQL SECURITY DEFINER
AS
SELECT
    occurred_date,
    source AS provider,
    result,
    duration_bucket,
    count_bucket,
    failure_code,
    count() AS event_count
FROM linka_metric.datalens_outcomes_v1
WHERE product = 'linka-tts'
GROUP BY occurred_date, provider, result, duration_bucket, count_bucket, failure_code;

CREATE VIEW IF NOT EXISTS linka_metric.datalens_telemetry_quality_daily_v1
SQL SECURITY DEFINER
AS
SELECT
    toDate(ingested_at, 'UTC') AS ingested_date,
    product,
    stream,
    count() AS batch_count,
    sum(record_count) AS accepted_record_count,
    countIf(status = 'completed') AS completed_batch_count,
    countIf(status = 'reserved') AS reserved_batch_count
FROM linka_metric.ingest_batches_v2 FINAL
GROUP BY ingested_date, product, stream;
