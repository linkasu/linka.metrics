package v2

import (
	"errors"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/product"
)

const (
	SchemaVersion   = 2
	MaxBatchRecords = 500
	MaxBatchBytes   = 512 * 1024
	MaxJSONDepth    = 16
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key was already used with a different body")
	ErrDuplicateRecord     = errors.New("record ID was already used by another batch")
	ErrSuppressed          = errors.New("telemetry scope is suppressed")
)

type Scope struct {
	Product    product.ID `json:"product"`
	SubjectKey string     `json:"subject_key"`
	PersonKey  *string    `json:"person_key,omitempty"`
	OrgKey     *string    `json:"org_key,omitempty"`
}

type BatchHeader struct {
	SchemaVersion int            `json:"schema_version"`
	BatchID       string         `json:"batch_id"`
	Scope         Scope          `json:"scope"`
	Stream        product.Stream `json:"stream"`
	SentAt        string         `json:"sent_at"`
}

type AppMetadata struct {
	Version   string `json:"version"`
	Build     string `json:"build"`
	Platform  string `json:"platform"`
	OSVersion string `json:"os_version"`
	Locale    string `json:"locale"`
}

type CommonRecord struct {
	RecordID     string      `json:"record_id"`
	OccurredAt   string      `json:"occurred_at"`
	Kind         string      `json:"kind"`
	AppSessionID string      `json:"app_session_id"`
	App          AppMetadata `json:"app"`
	Page         *string     `json:"page,omitempty"`
	Mode         *string     `json:"mode,omitempty"`
}

type TechnicalRecord struct {
	RecordID         string      `json:"record_id"`
	OccurredAt       string      `json:"occurred_at"`
	Kind             string      `json:"kind"`
	AppSessionID     string      `json:"app_session_id"`
	App              AppMetadata `json:"app"`
	Component        string      `json:"component"`
	State            *string     `json:"state,omitempty"`
	ErrorFingerprint *string     `json:"error_fingerprint,omitempty"`
	DroppedCount     *uint64     `json:"dropped_count,omitempty"`
	DropReason       *string     `json:"drop_reason,omitempty"`
}

type PlaysRecord struct {
	RecordID       string      `json:"record_id"`
	OccurredAt     string      `json:"occurred_at"`
	Kind           string      `json:"kind"`
	AppSessionID   string      `json:"app_session_id"`
	GameSessionID  string      `json:"game_session_id"`
	App            AppMetadata `json:"app"`
	GameID         string      `json:"game_id"`
	GameCategory   string      `json:"game_category"`
	InputMethod    string      `json:"input_method"`
	LevelIndex     *uint32     `json:"level_index,omitempty"`
	Outcome        *string     `json:"outcome,omitempty"`
	DurationMS     *uint64     `json:"duration_ms,omitempty"`
	SuccessCount   *uint32     `json:"success_count,omitempty"`
	MistakeCount   *uint32     `json:"mistake_count,omitempty"`
	HintCount      *uint32     `json:"hint_count,omitempty"`
	ValidGazeRatio *float64    `json:"valid_gaze_ratio,omitempty"`
}

type ProductRecord struct {
	RecordID     string      `json:"record_id"`
	OccurredAt   string      `json:"occurred_at"`
	Kind         string      `json:"kind"`
	AppSessionID string      `json:"app_session_id"`
	App          AppMetadata `json:"app"`
}

// OutcomeRecord intentionally uses only closed enums and coarse buckets. It
// captures a completed product action without accepting communication content.
type OutcomeRecord struct {
	RecordID       string      `json:"record_id"`
	OccurredAt     string      `json:"occurred_at"`
	Kind           string      `json:"kind"`
	AppSessionID   string      `json:"app_session_id"`
	App            AppMetadata `json:"app"`
	Result         *string     `json:"result,omitempty"`
	Source         *string     `json:"source,omitempty"`
	Mode           *string     `json:"mode,omitempty"`
	CountBucket    *string     `json:"count_bucket,omitempty"`
	DurationBucket *string     `json:"duration_bucket,omitempty"`
	FailureCode    *string     `json:"failure_code,omitempty"`
}

type CommonBatch struct {
	BatchHeader
	Records []CommonRecord `json:"records"`
}

type TechnicalBatch struct {
	BatchHeader
	Records []TechnicalRecord `json:"records"`
}

type PlaysBatch struct {
	BatchHeader
	Records []PlaysRecord `json:"records"`
}

type ProductBatch struct {
	BatchHeader
	Records []ProductRecord `json:"records"`
}

type OutcomeBatch struct {
	BatchHeader
	Records []OutcomeRecord `json:"records"`
}

type ValidatedCommonRecord struct {
	CommonRecord
	OccurredAtTime time.Time
}

type ValidatedTechnicalRecord struct {
	TechnicalRecord
	OccurredAtTime time.Time
}

type ValidatedPlaysRecord struct {
	PlaysRecord
	OccurredAtTime time.Time
}

type ValidatedProductRecord struct {
	ProductRecord
	OccurredAtTime time.Time
}

type ValidatedOutcomeRecord struct {
	OutcomeRecord
	OccurredAtTime time.Time
}

type ValidatedBatch struct {
	Header           BatchHeader
	SentAtTime       time.Time
	ProductKey       string
	CommonRecords    []ValidatedCommonRecord
	TechnicalRecords []ValidatedTechnicalRecord
	PlaysRecords     []ValidatedPlaysRecord
	ProductRecords   []ValidatedProductRecord
	OutcomeRecords   []ValidatedOutcomeRecord
}

func (b ValidatedBatch) RecordCount() int {
	return len(b.CommonRecords) + len(b.TechnicalRecords) + len(b.PlaysRecords) + len(b.ProductRecords) + len(b.OutcomeRecords)
}

type IngestResult struct {
	Replayed bool
	Count    int
}

type PrivacyAction string

const (
	PrivacyOptOut PrivacyAction = "opt_out"
	PrivacyDelete PrivacyAction = "delete"
)

type PrivacyRequest struct {
	SchemaVersion int           `json:"schema_version"`
	RequestID     string        `json:"request_id"`
	Scope         Scope         `json:"scope"`
	Action        PrivacyAction `json:"action"`
	RequestedAt   string        `json:"requested_at"`
}

type ValidatedPrivacyRequest struct {
	PrivacyRequest
	ProductKey      string
	RequestedAtTime time.Time
}

type PrivacyResult struct {
	Replayed bool
	Status   string
}
