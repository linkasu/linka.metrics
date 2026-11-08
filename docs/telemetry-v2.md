# Telemetry V2

## Контракт

Публичный collector предоставляет `POST /v2/batches`, `POST /v2/privacy/requests` и service-only [`POST /v2/consents`](consent-v2.md). `POST /v2/tokens` является только staging compatibility route. Writer предоставляет соответствующие service-only endpoints под `/internal/v2/`.

Batch ограничен 512 KiB, 500 records, глубиной JSON 16 и одним typed stream: `common`, `technical`, `plays`, `product` или `outcome`. Decoder запрещает duplicate keys, unknown fields, trailing values и чрезмерную вложенность. Product, stream, kinds, states, pages, modes, locales, input methods, game categories, outcomes и game IDs проверяются по compile-time allowlists в `internal/product` и `internal/contract/v2`.

`app.locale` принимает `ru`, `ru-RU`, `en`, `en-US` и privacy-safe агрегат `other`. Неподдерживаемый системный locale клиент обязан преобразовать в `other`, не передавая исходное значение. `plays.input_method` сохраняет значения `mouse`, `touch`, `gaze`, `keyboard`, а также принимает `unknown`, пока в сессии ещё не было ввода, и `mixed`, если в игровой сессии использовались gaze и mouse. `plays.game_category` принимает `unknown`, если категория ещё недоступна для direct-route или early session; поле остаётся обязательным.

Для `plays.session_finished` допустимы клиентские итоговые outcomes `completed`, `incomplete`, `lost`, `draw`, `interrupted`, а также сохранённые для совместимости `cancelled` и `error`. Outcomes `success`, `mistake`, `hint`, `cancelled` у `plays.interaction` проверяются отдельным allowlist.

`product` содержит только базовые metadata и закрытое product-specific значение `kind`. `outcome` передаёт завершённое безопасное действие через закрытые enums `result`, `source`, `mode`, `count_bucket`, `duration_bucket` и `failure_code`. Compile-time outcome registry задаёт обязательные и разрешённые поля для каждого `product/kind`; отсутствующее обязательное или присутствующее неразрешённое поле отклоняется. `failure_code` остаётся optional, когда зарегистрирован. Произвольные `payload`, `attributes`, текстовые значения, URL, пути и пользовательские идентификаторы отсутствуют в контракте и отклоняются strict decoder.

`sent_at` допускается от семи дней в прошлом до пяти минут в будущем. `occurred_at` должен быть в диапазоне от 30 дней до `sent_at` до пяти минут после него. Timestamp обязан быть RFC3339 с точностью не выше миллисекунд и попадать в storage range `[2020-01-01, 2100-01-01)`. Счётчики, duration и ratios имеют явные границы.

## Scope и tokens

Production V2 token выдаёт LINKa Identity и подписывает Ed25519 active key. Collector загружает active и retiring keys из HTTPS JWKS и проверяет точные issuer, audience, product, scope, lifetime, `jti` и pairwise subject claims. Batch scope обязан точно совпасть с claims.

Для managed consent `scope.consent_epoch` batch и claim `consent_epoch` JWT — одинаковый положительный uint64. После первого consent control legacy batch без epoch запрещён. Новый allowed epoch не меняет subject и не возвращает историю закрытых эпох. Consent controls используют отдельный Identity service JWT с единственным scope `consent:write`; формат и порядок rollout описаны в [consent-v2.md](consent-v2.md).

Для server-to-server TTS предусмотрен отдельный opt-in ingress [`POST /internal/tts/outcomes`](tts-outcome-ingestion.md). Он использует отдельный HMAC keyring и фиксированный opaque subject, а не Identity; публичный `/v2/batches` продолжает требовать Identity token.

Identity публикует active и retiring JWT keys. Для ротации сначала добавьте новый key и сделайте его active, оставьте предыдущий retiring минимум на maximum token TTL плюс skew, затем удалите. Collector обновляет JWKS cache при неизвестном `kid`. Collector-to-writer service HMAC независимо поддерживает active/previous keys.

