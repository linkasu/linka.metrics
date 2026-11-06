#!/bin/sh
set -eu

# Credentials are provisioned separately into the protected runtime .env.
# Presence checks never print values or dump the expanded Compose configuration.
if ! grep -q '^GA4_SERVICE_ACCOUNT_JSON_BASE64=.' .env || ! grep -q '^ANALYTICS_HMAC_ACTIVE_SECRET=.' .env; then
  printf '%s\n' 'Analytics credentials are not provisioned; worker remains disabled.'
  exit 0
fi
sh deploy/analytics-init.sh
docker compose --profile analytics up -d --no-build analytics-worker
