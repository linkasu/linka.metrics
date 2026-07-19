CREATE VIEW IF NOT EXISTS linka_metric.datalens_common_v3
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

CREATE VIEW IF NOT EXISTS linka_metric.datalens_technical_v3
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

CREATE VIEW IF NOT EXISTS linka_metric.datalens_plays_v3
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

CREATE VIEW IF NOT EXISTS linka_metric.datalens_game_sessions_v3
SQL SECURITY DEFINER
AS
SELECT
    product,
    game_session_key,
    any(product_key) AS product_key,
    any(subject_key) AS subject_key,
    any(person_key) AS person_key,
    any(org_key) AS org_key,
    any(app_session_key) AS app_session_key,
    if(countIf(kind = 'session_started') = 0, NULL, minIf(occurred_at, kind = 'session_started')) AS started_at,
    if(countIf(kind = 'session_finished') = 0, NULL, maxIf(occurred_at, kind = 'session_finished')) AS ended_at,
    if(countIf(kind = 'session_started') = 0, NULL, toDate(minIf(occurred_at, kind = 'session_started'), 'UTC')) AS session_date,
    countIf(kind = 'session_started') > 0 AS has_started,
    countIf(kind = 'session_finished') > 0 AS has_finished,
    if(countIf(kind = 'session_started') = 0, 'finish_without_start', if(countIf(kind = 'session_finished') = 0, 'unfinished', 'complete')) AS data_quality_status,
    argMaxIf(ifNull(outcome, 'unknown'), occurred_at, kind = 'session_finished') AS session_status,
    any(game_id) AS game_id,
    any(game_category) AS game_category,
    any(input_method) AS input_method,
    maxIf(duration_ms, kind = 'session_finished') AS duration_ms,
    maxIf(success_count, kind = 'session_finished') AS reported_success_count,
    maxIf(mistake_count, kind = 'session_finished') AS reported_mistake_count,
    maxIf(hint_count, kind = 'session_finished') AS reported_hint_count,
    maxIf(valid_gaze_ratio, kind = 'session_finished') AS valid_gaze_ratio,
    countIf(kind = 'interaction') AS interaction_count,
    countIf(kind = 'interaction' AND outcome = 'success') AS interaction_success_count,
    countIf(kind = 'interaction' AND outcome = 'mistake') AS interaction_mistake_count,
    countIf(kind = 'interaction' AND outcome = 'hint') AS interaction_hint_count,
    countIf(kind = 'interaction' AND outcome = 'cancelled') AS interaction_cancelled_count,
    any(app_version) AS app_version,
    any(app_build) AS app_build,
    any(platform) AS platform,
    any(os_version) AS os_version,
    any(locale) AS locale
FROM linka_metric.datalens_plays_v3
GROUP BY product, game_session_key;
