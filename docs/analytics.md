# Аналитические срезы CRM

## Статус и границы

Функция реализована в рабочей ветке, **в production ещё не развёрнута**. Production credentials подготовлены отдельно от Git; их значения, идентификаторы секретов и содержимое service account в документацию не включаются.

По результатам проверки основной сессии прошли реальный monthly executor и локальный A1 bridge. Полный quality gate ещё выполняется. Ожидание заголовка последней миграции в тесте исправлено на `018`; это поправка тестового ожидания, не дополнительное изменение runtime-кода. Эти результаты не означают, что production уже обновлён. В рамках документационного изменения обращения к source API не выполнялись.

Это aggregate-only read model, не SQL-прокси и не экспорт исходных событий. Контракт фиксированный (`contract_version: 1`), продукты: `linka-type`, `linka-looks`, `linka-pictures`, `linka-paperboard`, `linka-plays`, `linka-tts`. Наличие продукта или имени метрики в контракте не означает доступность данных.

## Методики и фактические возможности

| Источник / методика | Доступно | Ограничения |
| --- | --- | --- |
| ClickHouse `metrics-v2` / `installation-v2` | `active_1d`, `active_7d`, `active_30d`, `calendar_active`, `useful_actions`, `sessions` | Наблюдаемые установки / source identifiers, не люди и не подтверждённые аккаунты. Daily series: active и useful actions; monthly series: calendar active. |
| GA4 / `ga4` | `active_1d`, `active_7d`, `active_30d`, `calendar_active`, `sessions` | Property-local identifiers / sessions. GA4 aggregates не доказывают успешные действия или funnel. |
| TTS / `tts` | `service_operations`, `service_failed` | Типизированные `request_completed`: completed, failed, cancelled; не все HTTP-запросы. Фиксированный subject не означает охват людей. |
| `account-v3` и `funnel_*` | `unavailable` | Нужны соответствующие клиенты, verified aliases и доказанная история / полное семидневное покрытие; выводить funnel из activity нельзя. |

`active_1d/7d/30d` — distinct в полном окне, не сумма daily distinct. `calendar_active` — distinct последнего полного календарного месяца и ряд полных месяцев, не rolling 30 days. ClickHouse использует UTC, GA4 — timezone property из ответа; результат содержит точное полуоткрытое окно `window_from/window_to`.

Useful actions считают успешные outcomes (`utterance_completed`, `speech_completed`, завершённые игровые сессии), а не нажатия кнопки или просто opens. Paper Board технически имеет обработку `phrase_spoken`, но его release policy запрещает usage analytics: наличие кода и исторической property **не снимает policy gate** и не разрешает включать клиентский сбор без отдельного согласования политики и consent. Legacy synthetic session IDs (`import-*`) не считаются настоящими sessions.

ClickHouse поддерживает filters `platform`, `app_version`, `locale`, но не geography и не dimension breakdown. GA4 поддерживает filters `platform`, `app_version`, `country`, `region`; grouping через `dimensions` и locale filter сейчас возвращают unavailable. Нативные GA4 dimensions в capability не обещают произвольную группировку CRM-запроса. `/capabilities` возвращает только версию и список продуктов; source-specific возможности находятся в `report.capabilities`.

### Источники и историческое покрытие

Service account должен иметь только read-only доступ к этим четырём фактически зафиксированным properties; OAuth scope: `https://www.googleapis.com/auth/analytics.readonly`.

| Продукт | GA4 property |
| --- | --- |
| Напиши / `linka-type` | `151443791` |
| Покажи / `linka-pictures` | `340729214` |
| Paper Board / `linka-paperboard` | `251389924` |
| Смотри / `linka-looks` | `408073017` |

Для Type исторический поиск начинается с 2018 года в GA4, для Looks ClickHouse — с 2023 года. Это границы поиска, не гарантия непрерывного покрытия. Отсутствующая GA4 история отображается как `no_data` источника (в bridge/UI — sourceNoData), не как измеренный ноль и не как повод подставлять ClickHouse. Ошибка доступа / provider failure — `unavailable`, не `no_data`.

GA4 и ClickHouse сохраняются отдельными строками с source, methodology и unit. **Дедупликация между ними не заявляется.** Нельзя суммировать distinct по источникам, продуктам или датам и называть результат людьми. Отсутствие наблюдений в ClickHouse обычно даёт `null/no_data`, поскольку полное покрытие не доказано. GA4 quality flags (thresholding, sampling, other-row loss) сохраняются в reason с `approximate: true`.