## Идемпотентность

`Idempotency-Key` обязателен, должен быть UUID и в точности равен `batch_id` или privacy `request_id`. Writer сначала резервирует `batch_id` и SHA-256 точного request body в ClickHouse ledger, а затем пишет records:

- новый ID и body: `202`, `replayed=false`;
- тот же ID и тот же body: `202`, `replayed=true`, без повторной логической вставки;
- тот же ID и другой body: `409 idempotency_conflict`.

Если процесс завершился после reservation или части records, тот же body продолжает запись и переводит ledger в `completed`; другой body конфликтует уже с reservation. Stable record IDs и `ReplacingMergeTree` делают повтор partial-write безопасным.

Writer сериализует check/write внутри процесса, а `record_registry_v2` отклоняет повторное использование `record_id` другим batch. ClickHouse не предоставляет unique constraints или транзакцию над несколькими таблицами, поэтому writer всё равно должен работать в одной активной реплике; registry является durable recovery/detection ledger, а не distributed lock.

## Storage и retention

Migrations `004+` создают checksum ledger, `ingest_batches_v2`, `privacy_suppressions_v2`, typed `common_events_v2`, `technical_events_v2`, `plays_events_v2`, `product_events_v2`, `product_outcomes_v2` и safe DataLens views. Все V2 data tables partitioned по `ingested_at`; event time не управляет partition lifecycle. Raw opaque keys не выдаются DataLens, suppressed scopes исключаются во view.

`RETENTION_V2_INGEST_BATCHES`, `RETENTION_V2_COMMON`, `RETENTION_V2_TECHNICAL`, `RETENTION_V2_PLAYS`, `RETENTION_V2_PRODUCT`, `RETENTION_V2_OUTCOME`, `RETENTION_V2_REGISTRY` задают 180 дней (`4320h`) для новых событий, batch ledger и registry. `RETENTION_V2_PRIVACY` и `RETENTION_V2_DELETION_PROGRESS` задают 1095 дней (`26280h`) для новых privacy-записей. После migration 019 все active suppressions, включая delete, и consent safety ledger бессрочны: их удаление могло бы позволить запоздалому запросу снова включить сбор. Все длительности обязательны в production и читаются writer, privacy worker и importer одним parser.

Migration `017_v2_retention.sql` включает TTL и expiry-фильтры DataLens; миграции 001–016 не изменяются. Исторические `expires_at IS NULL` не заполняются и не удаляются TTL. Privacy TTL не удаляет активные opt-out, незавершённые запросы и незавершённый deletion progress. Физическое TTL-удаление ленивое; views скрывают истёкшие события сразу. Backup имеет отдельную политику и не заменяет TTL.

## Privacy flow

Privacy `requested_at` должен быть RFC3339 в storage range `[2020-01-01, 2100-01-01)`; при разборе время нормализуется до миллисекунд без изменения исходного request body. Будущее ограничено пятью минутами. Для `delete` сохраняется предел 24 часа в прошлом; для `opt_out` ограничения по давности нет, чтобы durable delivery могла повторять исходный запрос после длительного сбоя без подмены timestamps. Повторная доставка требует свежего Identity JWT с `privacy:write`, точного совпадения scope и collector-to-writer HMAC; UUID, action, strict JSON и идемпотентность проверяются как прежде.

Privacy API сначала durable-записывает active suppression и `pending` request. Worker claim имеет пятиминутный lease; stale `processing` автоматически возвращается в обработку. `privacy_deletion_progress_v2` отдельно фиксирует `processing/retry/completed` для каждой таблицы, поэтому crash после части mutations не повторяет уже завершённые таблицы. Ошибки получают bounded exponential retry и только после лимита становятся `failed`. До distributed lock worker запускается в одной активной реплике.

OpenAPI: [public](openapi-v2.yaml), [internal](openapi-internal-v2.yaml).
