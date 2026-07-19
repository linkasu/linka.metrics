# Legacy telemetry import

`cmd/import-legacy` imports strict sanitized NDJSON through the same V2 ClickHouse store, batch ledger, record registry, suppression checks, and privacy deletion tables as live telemetry.

The importer never accepts email, raw Firebase/Yandex IDs as output fields, event payloads, text, URL, file paths, error messages, or media content. `source_subject` exists only in process memory and is immediately projected to `HMAC-SHA256(IMPORT_SUBJECT_HMAC_SECRET, product + source + subject)` before storage. Keep the import HMAC secret in Lockbox and retain it while imported rows remain subject to deletion.

## Input

One JSON object per line, grouped by `source`, `product`, and `source_subject`:

```json
{"source":"looks-sqlite","source_record_id":"42","source_subject":"00000000-0000-4000-8000-000000000000","product":"linka-looks","occurred_at":"2026-07-18T12:00:00Z","kind":"start","app_version":"3.2.8","platform":"windows"}
```

Registered sources are:

- `looks-sqlite` -> `linka-looks`;
- `firebase-pictures` -> `linka-pictures`;
- `firebase-type` -> `linka-type`;
- `yandex-metrika` -> `linka-site`;
- `tts-postgres` -> `linka-tts`.

`kind` must belong to the compile-time product registry. Unknown fields reject the entire line. Missing `app_build`, `os_version`, and `locale` become import-safe markers; imported pseudo-sessions are deterministic per subject and UTC day and must not be interpreted as original application sessions.

## Runbook

1. Produce NDJSON with a reviewed source exporter and sort it by source subject.
2. Run a dry validation with the production import HMAC secret and no ClickHouse credentials:

```bash
IMPORT_SUBJECT_HMAC_SECRET=... linka-import-legacy --dry-run --input export.ndjson
```

3. Compare exporter and importer event totals by product/kind/day.
4. Run the published immutable importer image in the private ClickHouse network with the `metric_writer` credentials. On a memory-constrained ClickHouse host, use `--batch-delay 500ms` so background part merges do not compete with sustained registry inserts.
5. Repeat the same input to verify every batch reports as replayed rather than duplicated.
6. Verify `datalens_product_v2` totals and perform a canary deletion for an isolated test subject.

Never copy a complete source database to the ClickHouse host when a streaming sanitized exporter is available. Destroy temporary NDJSON containing source pseudonyms immediately after count reconciliation.
