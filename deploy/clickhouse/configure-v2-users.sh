#!/bin/sh
set -eu

# Re-run the idempotent in-container user/grant bootstrap so secrets are read
# from the ClickHouse service environment, never from command arguments.
docker compose exec -T clickhouse sh -s < deploy/clickhouse/init/010-users.sh
