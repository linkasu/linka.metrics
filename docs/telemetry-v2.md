# Telemetry V2

## Контракт

Публичный collector предоставляет `POST /v2/batches` и `POST /v2/privacy/requests`. `POST /v2/tokens` является только staging compatibility route. Writer предоставляет соответствующие service-only endpoints `POST /internal/v2/batches` и `POST /internal/v2/privacy/requests`.

Batch ограничен 512 KiB, 500 records, глубиной JSON 16 и одним typed stream: `common`, `technical`, `plays` или `product`. Decoder запрещает duplicate keys, unknown fields, trailing values и чрезмерную вложенность. Product, stream, kinds, states, pages, modes, locales, input methods, game categories, outcomes и game IDs проверяются по compile-time allowlists в `internal/product` и `internal/contract/v2`.

`product` содержит только базовые metadata и закрытое product-specific значение `kind`. Произвольные `payload`, `attributes`, текстовые значения, URL, пути и пользовательские идентификаторы отсутствуют в контракте и отклоняются strict decoder.

`sent_at` допускается от семи дней в прошлом до пяти минут в будущем. `occurred_at` должен быть в диапазоне от 30 дней до `sent_at` до пяти минут после него. Timestamp обязан быть RFC3339 с точностью не выше миллисекунд и попадать в storage range `[2020-01-01, 2100-01-01)`. Счётчики, duration и ratios имеют явные границы.

## Scope и tokens

Production V2 token выдаёт LINKa Identity и подписывает Ed25519 active key. Collector загружает active и retiring keys из HTTPS JWKS и проверяет точные issuer, audience, product, scope, lifetime, `jti` и pairwise subject claims. Batch scope обязан точно совпасть с claims.

Identity публикует active и retiring JWT keys. Для ротации сначала добавьте новый key и сделайте его active, оставьте предыдущий retiring минимум на maximum token TTL плюс skew, затем удалите. Collector обновляет JWKS cache при неизвестном `kid`. Collector-to-writer service HMAC независимо поддерживает active/previous keys.

## Идемпотентность

`Idempotency-Key` обязателен, должен быть UUID и в точности равен `batch_id` или privacy `request_id`. Writer сначала резервирует `batch_id` и SHA-256 точного request body в ClickHouse ledger, а затем пишет records:

- новый ID и body: `202`, `replayed=false`;
- тот же ID и тот же body: `202`, `replayed=true`, без повторной логической вставки;
- тот же ID и другой body: `409 idempotency_conflict`.

Если процесс завершился после reservation или части records, тот же body продолжает запись и переводит ledger в `completed`; другой body конфликтует уже с reservation. Stable record IDs и `ReplacingMergeTree` делают повтор partial-write безопасным.

Writer сериализует check/write внутри процесса, а `record_registry_v2` отклоняет повторное использование `record_id` другим batch. ClickHouse не предоставляет unique constraints или транзакцию над несколькими таблицами, поэтому writer всё равно должен работать в одной активной реплике; registry является durable recovery/detection ledger, а не distributed lock.

## Storage и retention

Migrations `004+` создают checksum ledger, `ingest_batches_v2`, `privacy_suppressions_v2`, typed `common_events_v2`, `technical_events_v2`, `plays_events_v2`, `product_events_v2` и safe DataLens views. Все V2 data tables partitioned по `ingested_at`; event time не управляет partition lifecycle. Raw opaque keys не выдаются DataLens, suppressed scopes исключаются во view.

`RETENTION_V2_INGEST_BATCHES`, `RETENTION_V2_COMMON`, `RETENTION_V2_TECHNICAL`, `RETENTION_V2_PLAYS`, `RETENTION_V2_PRODUCT`, `RETENTION_V2_PRIVACY` принимают positive Go durations. Пустое значение оставляет `expires_at=NULL`. SQL TTL отсутствует и не должен добавляться до юридического утверждения сроков и отдельного operational review.

## Privacy flow

Privacy API сначала durable-записывает active suppression и `pending` request. Worker claim имеет пятиминутный lease; stale `processing` автоматически возвращается в обработку. `privacy_deletion_progress_v2` отдельно фиксирует `processing/retry/completed` для каждой таблицы, поэтому crash после части mutations не повторяет уже завершённые таблицы. Ошибки получают bounded exponential retry и только после лимита становятся `failed`. До distributed lock worker запускается в одной активной реплике.

OpenAPI: [public](openapi-v2.yaml), [internal](openapi-internal-v2.yaml).