## API и чтение без тяжёлых запросов

CRM GET должен читать подготовленный snapshot через bridge, не запускать heavy scan, source API или build на каждый просмотр. В этом репозитории внутренний writer API принимает **только POST** с `Content-Type: application/json` и типизированным телом (до 16 KiB); GET получает 405.

| POST path | Назначение |
| --- | --- |
| `/internal/analytics/query` | Прочитать опубликованный срез; 200 либо 202 `pending`, без автоматической постановки в очередь. |
| `/internal/analytics/sync-requests` | Поставить работу в очередь; 202 с run. Request ID — UUID. Повтор с тем же query идемпотентен, с другим query — 409. |
| `/internal/analytics/sync-runs/<uuid>` | Прочитать состояние run; тело строго `{}`. |
| `/internal/analytics/capabilities` | Версия и продукты; тело — валидный Query, как для query. |

Пример тела (без секретов и подписи):

```json
{
  "contract_version": 1,
  "products": ["linka-type"],
  "metrics": ["active_1d", "calendar_active", "sessions"],
  "cutoff_date": "2026-11-01",
  "series_from": "2026-10-01",
  "series_to": "2026-11-01"
}
```

Неизвестные поля, метрики, продукты, недопустимые filters и диапазоны отклоняются. Query range включительный, максимум 366 дней. Это не произвольный SQL и не произвольный набор GA4 metrics.

Используется отдельный `ANALYTICS_HMAC` keyring, caller строго `crm-analytics`, без fallback на collector / telemetry JWT. Все вызовы подписываются ServiceSigner (`internal/auth/service.go`):

- `X-Linka-Signature-Version: v2`, `X-Linka-Key-ID`, `X-Linka-Caller`;
- `X-Linka-Request-ID`, `X-Linka-Timestamp` (Unix seconds, skew до 5 минут);
- `X-Linka-Body-SHA256` (lowercase hex), `X-Linka-Signature` (raw URL-safe base64 HMAC-SHA256).

Подписываются точные байты body и escaped path. Каноническая строка — соединённые `\n` значения: `LINKA-HMAC-V2`, `v2`, upper-case method, path, caller, key ID, request ID, timestamp, body SHA256; без завершающего newline. Использовать существующий signer, не собирать подпись в браузере. Секреты доступны только серверному bridge. Ответы `Cache-Control: no-store`; ошибки provider не раскрываются клиенту.

## Очередь, worker и публикация

Обязательная topology: **один HTTP writer очереди и один serial worker на одном хосте**. Process-local mutex не распределённая блокировка, а ReplacingMergeTree не distributed compare-and-swap. Не масштабировать writer enqueue горизонтально. Worker также seed-ит default/backfill requests; backfill не запускать параллельными процессами.

`writer analytics-sync` (в image entrypoint уже `/app/service`) держит `flock(LOCK_EX | LOCK_NB)` всю жизнь процесса. Все экземпляры worker, включая one-off команды, обязаны видеть один lock inode на общем host bind volume. Изолированный `/tmp` в каждом контейнере защиты не даёт. Compose задаёт:

- `ANALYTICS_LOCK_PATH=/analytics-runtime/executor.lock`;
- bind `${DATA_ROOT:-./data}/analytics-runtime:/analytics-runtime`;
- runtime app UID:GID `100:101`; `deploy/analytics-init.sh` выставляет владельца и mode `0700` только этому каталогу, не обходит существующие данные.

По умолчанию вне Compose lock — `<os.TempDir()>/linka-metrics-analytics/executor.lock`; это пригодно только при действительно общем single-host filesystem. Lock не защищает два разных хоста. Второй executor завершится ошибкой, не начнёт конкурентную обработку.

Run проходит `queued → running → validating → publishing → completed`; ошибки отмечаются `failed`. После interruption активный run подхватывается заново, publishing восстанавливается по generation ID. `cancelled` — отдельное terminal state хранилища, не синоним failed; отдельного cancellation endpoint нет. Worker проверяет очередь каждые 30 секунд и ограничивает run 15 минутами.

