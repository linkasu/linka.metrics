-- Approved retention policy: raw V2 event tables, ingest_batches_v2 and
-- record_registry_v2 expire 180 days after ingested_at; privacy_suppressions_v2
-- and privacy_deletion_progress_v2 expire 3 years (1095 days) after their
-- last update. Writers bake the duration into expires_at; this migration only
-- activates the TTL clause.
-- ClickHouse never deletes rows whose TTL expression is NULL, so pre-existing
-- expires_at IS NULL rows stay until the separate operational backfill fills
-- them. DataLens views below additionally hide already-expired rows that TTL
-- merges have not physically removed yet.

ALTER TABLE linka_metric.record_registry_v2
    ADD COLUMN IF NOT EXISTS expires_at Nullable(DateTime64(3, 'UTC'));

ALTER TABLE linka_metric.privacy_deletion_progress_v2
    ADD COLUMN IF NOT EXISTS expires_at Nullable(DateTime64(3, 'UTC'));

ALTER TABLE linka_metric.ingest_batches_v2 MODIFY TTL expires_at;

ALTER TABLE linka_metric.record_registry_v2 MODIFY TTL expires_at;

ALTER TABLE linka_metric.privacy_suppressions_v2 MODIFY TTL expires_at;

ALTER TABLE linka_metric.privacy_deletion_progress_v2 MODIFY TTL expires_at;

ALTER TABLE linka_metric.common_events_v2 MODIFY TTL expires_at;

ALTER TABLE linka_metric.technical_events_v2 MODIFY TTL expires_at;

ALTER TABLE linka_metric.plays_events_v2 MODIFY TTL expires_at;

ALTER TABLE linka_metric.product_events_v2 MODIFY TTL expires_at;

CREATE OR REPLACE VIEW linka_metric.datalens_common_v3
SQL SECURITY DEFINER
AS
SELECT
    2 AS schema_version,
    'common' AS stream,
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
    page,
    mode
FROM linka_metric.common_events_v2 AS events FINAL
WHERE (events.expires_at IS NULL OR events.expires_at > now())
AND (toString(events.product), toString(ifNull(events.product_key, '')), toString(events.subject_key)) NOT IN
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

CREATE OR REPLACE VIEW linka_metric.datalens_technical_v3
SQL SECURITY DEFINER
AS
SELECT
    2 AS schema_version,
    'technical' AS stream,
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
    component,
    state,
    error_fingerprint,
    dropped_count,
    drop_reason
FROM linka_metric.technical_events_v2 AS events FINAL
WHERE (events.expires_at IS NULL OR events.expires_at > now())
AND (toString(events.product), toString(ifNull(events.product_key, '')), toString(events.subject_key)) NOT IN
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

CREATE OR REPLACE VIEW linka_metric.datalens_plays_v3
SQL SECURITY DEFINER
AS
SELECT
    2 AS schema_version,
    'plays' AS stream,
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
    cityHash64(product, game_session_id) AS game_session_key,
    app_version,
    app_build,
    platform,
    os_version,
    locale,
    game_id,
    game_category,
    input_method,
    level_index,
    outcome,
    duration_ms,
    success_count,
    mistake_count,
    hint_count,
    valid_gaze_ratio
FROM linka_metric.plays_events_v2 AS events FINAL
WHERE (events.expires_at IS NULL OR events.expires_at > now())
AND (toString(events.product), toString(ifNull(events.product_key, '')), toString(events.subject_key)) NOT IN
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

CREATE OR REPLACE VIEW linka_metric.datalens_product_v2
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
WHERE (events.expires_at IS NULL OR events.expires_at > now())
AND (toString(events.product), toString(ifNull(events.product_key, '')), toString(events.subject_key)) NOT IN
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

-- datalens_game_sessions_v3 reads from datalens_plays_v3, so it inherits the
-- expiry filter above and needs no separate replacement.
