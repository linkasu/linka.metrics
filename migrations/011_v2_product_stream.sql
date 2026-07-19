CREATE TABLE IF NOT EXISTS linka_metric.product_events_v2
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
    ingested_at DateTime64(3, 'UTC'),
    expires_at Nullable(DateTime64(3, 'UTC'))
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMM(ingested_at)
ORDER BY (product, subject_key, record_id);

CREATE VIEW IF NOT EXISTS linka_metric.datalens_product_v2
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
    locale
FROM linka_metric.product_events_v2 AS events FINAL
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
