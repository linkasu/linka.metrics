#!/bin/sh
set -eu

# Only the new application-owned lock directory is writable. UID/GID are the
# app identity in the published Alpine image; no existing data is traversed.
docker compose --profile analytics run --rm --no-deps --user 0:0 \
  --entrypoint /bin/sh analytics-worker -c 'chown 100:101 /analytics-runtime && chmod 0700 /analytics-runtime'
