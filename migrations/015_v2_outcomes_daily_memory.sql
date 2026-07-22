CREATE OR REPLACE VIEW linka_metric.datalens_outcomes_daily_v1
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
    uniqCombined64(subject_key) AS active_subject_count,
    uniqCombined64(app_session_key) AS active_session_count
FROM linka_metric.datalens_outcomes_v1
GROUP BY occurred_date, product, kind, result, source, mode, platform, app_version
SETTINGS max_bytes_before_external_group_by = 268435456;

CREATE OR REPLACE VIEW linka_metric.datalens_tts_operations_daily_v1
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
GROUP BY occurred_date, provider, result, duration_bucket, count_bucket, failure_code
SETTINGS max_bytes_before_external_group_by = 268435456;
