# Staging и upgrade

## Изоляция staging

Используйте отдельный ClickHouse data root, DNS names, secrets, service key IDs и immutable `sha-*` image tag. Шаблон `deploy/staging.env.example` содержит только фиктивные значения; реальные secrets должны поступать из защищённого runtime environment. Не используйте production Terraform backend key или production Lockbox version для staging.

Перед первым запуском:

1. Создайте отдельный env-файл с правами `0600` вне Git.
2. Заполните все `RETENTION_V2_*` утверждёнными сроками: `4320h` (180 дней) для raw events, batches и registry, `26280h` (3 года) для privacy tables. Пустое значение означает `expires_at=NULL`, и такие строки не удаляются никогда.
3. Запустите ClickHouse и примените migrations командой `docker compose --profile migration run --rm migrate`.
4. Выполните `deploy/clickhouse/preflight.sh`, затем примените least-privilege grants через `deploy/clickhouse/configure-v2-users.sh`.
5. Запустите writer, collector и только затем профиль `privacy`.
6. Проверьте new ingest, same-body replay, changed-body `409`, opt-out block и deletion на synthetic opaque scope.

## Upgrade существующей V1 БД

Сделайте backup/restore rehearsal средствами ClickHouse до изменения production. Не редактируйте `001-003`: unit test фиксирует их SHA-256. Запустите ровно один migration runner. Он безопасно выполняет idempotent старые DDL, записывает checksum ledger и останавливается при drift. Затем примените V2 grants и только после этого обновите writer.

Runner не использует Terraform и не читает state. Terraform validation следует запускать как `terraform init -backend=false && terraform validate`; команды `plan`, `apply`, state subcommands и local `tfvars` в этой процедуре не нужны.

## Runtime gates

Не включайте active-active writer/worker до появления distributed claim lock. Production обязан использовать HTTPS Identity JWKS с exact issuer/audience; legacy `/v2/tokens` разрешён только при `DEPLOYMENT_ENVIRONMENT=staging` и `ALLOW_LEGACY_PRODUCT_TOKENS=true`. SQL TTL активен с migration `013` и управляется только через `expires_at`: заполненные `RETENTION_V2_*` обязательны в production, исторические NULL-строки backfill'ятся отдельным operational шагом.
