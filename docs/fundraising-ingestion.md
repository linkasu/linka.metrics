# Fundraising Ingestion V1

WordPress sends donation accounting batches to the public collector route `POST /internal/fundraising/batches`. This route is service-to-service despite its public network exposure: it accepts `application/json` only and requires `LINKA-HMAC-V2` headers signed by `DONATION_INGEST_HMAC_ACTIVE_*` or a configured previous key. `X-Linka-Caller` must be exactly `nko-donations`.

The collector does not read `Authorization`, does not invoke LINKa Identity, and cannot accept browser or other client-side telemetry credentials. After authenticating and strictly validating the unchanged body, it relays it to `POST /internal/fundraising/batches` on writer using the independent collector-to-writer `SERVICE_HMAC_*` keyring and caller `collector`.

## Request Contract

`Idempotency-Key` and `X-Linka-Request-ID` must both exactly equal `batch_id`. `schema_version` is always `1`. Batches have at most 500 records, are at most 256 KiB, and use RFC3339 timestamps with millisecond precision.

```json
{
  "schema_version": 1,
  "batch_id": "10000000-0000-4000-8000-000000000001",
  "sent_at": "2026-07-21T12:00:00Z",
  "records": [
    {
      "event_id": "20000000-0000-4000-8000-000000000002",
      "occurred_at": "2026-07-21T11:59:00Z",
      "kind": "payment_succeeded",
      "amount": "1200.00",
      "currency": "RUB",
      "frequency": "one_time",
      "attribution_source": "utm",
      "attribution_campaign": "summer_2026"
    }
  ]
}
```

Allowed `kind` values are `payment_created`, `payment_succeeded`, `payment_cancelled`, `refund_recorded`, `recurring_activated`, `recurring_charge_succeeded`, `recurring_charge_failed`, and `subscription_cancelled`. Monetary kinds require a positive string Decimal(18,2) `amount` and `currency: RUB`; activation and cancellation kinds forbid both fields. `frequency` is `one_time` or `monthly`; `attribution_source` is `direct`, `organic`, `utm`, `qr`, or `unknown`. `attribution_campaign` is required and nullable: use `null` or a 32-character lowercase code matching `^[a-z][a-z0-9_-]{0,31}$`. `failure_code` is required only for `recurring_charge_failed` and limited to `declined`, `insufficient_funds`, `expired`, `temporary_unavailable`, `processing_error`, or `unknown`.

The strict decoder rejects unknown and duplicate fields. `event_id` is an ingestion UUID, not a payment or provider identifier. There are intentionally no fields for a donor name, email, phone, address, payment/provider identifier, subscription identifier, URL, IP address, free text, or arbitrary metadata.

## Idempotency And Privacy

Writer reserves `batch_id` with the SHA-256 of the exact request body before events are inserted, then marks the batch completed. A same-body retry returns `202` with `replayed: true`; reuse with another body returns `409 idempotency_conflict`. A retry after a partial write resumes the reserved batch safely.

`fundraising_events_v1` is an identity-free financial accounting table, not product telemetry. It stores only the closed aggregate dimensions in this contract and is explicitly excluded from product telemetry deletion mutations and privacy suppressions. DataLens receives `datalens_fundraising_v1` without batch/event identifiers and the pre-aggregated `fundraising_finance_daily_v1` finance view.
