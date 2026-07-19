package v2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/linkasu/linka.plays-metric/internal/jsonstrict"
	"github.com/linkasu/linka.plays-metric/internal/product"
)

var safeValuePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]{0,95}$`)
var opaqueKeyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var fingerprintPattern = regexp.MustCompile(`^(?:[a-f0-9]{16,96}|sha256:[a-f0-9]{64})$`)

type batchEnvelope struct {
	BatchHeader
	Records json.RawMessage `json:"records"`
}

func ParseBatch(data []byte, now time.Time) (ValidatedBatch, error) {
	if len(data) > MaxBatchBytes {
		return ValidatedBatch{}, errors.New("batch exceeds maximum size")
	}
	var envelope batchEnvelope
	if err := jsonstrict.DecodeObject(data, &envelope, MaxJSONDepth); err != nil {
		return ValidatedBatch{}, fmt.Errorf("decode batch envelope: %w", err)
	}
	validated, spec, err := validateHeader(envelope.BatchHeader, now)
	if err != nil {
		return ValidatedBatch{}, err
	}

	switch envelope.Stream {
	case product.StreamCommon:
		var batch CommonBatch
		if err := jsonstrict.DecodeObject(data, &batch, MaxJSONDepth); err != nil {
			return ValidatedBatch{}, fmt.Errorf("decode common batch: %w", err)
		}
		if err := validateRecordCount(len(batch.Records)); err != nil {
			return ValidatedBatch{}, err
		}
		validated.CommonRecords = make([]ValidatedCommonRecord, 0, len(batch.Records))
		for index, record := range batch.Records {
			parsed, err := validateCommonRecord(record, validated.SentAtTime)
			if err != nil {
				return ValidatedBatch{}, fmt.Errorf("records[%d]: %w", index, err)
			}
			validated.CommonRecords = append(validated.CommonRecords, parsed)
		}
	case product.StreamTechnical:
		var batch TechnicalBatch
		if err := jsonstrict.DecodeObject(data, &batch, MaxJSONDepth); err != nil {
			return ValidatedBatch{}, fmt.Errorf("decode technical batch: %w", err)
		}
		if err := validateRecordCount(len(batch.Records)); err != nil {
			return ValidatedBatch{}, err
		}
		validated.TechnicalRecords = make([]ValidatedTechnicalRecord, 0, len(batch.Records))
		for index, record := range batch.Records {
			parsed, err := validateTechnicalRecord(record, validated.SentAtTime)
			if err != nil {
				return ValidatedBatch{}, fmt.Errorf("records[%d]: %w", index, err)
			}
			validated.TechnicalRecords = append(validated.TechnicalRecords, parsed)
		}
	case product.StreamPlays:
		var batch PlaysBatch
		if err := jsonstrict.DecodeObject(data, &batch, MaxJSONDepth); err != nil {
			return ValidatedBatch{}, fmt.Errorf("decode plays batch: %w", err)
		}
		if err := validateRecordCount(len(batch.Records)); err != nil {
			return ValidatedBatch{}, err
		}
		validated.PlaysRecords = make([]ValidatedPlaysRecord, 0, len(batch.Records))
		for index, record := range batch.Records {
			parsed, err := validatePlaysRecord(record, validated.SentAtTime, spec)
			if err != nil {
				return ValidatedBatch{}, fmt.Errorf("records[%d]: %w", index, err)
			}
			validated.PlaysRecords = append(validated.PlaysRecords, parsed)
		}
	case product.StreamProduct:
		var batch ProductBatch
		if err := jsonstrict.DecodeObject(data, &batch, MaxJSONDepth); err != nil {
			return ValidatedBatch{}, fmt.Errorf("decode product batch: %w", err)
		}
		if err := validateRecordCount(len(batch.Records)); err != nil {
			return ValidatedBatch{}, err
		}
		validated.ProductRecords = make([]ValidatedProductRecord, 0, len(batch.Records))
		for index, record := range batch.Records {
			parsed, err := validateProductRecord(record, validated.SentAtTime, spec)
			if err != nil {
				return ValidatedBatch{}, fmt.Errorf("records[%d]: %w", index, err)
			}
			validated.ProductRecords = append(validated.ProductRecords, parsed)
		}
	default:
		return ValidatedBatch{}, errors.New("unknown stream")
	}
	if err := validateUniqueRecordIDs(validated); err != nil {
		return ValidatedBatch{}, err
	}
	return validated, nil
}

func validateUniqueRecordIDs(batch ValidatedBatch) error {
	seen := make(map[string]struct{}, batch.RecordCount())
	for _, record := range batch.CommonRecords {
		if _, exists := seen[record.RecordID]; exists {
			return errors.New("duplicate record_id in batch")
		}
		seen[record.RecordID] = struct{}{}
	}
	for _, record := range batch.TechnicalRecords {
		if _, exists := seen[record.RecordID]; exists {
			return errors.New("duplicate record_id in batch")
		}
		seen[record.RecordID] = struct{}{}
	}
	for _, record := range batch.PlaysRecords {
		if _, exists := seen[record.RecordID]; exists {
			return errors.New("duplicate record_id in batch")
		}
		seen[record.RecordID] = struct{}{}
	}
	for _, record := range batch.ProductRecords {
		if _, exists := seen[record.RecordID]; exists {
			return errors.New("duplicate record_id in batch")
		}
		seen[record.RecordID] = struct{}{}
	}
	return nil
}

func ParsePrivacyRequest(data []byte, now time.Time) (ValidatedPrivacyRequest, error) {
	if len(data) > 16*1024 {
		return ValidatedPrivacyRequest{}, errors.New("privacy request exceeds maximum size")
	}
	var request PrivacyRequest
	if err := jsonstrict.DecodeObject(data, &request, MaxJSONDepth); err != nil {
		return ValidatedPrivacyRequest{}, fmt.Errorf("decode privacy request: %w", err)
	}
	if request.SchemaVersion != SchemaVersion {
		return ValidatedPrivacyRequest{}, errors.New("unsupported schema_version")
	}
	if err := validateUUID(request.RequestID); err != nil {
		return ValidatedPrivacyRequest{}, fmt.Errorf("request_id: %w", err)
	}
	spec, err := validateScope(request.Scope)
	if err != nil {
		return ValidatedPrivacyRequest{}, err
	}
	if request.Action != PrivacyOptOut && request.Action != PrivacyDelete {
		return ValidatedPrivacyRequest{}, errors.New("unknown privacy action")
	}
	requestedAt, err := parsePrivacyTimestamp(request.RequestedAt)
	if err != nil {
		return ValidatedPrivacyRequest{}, fmt.Errorf("requested_at: %w", err)
	}
	if requestedAt.Before(now.Add(-24*time.Hour)) || requestedAt.After(now.Add(5*time.Minute)) {
		return ValidatedPrivacyRequest{}, errors.New("requested_at is outside the allowed range")
	}
	return ValidatedPrivacyRequest{PrivacyRequest: request, ProductKey: spec.OpaqueKey, RequestedAtTime: requestedAt}, nil
}

func BodySHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func ValidateIdempotencyKey(headerValue, bodyID string) error {
	if headerValue == "" || headerValue != bodyID {
		return errors.New("Idempotency-Key must equal the body ID")
	}
	return validateUUID(headerValue)
}

func validateHeader(header BatchHeader, now time.Time) (ValidatedBatch, product.Spec, error) {
	if header.SchemaVersion != SchemaVersion {
		return ValidatedBatch{}, product.Spec{}, errors.New("unsupported schema_version")
	}
	if err := validateUUID(header.BatchID); err != nil {
		return ValidatedBatch{}, product.Spec{}, fmt.Errorf("batch_id: %w", err)
	}
	spec, err := validateScope(header.Scope)
	if err != nil {
		return ValidatedBatch{}, product.Spec{}, err
	}
	if !spec.AllowsStream(header.Stream) {
		return ValidatedBatch{}, product.Spec{}, errors.New("stream is not registered for product")
	}
	sentAt, err := parseTimestamp(header.SentAt)
	if err != nil {
		return ValidatedBatch{}, product.Spec{}, fmt.Errorf("sent_at: %w", err)
	}
	if sentAt.Before(now.Add(-7*24*time.Hour)) || sentAt.After(now.Add(5*time.Minute)) {
		return ValidatedBatch{}, product.Spec{}, errors.New("sent_at is outside the allowed range")
	}
	return ValidatedBatch{Header: header, SentAtTime: sentAt, ProductKey: spec.OpaqueKey}, spec, nil
}

func validateScope(scope Scope) (product.Spec, error) {
	spec, ok := product.Lookup(scope.Product)
	if !ok {
		return product.Spec{}, errors.New("unknown product")
	}
	if !opaqueKeyPattern.MatchString(scope.SubjectKey) {
		return product.Spec{}, errors.New("subject_key must be a lowercase opaque SHA-256 key")
	}
	for name, key := range map[string]*string{"person_key": scope.PersonKey, "org_key": scope.OrgKey} {
		if key != nil && !opaqueKeyPattern.MatchString(*key) {
			return product.Spec{}, fmt.Errorf("%s must be a lowercase opaque SHA-256 key", name)
		}
	}
	return spec, nil
}

func validateCommonRecord(record CommonRecord, sentAt time.Time) (ValidatedCommonRecord, error) {
	occurredAt, err := validateBaseRecord(record.RecordID, record.OccurredAt, record.AppSessionID, record.App, sentAt)
	if err != nil {
		return ValidatedCommonRecord{}, err
	}
	switch record.Kind {
	case "app_started", "app_backgrounded", "app_foregrounded", "app_closed":
		if record.Page != nil || record.Mode != nil {
			return ValidatedCommonRecord{}, errors.New("page and mode are forbidden for app lifecycle records")
		}
	case "page_viewed":
		if record.Page == nil || record.Mode != nil || !oneOf(*record.Page, "home", "games", "game", "settings", "statistics", "onboarding") {
			return ValidatedCommonRecord{}, errors.New("page_viewed requires an allowed page only")
		}
	case "mode_changed":
		if record.Mode == nil || record.Page != nil || !oneOf(*record.Mode, "self", "assisted") {
			return ValidatedCommonRecord{}, errors.New("mode_changed requires an allowed mode only")
		}
	default:
		return ValidatedCommonRecord{}, errors.New("unknown common kind")
	}
	return ValidatedCommonRecord{CommonRecord: record, OccurredAtTime: occurredAt}, nil
}

func validateTechnicalRecord(record TechnicalRecord, sentAt time.Time) (ValidatedTechnicalRecord, error) {
	occurredAt, err := validateBaseRecord(record.RecordID, record.OccurredAt, record.AppSessionID, record.App, sentAt)
	if err != nil {
		return ValidatedTechnicalRecord{}, err
	}
	if !oneOf(record.Component, "main", "renderer", "tobii", "updater", "telemetry") {
		return ValidatedTechnicalRecord{}, errors.New("unknown technical component")
	}
	switch record.Kind {
	case "state_changed":
		if record.State == nil || record.ErrorFingerprint != nil || record.DroppedCount != nil || record.DropReason != nil ||
			!oneOf(*record.State, "starting", "ready", "idle", "connecting", "connected", "tracking", "checking", "downloading", "installing", "unavailable", "error") {
			return ValidatedTechnicalRecord{}, errors.New("state_changed requires an allowed state only")
		}
	case "error":
		if record.ErrorFingerprint == nil || record.State != nil || record.DroppedCount != nil || record.DropReason != nil || !fingerprintPattern.MatchString(*record.ErrorFingerprint) {
			return ValidatedTechnicalRecord{}, errors.New("error requires a lowercase stable fingerprint only")
		}
	case "queue_dropped":
		if record.Component != "telemetry" || record.DroppedCount == nil || *record.DroppedCount == 0 || *record.DroppedCount > 1_000_000 || record.DropReason == nil ||
			record.State != nil || record.ErrorFingerprint != nil || !oneOf(*record.DropReason, "capacity", "expired", "invalid", "shutdown") {
			return ValidatedTechnicalRecord{}, errors.New("queue_dropped requires bounded count and allowed reason")
		}
	default:
		return ValidatedTechnicalRecord{}, errors.New("unknown technical kind")
	}
	return ValidatedTechnicalRecord{TechnicalRecord: record, OccurredAtTime: occurredAt}, nil
}

func validatePlaysRecord(record PlaysRecord, sentAt time.Time, spec product.Spec) (ValidatedPlaysRecord, error) {
	occurredAt, err := validateBaseRecord(record.RecordID, record.OccurredAt, record.AppSessionID, record.App, sentAt)
	if err != nil {
		return ValidatedPlaysRecord{}, err
	}
	if err := validateUUID(record.GameSessionID); err != nil {
		return ValidatedPlaysRecord{}, fmt.Errorf("game_session_id: %w", err)
	}
	if !spec.AllowsGame(record.GameID) {
		return ValidatedPlaysRecord{}, errors.New("game_id is not registered for product")
	}
	if !oneOf(record.GameCategory, "gaze-basics", "visual-search", "sequencing", "language-aac", "numeracy", "strategy", "continuous-control") {
		return ValidatedPlaysRecord{}, errors.New("unknown game_category")
	}
	if !oneOf(record.InputMethod, "mouse", "touch", "gaze", "keyboard") {
		return ValidatedPlaysRecord{}, errors.New("unknown input_method")
	}
	if record.ValidGazeRatio != nil && (math.IsNaN(*record.ValidGazeRatio) || math.IsInf(*record.ValidGazeRatio, 0) || *record.ValidGazeRatio < 0 || *record.ValidGazeRatio > 1) {
		return ValidatedPlaysRecord{}, errors.New("valid_gaze_ratio must be finite and between zero and one")
	}
	switch record.Kind {
	case "session_started":
		if hasPlaysOutcome(record) {
			return ValidatedPlaysRecord{}, errors.New("session_started cannot contain outcome metrics")
		}
	case "session_finished":
		if record.Outcome == nil || !oneOf(*record.Outcome, "completed", "interrupted", "cancelled", "error") || record.DurationMS == nil || *record.DurationMS > uint64((7*24*time.Hour).Milliseconds()) {
			return ValidatedPlaysRecord{}, errors.New("session_finished requires allowed outcome and bounded duration_ms")
		}
		if record.LevelIndex != nil {
			return ValidatedPlaysRecord{}, errors.New("level_index is forbidden for session_finished")
		}
	case "interaction":
		if record.LevelIndex == nil || record.Outcome == nil || !oneOf(*record.Outcome, "success", "mistake", "hint", "cancelled") || record.DurationMS != nil ||
			record.SuccessCount != nil || record.MistakeCount != nil || record.HintCount != nil || record.ValidGazeRatio != nil {
			return ValidatedPlaysRecord{}, errors.New("interaction requires level_index and allowed outcome only")
		}
	default:
		return ValidatedPlaysRecord{}, errors.New("unknown plays kind")
	}
	return ValidatedPlaysRecord{PlaysRecord: record, OccurredAtTime: occurredAt}, nil
}

func validateProductRecord(record ProductRecord, sentAt time.Time, spec product.Spec) (ValidatedProductRecord, error) {
	occurredAt, err := validateBaseRecord(record.RecordID, record.OccurredAt, record.AppSessionID, record.App, sentAt)
	if err != nil {
		return ValidatedProductRecord{}, err
	}
	if !spec.AllowsProductKind(record.Kind) {
		return ValidatedProductRecord{}, errors.New("product kind is not registered for product")
	}
	return ValidatedProductRecord{ProductRecord: record, OccurredAtTime: occurredAt}, nil
}

func validateBaseRecord(recordID, occurredAt, appSessionID string, app AppMetadata, sentAt time.Time) (time.Time, error) {
	if err := validateUUID(recordID); err != nil {
		return time.Time{}, fmt.Errorf("record_id: %w", err)
	}
	if err := validateUUID(appSessionID); err != nil {
		return time.Time{}, fmt.Errorf("app_session_id: %w", err)
	}
	if err := validateApp(app); err != nil {
		return time.Time{}, err
	}
	parsed, err := parseTimestamp(occurredAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("occurred_at: %w", err)
	}
	if parsed.Before(sentAt.Add(-30*24*time.Hour)) || parsed.After(sentAt.Add(5*time.Minute)) {
		return time.Time{}, errors.New("occurred_at is outside the allowed batch range")
	}
	return parsed, nil
}

func validateApp(app AppMetadata) error {
	for name, value := range map[string]string{"app.version": app.Version, "app.build": app.Build, "app.os_version": app.OSVersion} {
		if !safeValuePattern.MatchString(value) {
			return fmt.Errorf("%s contains an unsafe or empty value", name)
		}
	}
	if !oneOf(app.Platform, "windows", "macos", "linux", "web", "android", "ios") {
		return errors.New("unknown app.platform")
	}
	if !oneOf(app.Locale, "ru", "ru-RU", "en", "en-US") {
		return errors.New("unknown app.locale")
	}
	return nil
}

func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("must be RFC3339")
	}
	if parsed.Nanosecond()%int(time.Millisecond) != 0 {
		return time.Time{}, errors.New("precision must not exceed milliseconds")
	}
	return validateTimestampRange(parsed.UTC())
}

func parsePrivacyTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("must be RFC3339")
	}
	return validateTimestampRange(parsed.UTC().Truncate(time.Millisecond))
}

func validateTimestampRange(parsed time.Time) (time.Time, error) {
	if parsed.Before(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) || !parsed.Before(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)) {
		return time.Time{}, errors.New("timestamp is outside the storage range")
	}
	return parsed, nil
}

func validateRecordCount(count int) error {
	if count < 1 || count > MaxBatchRecords {
		return fmt.Errorf("batch must contain between 1 and %d records", MaxBatchRecords)
	}
	return nil
}

func validateUUID(value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return errors.New("invalid UUID")
	}
	return nil
}

func hasPlaysOutcome(record PlaysRecord) bool {
	return record.LevelIndex != nil || record.Outcome != nil || record.DurationMS != nil || record.SuccessCount != nil ||
		record.MistakeCount != nil || record.HintCount != nil || record.ValidGazeRatio != nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
