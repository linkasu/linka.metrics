#!/bin/sh
set -eu

id=${1:-}
case "$id" in
  ''|*[!a-zA-Z0-9_-]*) echo 'Expected a safe release identifier' >&2; exit 2 ;;
esac
[ "${#id}" -le 64 ] || exit 2

docker compose exec -T clickhouse sh -s -- "$id" <<'REMOTE'
set -eu
query() {
  clickhouse-client --user "$CLICKHOUSE_ADMIN_USER" --password "$CLICKHOUSE_ADMIN_PASSWORD" \
    --max_execution_time 300 --max_threads 1 --query "$1"
}
id=$1
restore="retention_verify_$(printf '%s' "$id" | tr '-' '_')"
archive="backups/before-retention-$id.zip"
created=0
cleanup() {
  status=$?
  trap - EXIT
  if [ "$created" = 1 ] && ! query "DROP DATABASE $restore SYNC" >/dev/null; then
    echo 'Temporary restore database cleanup failed' >&2
    [ "$status" -ne 0 ] || status=1
  fi
  exit "$status"
}
trap cleanup EXIT

exists=$(query "EXISTS DATABASE $restore FORMAT TSV")
[ "$exists" = 0 ] || {
  echo 'Restore verification database already exists; refusing to overwrite it' >&2
  exit 1
}
bytes=$(query "SELECT ifNull(sum(bytes_on_disk),0) FROM system.parts WHERE database='linka_metric' AND active FORMAT TSV")
available=$(df -Pk /var/lib/clickhouse | awk 'NR == 2 { print $4 }')
[ "$((available * 1024))" -gt "$((bytes * 3 + 1073741824))" ] || {
  echo 'Insufficient free space for backup and isolated restore' >&2
  exit 1
}

if [ "$(query "SELECT count() FROM linka_metric.schema_migrations FINAL WHERE version=17 FORMAT TSV")" = 0 ]; then
  for table in ingest_batches_v2 record_registry_v2 privacy_suppressions_v2 privacy_deletion_progress_v2 common_events_v2 technical_events_v2 plays_events_v2 product_events_v2 product_outcomes_v2; do
    has_expiry=$(query "SELECT count() FROM system.columns WHERE database='linka_metric' AND table='$table' AND name='expires_at' FORMAT TSV")
    if [ "$has_expiry" = 1 ]; then
      [ "$(query "SELECT count() FROM linka_metric.$table WHERE expires_at IS NOT NULL FORMAT TSV")" = 0 ] || {
        echo "Existing non-NULL expiry in $table requires separate review before TTL activation" >&2
        exit 1
      }
    fi
  done
fi

if [ ! -f "/var/lib/clickhouse/backups/$archive" ]; then
  query "BACKUP DATABASE linka_metric TO File('$archive')"
fi
# A prior archive is trusted only after a successful native restore/checksum check.
created=1
query "RESTORE DATABASE linka_metric AS $restore FROM File('$archive')"
[ "$(query "SELECT count() FROM system.tables WHERE database='$restore' AND name IN ('ingest_batches_v2','record_registry_v2','privacy_suppressions_v2','privacy_deletion_progress_v2','common_events_v2','technical_events_v2','plays_events_v2','product_events_v2','product_outcomes_v2') FORMAT TSV")" = 9 ]
query "SELECT count() AS restored_product_rows FROM $restore.product_events_v2 FORMAT TSVWithNames"
printf 'Native backup and isolated restore verified: %s\n' "$archive"
REMOTE
