#!/bin/sh
set -eu

id=${1:-}
case "$id" in
  ''|*[!a-zA-Z0-9_-]*) echo 'Expected a safe release identifier' >&2; exit 2 ;;
esac
[ "${#id}" -le 64 ] || exit 2
[ -f .env ] && [ ! -L .env ] || exit 1
backup=".env.before-retention-$id"
if [ ! -e "$backup" ]; then
  (umask 077; set -C; cat .env > "$backup")
fi
temporary=$(mktemp .env.retention.XXXXXXXXXX)
trap 'rm -f -- "$temporary"' EXIT
cp -p .env "$temporary"
awk -F= '
BEGIN {
  split("INGEST_BATCHES COMMON TECHNICAL PLAYS PRODUCT OUTCOME REGISTRY PRIVACY DELETION_PROGRESS", names, " ")
  for (i in names) {
    key = "RETENTION_V2_" names[i]
    values[key] = (names[i] == "PRIVACY" || names[i] == "DELETION_PROGRESS") ? "26280h" : "4320h"
  }
}
{
  if ($1 in values) { print $1 "=" values[$1]; seen[$1] = 1 }
  else { print }
}
END { for (key in values) if (!(key in seen)) print key "=" values[key] }
' .env > "$temporary"
mv -- "$temporary" .env
trap - EXIT
printf 'Approved retention settings activated; prior environment retained in %s\n' "$backup"
