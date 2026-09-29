#!/bin/sh
set -eu

if sh deploy/clickhouse/preflight.sh; then
  exit 0
fi

echo "ClickHouse preflight failed; restarting ClickHouse once before retry" >&2
docker compose --profile privacy stop writer privacy-worker
restore_services=true
trap '
  status=$?
  if [ "$status" -ne 0 ] && [ "$restore_services" = true ]; then
    docker compose --profile privacy up -d --no-build writer privacy-worker || true
  fi
  exit "$status"
' EXIT
docker compose restart clickhouse

attempt=0
until docker compose exec -T clickhouse sh -eu -c \
  'clickhouse-client --user "$CLICKHOUSE_ADMIN_USER" --password "$CLICKHOUSE_ADMIN_PASSWORD" --query "SELECT 1" >/dev/null'
do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 30 ]; then
    echo "ClickHouse did not become ready after restart" >&2
    exit 1
  fi
  sleep 2
done

sh deploy/clickhouse/preflight.sh
restore_services=false
