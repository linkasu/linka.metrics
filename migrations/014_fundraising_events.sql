CREATE TABLE IF NOT EXISTS linka_metric.fundraising_ingest_batches_v1
(
    batch_id UUID,
    body_sha256 FixedString(64),
    record_count UInt16,
    sent_at DateTime64(3, 'UTC'),
    ingested_at DateTime64(3, 'UTC'),
    status LowCardinality(String)
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMM(ingested_at)
ORDER BY batch_id;

CREATE TABLE IF NOT EXISTS linka_metric.fundraising_events_v1
(
    batch_id UUID,
    event_id UUID,
    occurred_at DateTime64(3, 'UTC'),
    kind LowCardinality(String),
    amount Nullable(Decimal(18, 2)),
    currency LowCardinality(Nullable(FixedString(3))),
    frequency LowCardinality(String),
    attribution_source LowCardinality(String),
    attribution_campaign LowCardinality(Nullable(String)),
    failure_code LowCardinality(Nullable(String)),
    ingested_at DateTime64(3, 'UTC')
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMM(ingested_at)
ORDER BY (batch_id, event_id);

CREATE VIEW IF NOT EXISTS linka_metric.datalens_fundraising_v1
SQL SECURITY DEFINER
AS
SELECT
    toDate(occurred_at, 'UTC') AS occurred_date,
    kind,
    amount,
    currency,
    frequency,
    attribution_source,
    attribution_campaign,
    failure_code
FROM linka_metric.fundraising_events_v1 FINAL;

CREATE VIEW IF NOT EXISTS linka_metric.fundraising_finance_daily_v1
SQL SECURITY DEFINER
AS
SELECT
    toDate(occurred_at, 'UTC') AS occurred_date,
    frequency,
    attribution_source,
    attribution_campaign,
    currency,
    countIf(kind IN ('payment_succeeded', 'recurring_charge_succeeded')) AS successful_payment_count,
    countIf(kind = 'refund_recorded') AS refund_count,
    sumIf(ifNull(amount, toDecimal64(0, 2)), kind IN ('payment_succeeded', 'recurring_charge_succeeded')) AS gross_amount,
    sumIf(ifNull(amount, toDecimal64(0, 2)), kind = 'refund_recorded') AS refunded_amount,
    gross_amount - refunded_amount AS net_amount
FROM linka_metric.fundraising_events_v1 FINAL
GROUP BY occurred_date, frequency, attribution_source, attribution_campaign, currency;
