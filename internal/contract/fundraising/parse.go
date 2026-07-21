package fundraising

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/linkasu/linka.plays-metric/internal/jsonstrict"
)

var (
	amountPattern   = regexp.MustCompile(`^(?:0|[1-9][0-9]{0,15})\.[0-9]{2}$`)
	campaignPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

func ParseBatch(data []byte, now time.Time) (ValidatedBatch, error) {
	if len(data) > MaxBatchBytes {
		return ValidatedBatch{}, errors.New("batch exceeds maximum size")
	}
	var raw map[string]json.RawMessage
	if err := jsonstrict.DecodeObject(data, &raw, MaxJSONDepth); err != nil {
		return ValidatedBatch{}, fmt.Errorf("decode fundraising batch: %w", err)
	}
	var rawRecords []map[string]json.RawMessage
	if err := json.Unmarshal(raw["records"], &rawRecords); err != nil {
		return ValidatedBatch{}, errors.New("records must be an array")
	}
	for index, record := range rawRecords {
		if _, ok := record["attribution_campaign"]; !ok {
			return ValidatedBatch{}, fmt.Errorf("records[%d]: attribution_campaign is required and may be null", index)
		}
	}
	var batch Batch
	if err := jsonstrict.DecodeObject(data, &batch, MaxJSONDepth); err != nil {
		return ValidatedBatch{}, fmt.Errorf("decode fundraising batch: %w", err)
	}
	if batch.SchemaVersion != SchemaVersion {
		return ValidatedBatch{}, errors.New("unsupported schema_version")
	}
	if err := validateUUID(batch.BatchID); err != nil {
		return ValidatedBatch{}, fmt.Errorf("batch_id: %w", err)
	}
	sentAt, err := parseTimestamp(batch.SentAt)
	if err != nil {
		return ValidatedBatch{}, fmt.Errorf("sent_at: %w", err)
	}
	if sentAt.Before(now.Add(-7*24*time.Hour)) || sentAt.After(now.Add(5*time.Minute)) {
		return ValidatedBatch{}, errors.New("sent_at is outside the allowed range")
	}
	if len(batch.Records) < 1 || len(batch.Records) > MaxBatchRecords {
		return ValidatedBatch{}, fmt.Errorf("batch must contain between 1 and %d records", MaxBatchRecords)
	}
	validated := ValidatedBatch{Batch: batch, SentAtTime: sentAt, Records: make([]ValidatedEvent, 0, len(batch.Records))}
	seen := make(map[string]struct{}, len(batch.Records))
	for index, event := range batch.Records {
		if _, duplicate := seen[event.EventID]; duplicate {
			return ValidatedBatch{}, fmt.Errorf("records[%d]: duplicate event_id in batch", index)
		}
		seen[event.EventID] = struct{}{}
		parsed, err := validateEvent(event, sentAt)
		if err != nil {
			return ValidatedBatch{}, fmt.Errorf("records[%d]: %w", index, err)
		}
		validated.Records = append(validated.Records, parsed)
	}
	return validated, nil
}

func ValidateIdempotencyKey(headerValue, batchID string) error {
	if headerValue == "" || headerValue != batchID {
		return errors.New("Idempotency-Key must equal batch_id")
	}
	return validateUUID(headerValue)
}

func validateEvent(event Event, sentAt time.Time) (ValidatedEvent, error) {
	if err := validateUUID(event.EventID); err != nil {
		return ValidatedEvent{}, fmt.Errorf("event_id: %w", err)
	}
	occurredAt, err := parseTimestamp(event.OccurredAt)
	if err != nil {
		return ValidatedEvent{}, fmt.Errorf("occurred_at: %w", err)
	}
	if occurredAt.Before(sentAt.Add(-30*24*time.Hour)) || occurredAt.After(sentAt.Add(5*time.Minute)) {
		return ValidatedEvent{}, errors.New("occurred_at is outside the allowed batch range")
	}
	if !oneOf(event.Kind, "payment_created", "payment_succeeded", "payment_cancelled", "refund_recorded", "recurring_activated", "recurring_charge_succeeded", "recurring_charge_failed", "subscription_cancelled") {
		return ValidatedEvent{}, errors.New("unknown fundraising kind")
	}
	if !oneOf(event.Frequency, "one_time", "monthly") {
		return ValidatedEvent{}, errors.New("unknown frequency")
	}
	if !oneOf(event.AttributionSource, "direct", "organic", "utm", "qr", "unknown") {
		return ValidatedEvent{}, errors.New("unknown attribution_source")
	}
	if event.AttributionCampaign != nil && !campaignPattern.MatchString(*event.AttributionCampaign) {
		return ValidatedEvent{}, errors.New("attribution_campaign must be a bounded safe campaign code")
	}
	moneyEvent := oneOf(event.Kind, "payment_created", "payment_succeeded", "payment_cancelled", "refund_recorded", "recurring_charge_succeeded", "recurring_charge_failed")
	if moneyEvent {
		if event.Amount == nil || !amountPattern.MatchString(*event.Amount) || *event.Amount == "0.00" || event.Currency != "RUB" {
			return ValidatedEvent{}, errors.New("monetary event requires a positive Decimal(18,2) RUB amount")
		}
	} else if event.Amount != nil || event.Currency != "" {
		return ValidatedEvent{}, errors.New("non-monetary event cannot contain amount or currency")
	}
	if event.Kind == "recurring_charge_failed" {
		if event.FailureCode == nil || !oneOf(*event.FailureCode, "declined", "insufficient_funds", "expired", "temporary_unavailable", "processing_error", "unknown") {
			return ValidatedEvent{}, errors.New("recurring_charge_failed requires an allowed failure_code")
		}
	} else if event.FailureCode != nil {
		return ValidatedEvent{}, errors.New("failure_code is only allowed for recurring_charge_failed")
	}
	return ValidatedEvent{Event: event, OccurredAtTime: occurredAt}, nil
}

func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("must be RFC3339")
	}
	if parsed.Nanosecond()%int(time.Millisecond) != 0 {
		return time.Time{}, errors.New("precision must not exceed milliseconds")
	}
	parsed = parsed.UTC()
	if parsed.Before(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) || !parsed.Before(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)) {
		return time.Time{}, errors.New("timestamp is outside the storage range")
	}
	return parsed, nil
}

func validateUUID(value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return errors.New("invalid UUID")
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
