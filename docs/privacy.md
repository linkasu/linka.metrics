# Политика приватности

Версия: `2026-07-19-v3`. Дата вступления в силу: 19 июля 2026 года.

## Назначение

LINKa Metrics собирает только предусмотренные закрытыми контрактами v1 и v2 обезличенные технические, продуктовые и игровые события LINKa Plays, Looks, Pictures, Type, Paperboard, сайта linka.su и TTS-сервиса. Неизвестные события, поля, дублированные JSON-ключи и значения вне product-specific allowlist отклоняются до записи.

## Что разрешено

- случайные UUID сессий и product/audience-separated pairwise keys установки;
- версия приложения, номер сборки, платформа, версия ОС и locale;
- безопасные перечисления страниц, режимов, игр, уровней, типов целей, способов ввода и product-specific event kinds;
- состояния Tobii и updater;
- агрегированные длительности и счётчики успехов, ошибок и подсказок;
- стабильный заранее вычисленный fingerprint ошибки и безопасное имя компонента.

## Что запрещено

Сервис не принимает и не хранит имя, контакты, raw account ID, аппаратный идентификатор, Firebase/Yandex visitor ID, IP-адрес в прикладных логах, произвольный текст, фразу, ожидаемый или фактический ответ, `targetId`, координаты `x/y`, траекторию взгляда или указателя, путь файла/URL, сообщение и stack trace ошибки. Такие ключи не входят ни в один сетевой тип и приводят к отказу всего batch.

Значения строк ограничены коротким безопасным алфавитом без пробелов и `/`, поэтому разрешённые ключи нельзя использовать как канал для произвольного текста или пути.

## Идентификатор установки

Collector генерирует UUID v4 криптографическим генератором и выдаёт bounded-lifetime stateless token `v1`, содержащий UUID, время выдачи и HMAC-SHA256. Token можно обновить без смены UUID. Production v2 принимает короткоживущие Ed25519 JWT от LINKa Identity через rotating JWKS; `subject_key`, `person_key` и `org_key` являются product/audience-separated 64-символьными pairwise HMAC keys. Legacy `/v2/tokens` доступен только при явном staging flag. Сравнение HMAC выполняется в постоянном времени.

## Передача и хранение

Collector проверяет bearer token и весь batch, затем подписывает точное тело запроса отдельным HMAC-секретом writer. V2 service HMAC связывает версию, HTTP method, path, caller, key id, request/idempotency id, timestamp и SHA-256 точного тела; writer принимает active и один previous key при ротации. Writer проверяет подпись до разбора и вставки. В рабочей конфигурации внешние соединения завершаются TLS.

В таблицах ClickHouse нет активного TTL. V2 содержит nullable `expires_at`; он заполняется только после явной настройки утверждённых юридической функцией сроков. Настройка срока сама по себе не включает SQL `TTL`: удаление по сроку требует отдельного согласованного operational change. `ReplacingMergeTree`, stable record IDs, batch ledger и DataLens views с `FINAL` обеспечивают безопасный replay/read path.

## Opt-out и удаление

`POST /v2/privacy/requests` принимает `opt_out` или `delete` только в scope предъявленного Identity JWT. Ответ `202` означает, что active suppression и request надёжно записаны, но не означает завершение удаления. Worker использует lease, bounded retry и отдельный durable progress для каждой ClickHouse table; `completed` появляется только после всех synchronous mutations. Повторный idempotent POST возвращает текущий status и служит receipt polling для Identity.

`POST /v1/privacy/requests` удаляет `events` и `session_summaries` только для installation из предъявленного V1 token и блокирует последующую загрузку этой installation. Удаление v1 по person/org невозможно без внешнего сопоставления: v1 хранит только случайный installation UUID.

Технический контакт: ivan@aacidov.ru.
