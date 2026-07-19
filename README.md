# LINKa Metrics

Единый multi-product сервис обезличенной технической, продуктовой и игровой телеметрии LINKa. Поддерживаемые продукты регистрируются закрытым compile-time allowlist; произвольные события и payload не принимаются. Репозиторий содержит пять Go-бинарей:

- `collector` выдаёт анонимный installation token, проверяет контракт v1 и синхронно пересылает batch в writer;
- `writer` проверяет отдельную HMAC-подпись collector и записывает данные в ClickHouse;
- `disk-alert` отправляет одно SMTP-уведомление при достижении порога диска и не повторяет его до восстановления.
- `migrate` применяет embedded ClickHouse migrations через checksum ledger;
- `privacy-worker` выполняет lease/retry-based удаление V1/V2 telemetry с durable per-table progress.

Collector не считает installation token доказательством подлинности приложения. Токен только связывает случайный UUID установки с HMAC сервера. IP-адреса и тела запросов не журналируются.

## Разработка

Требуется Go 1.24 или совместимая более новая версия.

```bash
go mod download
make check
make build
```

Локальный запуск writer требует применённых миграций и переменных ClickHouse:

```bash
WRITER_HMAC_SECRET='at-least-32-random-bytes-change-me' \
CLICKHOUSE_PASSWORD='writer-password' \
go run ./cmd/writer
```

Collector:

```bash
INSTALLATION_HMAC_SECRET='at-least-32-random-bytes-change-me' \
WRITER_HMAC_SECRET='another-at-least-32-byte-secret' \
WRITER_URL='http://127.0.0.1:8081' \
DEPLOYMENT_ENVIRONMENT=staging \
ALLOW_LEGACY_PRODUCT_TOKENS=true \
PRODUCT_TOKEN_HMAC_ACTIVE_SECRET='local-staging-token-secret-at-least-32-bytes' \
SUBJECT_KEY_HMAC_SECRET='local-stable-subject-secret-at-least-32-bytes' \
go run ./cmd/collector
```

Production instead requires `IDENTITY_JWKS_URL` (HTTPS), exact `IDENTITY_TOKEN_ISSUER` and product-specific `IDENTITY_TELEMETRY_AUDIENCES_JSON` (or its base64 form); legacy token minting cannot be enabled in production. Canonical public endpoint is `https://metrics.nkolinka.ru`.

## Контейнеры

Один `Dockerfile` собирает нужный бинарь через build argument `BINARY`. Образы собираются только GitHub Actions и публикуются в GHCR; локальная и серверная production-сборка не используется.

Для VPS скопируйте `.env.example` в `.env`, замените все пароли и HMAC-секреты случайными значениями и создайте bind-каталоги из `DATA_ROOT`. Затем используйте только опубликованный CI-образ:

```bash
docker compose pull
docker compose up -d --no-build
```

ClickHouse слушает `8123/9000` только во внутренней Docker-сети. Caddy публикует HTTPS для writer и HTTP-интерфейса БД. Доступ к БД ограничен официальными IPv4-подсетями DataLens; логин `datalens` имеет `SELECT` только на представления. Значения паролей берутся из `.env`, хешируются init-скриптом и не хранятся в репозитории.

SQL из `docker-entrypoint-initdb.d` выполняется образом ClickHouse только при инициализации пустого bind-каталога. Для уже существующей БД новые миграции следует применять отдельно через `clickhouse-client` от администратора.

## Disk alert

Соберите `disk-alert` тем же Dockerfile с `--build-arg BINARY=disk-alert` либо установите бинарь на VPS. Сохраните SMTP-переменные в `/etc/linka-metric/disk-alert.env` с правами `0600`. Пример cron каждые 15 минут:

```cron
*/15 * * * * /bin/sh -c 'set -a; . /etc/linka-metric/disk-alert.env; exec /usr/local/bin/linka-disk-alert'
```

Environment-файл задаёт `DISK_PATH`, `DISK_THRESHOLD_PERCENT=80`, `DISK_ALERT_STATE_FILE`, `SMTP_ADDR`, `SMTP_FROM`, `SMTP_TO=ivan@aacidov.ru`, `SMTP_USER` и `SMTP_PASSWORD`. SMTP-сервер обязан поддерживать STARTTLS. Состояние `alerted` записывается только после успешной отправки и сбрасывается после снижения заполнения ниже 80%.

## Документация

- [API v1](docs/api.md)
- [Telemetry V2](docs/telemetry-v2.md)
- [Staging и upgrade](docs/staging.md)
- [Политика приватности](docs/privacy.md)
- [Terraform и bootstrap YC](infra/terraform/README.md)

## CI/CD

`CI` запускает gofmt, unit tests, ClickHouse 25.3 migration/integration tests, vet и проверочную сборку образов. После успешного `main` workflow `Publish Images` публикует collector, writer и disk-alert в GHCR. Deploy workflows скачивают уже собранные образы и не собирают их на VPS или в Yandex Cloud.

Необходимые GitHub secrets:

- VPS: `VPS_HOST`, `VPS_USER`, `VPS_SSH_KEY`, `VPS_KNOWN_HOSTS`, `VPS_DEPLOY_PATH`, `CLICKHOUSE_DATALENS_PASSWORD`;
- YC: `YC_SA_KEY_JSON`, `YC_CLOUD_ID`, dedicated `YC_FOLDER_ID`, `YC_REGISTRY_ID`, `YC_RUNTIME_SA_ID`, `YC_LOCKBOX_SECRET_ID`, `YC_LOCKBOX_SECRET_VERSION_ID`, `WRITER_URL`.

Production YC environment variables: `IDENTITY_JWKS_URL`, `IDENTITY_TOKEN_ISSUER`, `IDENTITY_TELEMETRY_AUDIENCES_JSON`, `CORS_ALLOWED_ORIGINS` and `COLLECTOR_PUBLIC_URL`.
