#!/bin/sh
set -eu

container="linka-plays-metric-clickhouse-1"

wait_ready() {
  attempt=0
  until docker compose exec -T clickhouse sh -eu -c \
    'clickhouse-client --user "$CLICKHOUSE_ADMIN_USER" --password "$CLICKHOUSE_ADMIN_PASSWORD" --query "SELECT 1" >/dev/null'
  do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 60 ]; then
      echo "ClickHouse did not become ready" >&2
      return 1
    fi
    sleep 3
  done
}

restore() {
  docker update --memory 2g --memory-swap 4g "$container" >/dev/null
  docker compose restart clickhouse >/dev/null
  wait_ready
  docker compose --profile privacy up -d --no-build writer privacy-worker >/dev/null
}

cleanup() {
  restore || true
}
trap cleanup EXIT

docker compose --profile privacy stop writer privacy-worker
docker update --memory 3584m --memory-swap 5g "$container" >/dev/null
docker compose restart clickhouse
wait_ready
docker compose exec -T clickhouse sh -eu -c \
  'clickhouse-client --user "$CLICKHOUSE_ADMIN_USER" --password "$CLICKHOUSE_ADMIN_PASSWORD" --query "TRUNCATE TABLE system.error_log"'

restore
trap - EXIT
