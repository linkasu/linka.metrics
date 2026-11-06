# Staging и upgrade

## Изоляция staging

Используйте отдельный ClickHouse data root, DNS names, secrets, service key IDs и immutable `sha-*` image tag. Шаблон `deploy/staging.env.example` содержит только фиктивные значения; реальные secrets должны поступать из защищённого runtime environment. Не используйте production Terraform backend key или production Lockbox version для staging.

Перед первым запуском:

1. Создайте отдельный env-файл с правами `0600` вне Git.
2. Заполните все девять `RETENTION_V2_*`: `4320h` для raw events (включая outcome), batches и registry, `26280h` для завершённых privacy-записей. Production отвергает пустые durations; development может оставлять новые expiry NULL. Активные opt-out и незавершённые privacy jobs не истекают.
3. Запустите ClickHouse и примените migrations командой `docker compose --profile migration run --rm migrate`.
4. Выполните `deploy/clickhouse/preflight.sh`, затем примените least-privilege grants через `deploy/clickhouse/configure-v2-users.sh`.
5. Запустите writer, collector и только затем профиль `privacy`.
6. Проверьте new ingest, same-body replay, changed-body `409`, opt-out block и deletion на synthetic opaque scope.

## Upgrade существующей V1 БД

Сделайте backup/restore rehearsal средствами ClickHouse до изменения production. Не редактируйте `001-003`: unit test фиксирует их SHA-256. Запустите ровно один migration runner. Он безопасно выполняет idempotent старые DDL, записывает checksum ledger и останавливается при drift. Затем примените V2 grants и только после этого обновите writer.

Runner не использует Terraform и не читает state. Terraform validation следует запускать как `terraform init -backend=false && terraform validate`; команды `plan`, `apply`, state subcommands и local `tfvars` в этой процедуре не нужны.

## Runtime gates

Не включайте active-active writer/worker до появления distributed claim lock. Production обязан использовать HTTPS Identity JWKS с exact issuer/audience; legacy `/v2/tokens` разрешён только при `DEPLOYMENT_ENVIRONMENT=staging` и `ALLOW_LEGACY_PRODUCT_TOKENS=true`. SQL TTL активен с migration `017`; исторические NULL остаются без expiry. До миграции выполните `sh deploy/clickhouse/backup-retention.sh <release-id>`: backup должен успешно восстановиться в временную БД. При откате binary не запускайте старый migration runner поверх ledger 017; новые nullable колонки совместимы со старым writer, активные opt-out защищены SQL независимо от версии binary.
