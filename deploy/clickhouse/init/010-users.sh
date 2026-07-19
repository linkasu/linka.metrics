#!/bin/sh
set -eu

: "${CLICKHOUSE_ADMIN_USER:?CLICKHOUSE_ADMIN_USER is required}"
: "${CLICKHOUSE_ADMIN_PASSWORD:?CLICKHOUSE_ADMIN_PASSWORD is required}"
: "${CLICKHOUSE_WRITER_PASSWORD:?CLICKHOUSE_WRITER_PASSWORD is required}"
: "${CLICKHOUSE_PRIVACY_PASSWORD:?CLICKHOUSE_PRIVACY_PASSWORD is required}"
: "${CLICKHOUSE_DATALENS_PASSWORD:?CLICKHOUSE_DATALENS_PASSWORD is required}"

writer_hash="$(printf '%s' "$CLICKHOUSE_WRITER_PASSWORD" | sha256sum | cut -d ' ' -f 1)"
privacy_hash="$(printf '%s' "$CLICKHOUSE_PRIVACY_PASSWORD" | sha256sum | cut -d ' ' -f 1)"
datalens_hash="$(printf '%s' "$CLICKHOUSE_DATALENS_PASSWORD" | sha256sum | cut -d ' ' -f 1)"

clickhouse-client \
  --user "$CLICKHOUSE_ADMIN_USER" \
  --password "$CLICKHOUSE_ADMIN_PASSWORD" \
  --multiquery \
  --query "
    CREATE USER OR REPLACE metric_writer
      IDENTIFIED WITH sha256_hash BY '${writer_hash}'
      HOST ANY;
    CREATE USER OR REPLACE datalens
      IDENTIFIED WITH sha256_hash BY '${datalens_hash}'
      HOST ANY
      SETTINGS readonly = 2;
    CREATE USER OR REPLACE metric_privacy
      IDENTIFIED WITH sha256_hash BY '${privacy_hash}'
      HOST ANY;
    GRANT INSERT ON linka_metric.events TO metric_writer;
    GRANT INSERT ON linka_metric.session_summaries TO metric_writer;
    GRANT SELECT ON linka_metric.ingest_batches_v2 TO metric_writer;
    GRANT SELECT ON linka_metric.privacy_suppressions_v2 TO metric_writer;
    GRANT INSERT ON linka_metric.ingest_batches_v2 TO metric_writer;
    GRANT INSERT ON linka_metric.privacy_suppressions_v2 TO metric_writer;
    GRANT INSERT ON linka_metric.common_events_v2 TO metric_writer;
    GRANT INSERT ON linka_metric.technical_events_v2 TO metric_writer;
    GRANT INSERT ON linka_metric.plays_events_v2 TO metric_writer;
    GRANT INSERT ON linka_metric.product_events_v2 TO metric_writer;
    GRANT SELECT, INSERT ON linka_metric.record_registry_v2 TO metric_writer;
    GRANT SELECT, INSERT ON linka_metric.privacy_suppressions_v2 TO metric_privacy;
    GRANT SELECT, INSERT ON linka_metric.privacy_deletion_progress_v2 TO metric_privacy;
    GRANT ALTER DELETE ON linka_metric.record_registry_v2 TO metric_privacy;
    GRANT ALTER DELETE ON linka_metric.ingest_batches_v2 TO metric_privacy;
    GRANT ALTER DELETE ON linka_metric.common_events_v2 TO metric_privacy;
    GRANT ALTER DELETE ON linka_metric.technical_events_v2 TO metric_privacy;
    GRANT ALTER DELETE ON linka_metric.plays_events_v2 TO metric_privacy;
    GRANT ALTER DELETE ON linka_metric.product_events_v2 TO metric_privacy;
    GRANT ALTER DELETE ON linka_metric.events TO metric_privacy;
    GRANT ALTER DELETE ON linka_metric.session_summaries TO metric_privacy;
    GRANT SELECT ON linka_metric.datalens_events TO datalens;
    GRANT SELECT ON linka_metric.datalens_session_summaries TO datalens;
    GRANT SELECT ON linka_metric.datalens_common_v2 TO datalens;
    GRANT SELECT ON linka_metric.datalens_technical_v2 TO datalens;
    GRANT SELECT ON linka_metric.datalens_plays_v2 TO datalens;
    GRANT SELECT ON linka_metric.datalens_product_v2 TO datalens;
    GRANT SELECT ON linka_metric.datalens_common_v3 TO datalens;
    GRANT SELECT ON linka_metric.datalens_technical_v3 TO datalens;
    GRANT SELECT ON linka_metric.datalens_plays_v3 TO datalens;
    GRANT SELECT ON linka_metric.datalens_game_sessions_v3 TO datalens;
  "
