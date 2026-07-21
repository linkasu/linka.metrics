// Package fundraising defines the closed, identity-free donation ingestion contract.
package fundraising

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

const (
	SchemaVersion   = 1
	MaxBatchRecords = 500
	MaxBatchBytes   = 256 * 1024
	MaxJSONDepth    = 8
)

var ErrIdempotencyConflict = errors.New("idempotency key was already used with a different body")

type Batch struct {
	SchemaVersion int     `json:"schema_version"`
	BatchID       string  `json:"batch_id"`
	SentAt        string  `json:"sent_at"`
	Records       []Event `json:"records"`
}

// Event deliberately has no donor, payment-provider, URL, or free-text fields.
type Event struct {
	EventID             string  `json:"event_id"`
	OccurredAt          string  `json:"occurred_at"`
	Kind                string  `json:"kind"`
	Amount              *string `json:"amount,omitempty"`
	Currency            string  `json:"currency,omitempty"`
	Frequency           string  `json:"frequency"`
	AttributionSource   string  `json:"attribution_source"`
	AttributionCampaign *string `json:"attribution_campaign,omitempty"`
	FailureCode         *string `json:"failure_code,omitempty"`
}

type ValidatedEvent struct {
	Event
	OccurredAtTime time.Time
}

type ValidatedBatch struct {
	Batch
	SentAtTime time.Time
	Records    []ValidatedEvent
}

type IngestResult struct {
	Count    int
	Replayed bool
}

func BodySHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
