#!/bin/sh
set -eu

docker compose exec -T clickhouse sh -eu -c '
client() {
  clickhouse-client --user "$CLICKHOUSE_ADMIN_USER" --password "$CLICKHOUSE_ADMIN_PASSWORD" --query "$1"
}

[ "$(client "SELECT count(), max(version) FROM linka_metric.schema_migrations FORMAT TSVRaw")" = "$(printf "10\t10")" ]
[ "$(client "SELECT count() FROM system.columns WHERE database = '\''linka_metric'\'' AND table = '\''privacy_suppressions_v2'\'' AND name IN ('\''attempts'\'', '\''available_at'\'', '\''lease_until'\'', '\''legacy_installation_id'\'')")" = "4" ]
[ "$(client "SELECT count() FROM system.tables WHERE database = '\''linka_metric'\'' AND name IN ('\''privacy_deletion_progress_v2'\'', '\''record_registry_v2'\'')")" = "2" ]
[ "$(client "SELECT count() FROM system.columns WHERE database = '\''linka_metric'\'' AND table = '\''ingest_batches_v2'\'' AND name = '\''status'\''")" = "1" ]
[ "$(client "SELECT count() FROM system.columns WHERE database = '\''linka_metric'\'' AND table = '\''record_registry_v2'\'' AND name IN ('\''product_key'\'', '\''subject_key'\'', '\''person_key'\'', '\''org_key'\'')")" = "4" ]

case "$(client "SHOW CREATE TABLE linka_metric.privacy_deletion_progress_v2")" in
  *"ReplacingMergeTree(updated_at)"*"ORDER BY (product, request_id, table_name)"*) ;;
  *) exit 1 ;;
esac
'
