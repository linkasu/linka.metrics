# API v1

Все JSON endpoints требуют `Content-Type: application/json`. Неизвестные JSON-поля и данные после корневого объекта запрещены. Максимальный размер batch — 512 KiB, максимум 500 записей суммарно для `events` и `session_summaries`.

## Collector

### `GET /healthz`

Возвращает `200` при готовности HTTP-процесса.

### `GET /privacy`

Возвращает русский текст политики приватности как `text/plain; charset=utf-8`.

### `POST /v1/installations`

Тело должно быть пустым JSON-объектом `{}`. Ответ `201`:

```json
{
  "installation_id": "20000000-0000-4000-8000-000000000002",
  "issued_at": "2026-07-18T12:00:00Z",
  "token": "v1....",
  "token_version": "v1"
}
```

### `POST /v1/events`

Требует `Authorization: Bearer <installation-token>`. Каждый `installation_id` в batch должен совпадать с token. Ответ `202` возвращается только после успешного ответа writer и содержит количество принятых событий и summaries.

```json
{
  "schema_version": 1,
  "events": [
    {
      "event_id": "10000000-0000-4000-8000-000000000001",
      "event_name": "target_clicked",
      "occurred_at": "2026-07-18T12:00:00.123Z",
      "installation_id": "20000000-0000-4000-8000-000000000002",
      "app_session_id": "30000000-0000-4000-8000-000000000003",
      "game_session_id": "40000000-0000-4000-8000-000000000004",
      "app": {
        "version": "1.2.3",
        "build": "42",
        "platform": "linux",
        "os_version": "6.8",
        "locale": "ru-RU"
      },
      "properties": {
        "game_id": "letters",
        "level_index": 2,
		"target_kind": "interactive",
		"input_method": "gaze",
		"elapsed_ms": 750
      }
    }
  ]
}
```

Время задаётся в RFC3339 с точностью не выше миллисекунд. UUID обязательны. `game_session_id` обязателен для игровых событий.

## События и properties

| event_name | Разрешённые properties |
| --- | --- |
| `installation_created`, `app_started`, `app_backgrounded`, `app_foregrounded`, `app_closed` | пустой объект |
| `page_viewed` | `page` |
| `mode_changed` | `mode` |
| `settings_changed` | `setting_key` и ровно одно из `enabled` (bool) или `number` |
| `game_session_started`, `game_session_paused`, `game_session_resumed` | `game_id`, optional `mode`, `game_category` |
| `game_session_finished`, `game_session_interrupted` | `game_id`, `result`/`reason` |
| `level_entered`, `level_cancelled`, `level_clicked` | `game_id`, `level_index` |
| `target_entered`, `target_cancelled`, `target_clicked` | `game_id`, `level_index`, `target_kind`, `input_method`, optional `elapsed_ms`, `reason` |
| `success`, `mistake` | `game_id`, `level_index`, optional `target_kind`, `input_method`, `response_ms` |
| `hint_used` | `game_id`, `level_index`, `hint_kind` |
| `difficulty_changed` | `game_id`, `difficulty` от 1 до 10 |
| `tobii_state_changed` | `state` из фиксированного enum |
| `updater_state_changed` | `state` из фиксированного enum, optional `version` |
| `error` | `fingerprint`, `component` |
| `queue_dropped` | `dropped_count`, `reason` из фиксированного enum |

## Session summary

Summary хранится отдельно от событий для прямого использования DataLens:

```json
{
  "session_id": "40000000-0000-4000-8000-000000000004",
  "session_type": "game",
  "installation_id": "20000000-0000-4000-8000-000000000002",
  "app_session_id": "30000000-0000-4000-8000-000000000003",
  "game_session_id": "40000000-0000-4000-8000-000000000004",
  "game_id": "letters",
  "started_at": "2026-07-18T12:00:00.000Z",
  "ended_at": "2026-07-18T12:05:00.000Z",
  "duration_ms": 300000,
	"paused_ms": 10000,
	"menu_mode": "self",
	"game_category": "language-aac",
	"input_method": "gaze",
	"finish_reason": "game-complete",
	"steps_completed": 14,
	"max_steps": 14,
  "success_count": 12,
  "mistake_count": 2,
  "hint_count": 1,
	"target_cancel_count": 3,
	"gaze_lost_count": 1,
	"difficulty_changes": 0,
	"gaze_sample_count": 1200,
	"mouse_sample_count": 0,
	"valid_gaze_ratio": 0.96,
	"mean_dwell_ms": 820,
	"configured_dwell_ms": 750,
  "result": "completed",
  "app": {
    "version": "1.2.3",
    "build": "42",
    "platform": "linux",
    "os_version": "6.8",
    "locale": "ru-RU"
  }
}
```

Для `session_type=app` поля `game_session_id` и `game_id` отсутствуют, а `session_id` равен `app_session_id`. Для `game` `session_id` равен `game_session_id`.

## Writer

`POST /internal/v1/events` предназначен только для collector. Заголовки:

- `X-Linka-Timestamp`: Unix seconds;
- `X-Linka-Body-SHA256`: lowercase hex SHA-256 точного тела;
- `X-Linka-Signature`: base64url HMAC-SHA256 от `<timestamp>\n<body-sha>`.

Допустимый clock skew — пять минут. `GET /healthz` проверяет ClickHouse с таймаутом одна секунда.