Default query закреплён на **последнем полностью завершённом воскресенье UTC**: в само воскресенье используется предыдущее. Cutoff и 30-дневный series range стабильны всю отчётную неделю. Worker автоматически seed-ит только этот default slice; ручные filters/ranges требуют явного sync request.

`analytics-sync --once` seed-ит default, обрабатывает доступную очередь и выходит. Опциональный `analytics-sync --once --backfill-from-year=2018` ставит отдельные полные годы и текущий YTD до weekly cutoff. **Не запускать автоматически:** даже bounded requests могут создать тяжёлую многолетнюю нагрузку и исчерпать GA4 quota. Backfill идёт последовательными годовыми chunks; каждый native date range максимум 366 дней, monthly GA4 series также разбивается на годовые chunks. Это не безлимитный исторический scan. ClickHouse calendar series ограничен 13 месяцами; budgets могут дать unavailable вместо отчёта. Сначала проверять ограниченный диапазон и планировать backfill отдельно.

### Schema и privacy

`018_analytics_snapshots.sql` аддитивно создаёт ровно три таблицы:

- `analytics_requests_v1`: run journal, `ReplacingMergeTree(version)`;
- `analytics_request_aliases_v1`: request-ID aliases для idempotency;
- `analytics_snapshots_v1`: JSON aggregate snapshot, generation ID, privacy revision, expiry, монотонная `revision` с `ReplacingMergeTree(revision)`.

Существующие events / identity и исторические expiry не переписываются. Для существующей базы нужна migration runner, одних init mounts недостаточно. Сначала миграция, затем новый writer/worker. Старый binary не использует добавленные таблицы; новый binary до 018 не может обслужить analytics. При rollback остановить новый worker и оставить аддитивные таблицы, не удалять данные.

Перед compute и publish проверяется privacy revision, чтение также сверяет её с текущим suppression state. Snapshot живёт не более 8 дней и ограничивается ближайшим expiry используемых ClickHouse source rows. Изменение privacy или истечение срока инвалидирует snapshot: возвращается pending без устаревших результатов, не stale fallback. Read path читает snapshots и bounded privacy-state check, но не пересчитывает raw-event aggregates или source expiry scan. Это безопасная инвалидизация, не обещание атомарной транзакции между независимыми privacy writes и HTTP response.

## Конфигурация и запуск после отдельного разрешения

Опциональные закомментированные настройки находятся в `.env.example`. Для writer нужны обе `ANALYTICS_HMAC_ACTIVE_KEY_ID` и `ANALYTICS_HMAC_ACTIVE_SECRET` (не менее 32 bytes); частичная конфигурация останавливает старт. Previous key ID/secret задаются парой для ротации, ID должен отличаться от active. Сначала допускают обе версии, переключают bridge на новый active, затем удаляют previous после переходного периода. При полностью отсутствующем analytics keyring route не подключается.

Worker требует `GA4_SERVICE_ACCOUNT_JSON_BASE64` — standard-base64 JSON service account, только в защищённом runtime environment. Не публиковать `.env`, decoded JSON, tokens или `docker compose config` с раскрытыми значениями. Не передавать credentials в query, логи или примеры.

Использовать **только CI-published GHCR images** с immutable `IMAGE_TAG=sha-<40-hex-commit>` соответствующего release; `latest` из общего примера не подходит для production. Не собирать images локально или на сервере. Writer и analytics-worker используют один writer image. Deployment wiring в `.github/workflows/deploy-vps.yml` сначала pull-ит images, выполняет миграции, обновляет writer и вызывает `deploy/analytics-start.sh`.

`deploy/analytics-start.sh` проверяет наличие GA4 credentials и active HMAC secret в runtime `.env` без вывода значений. При отсутствии оставляет worker выключенным; при наличии вызывает init и `docker compose --profile analytics up -d --no-build analytics-worker`. Это presence check, не полноценная проверка keyring и permissions; задание active key ID и доступ к четырём properties обязательны отдельно.

Worker подключён к закрытой `backend` для ClickHouse и существующей `edge` для исходящего HTTPS к Google OAuth/Data API; опубликованных портов у него нет. Граница проверяется `.github/scripts/test-analytics-deployment.py`. До завершения rollout отдельно проверить реальный GA4-read из production container: успешный локальный bridge не заменяет эту проверку. ClickHouse и HTTP writer остаются только в закрытой сети.
