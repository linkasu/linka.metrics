#!/bin/sh
set -eu

[ -f migrations/001_database.sql ] || { echo 'Run preflight from the repository root' >&2; exit 1; }
expected=$(printf '%s\n' migrations/[0-9][0-9][0-9]_*.sql | awk -F '[/_]' '{ print $2 + 0 }' | sort -n)

docker compose exec -T clickhouse sh -eu -c '
client() {
  clickhouse-client --user "$CLICKHOUSE_ADMIN_USER" --password "$CLICKHOUSE_ADMIN_PASSWORD" --query "$1"
}

has_table() {
  [ "$(client "SELECT 1 FROM system.tables WHERE database = '\''linka_metric'\'' AND name = '\''$1'\'' LIMIT 1")" = "1" ]
}

has_column() {
  [ "$(client "SELECT 1 FROM system.columns WHERE database = '\''linka_metric'\'' AND table = '\''$1'\'' AND name = '\''$2'\'' LIMIT 1")" = "1" ]
}

[ "$(client "SELECT version FROM linka_metric.schema_migrations FINAL ORDER BY version FORMAT TSVRaw")" = "$1" ]
for column in attempts available_at lease_until legacy_installation_id; do has_column privacy_suppressions_v2 "$column"; done
for table in privacy_deletion_progress_v2 record_registry_v2; do has_table "$table"; done
has_column ingest_batches_v2 status
for column in product_key subject_key person_key org_key; do has_column record_registry_v2 "$column"; done
for table in product_events_v2 datalens_product_v2; do has_table "$table"; done
for table in datalens_common_v3 datalens_technical_v3 datalens_plays_v3 datalens_game_sessions_v3; do has_table "$table"; done
for table in product_outcomes_v2 datalens_outcomes_v1 datalens_outcomes_daily_v1 datalens_tts_operations_daily_v1 datalens_telemetry_quality_daily_v1; do has_table "$table"; done
for table in fundraising_ingest_batches_v1 fundraising_events_v1 datalens_fundraising_v1 fundraising_finance_daily_v1; do has_table "$table"; done
for table in ingest_batches_v2 record_registry_v2 privacy_suppressions_v2 privacy_deletion_progress_v2 common_events_v2 technical_events_v2 plays_events_v2 product_events_v2 product_outcomes_v2; do
  has_column "$table" expires_at
  case "$(client "SHOW CREATE TABLE linka_metric.$table")" in
    *"TTL "*) ;;
    *) exit 1 ;;
  esac
done

case "$(client "SHOW CREATE TABLE linka_metric.privacy_deletion_progress_v2")" in
  *"ReplacingMergeTree(updated_at)"*"ORDER BY (product, request_id, table_name)"*) ;;
  *) exit 1 ;;
esac
' -- "$expected"
