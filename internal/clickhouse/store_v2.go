package clickhouse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
)

func (s *Store) InsertV2(ctx context.Context, batch v2.ValidatedBatch, bodySHA string) (v2.IngestResult, error) {
	// ClickHouse has no uniqueness constraints. Serialize the check/write sequence
	// within a writer process; ReplacingMergeTree makes same-body retries harmless.
	s.v2Mu.Lock()
	defer s.v2Mu.Unlock()

	existingSHA, status, reservedAt, exists, err := s.batchLedgerV2(ctx, string(batch.Header.Scope.Product), batch.Header.BatchID)
	if err != nil {
		return v2.IngestResult{}, err
	}
	if exists {
		if existingSHA != bodySHA {
			return v2.IngestResult{}, v2.ErrIdempotencyConflict
		}
		if status == "completed" {
			return v2.IngestResult{Replayed: true, Count: batch.RecordCount()}, nil
		}
		if status != "reserved" {
			return v2.IngestResult{}, errors.New("invalid v2 batch ledger status")
		}
	}
	suppressed, err := s.isSuppressed(ctx, batch.Header.Scope, batch.ProductKey)
	if err != nil {
		return v2.IngestResult{}, err
	}
	if suppressed {
		return v2.IngestResult{}, v2.ErrSuppressed
	}

	ingestedAt := reservedAt
	if !exists {
		ingestedAt = s.now().UTC().Truncate(time.Millisecond)
		if err := s.insertBatchLedgerV2(ctx, batch, bodySHA, ingestedAt, "reserved"); err != nil {
			return v2.IngestResult{}, err
		}
	}
	if err := s.registerRecordsV2(ctx, batch, bodySHA, ingestedAt); err != nil {
		return v2.IngestResult{}, err
	}
	switch batch.Header.Stream {
	case "common":
		err = s.insertCommonV2(ctx, batch, ingestedAt)
	case "technical":
		err = s.insertTechnicalV2(ctx, batch, ingestedAt)
	case "plays":
		err = s.insertPlaysV2(ctx, batch, ingestedAt)
	case "product":
		err = s.insertProductV2(ctx, batch, ingestedAt)
	case "outcome":
		err = s.insertOutcomeV2(ctx, batch, ingestedAt)
	default:
		err = errors.New("unsupported validated stream")
	}
	if err != nil {
		return v2.IngestResult{}, err
	}
	completedAt := s.now().UTC().Truncate(time.Millisecond)
	if !completedAt.After(ingestedAt) {
		completedAt = ingestedAt.Add(time.Millisecond)
	}
	if err := s.insertBatchLedgerV2(ctx, batch, bodySHA, completedAt, "completed"); err != nil {
		return v2.IngestResult{}, err
	}
	return v2.IngestResult{Count: batch.RecordCount()}, nil
}

func (s *Store) registerRecordsV2(ctx context.Context, batch v2.ValidatedBatch, bodySHA string, registeredAt time.Time) error {
	recordIDs := make([]uuid.UUID, 0, batch.RecordCount())
	for _, record := range batch.CommonRecords {
		recordIDs = append(recordIDs, uuid.MustParse(record.RecordID))
	}
	for _, record := range batch.TechnicalRecords {
		recordIDs = append(recordIDs, uuid.MustParse(record.RecordID))
	}
	for _, record := range batch.PlaysRecords {
		recordIDs = append(recordIDs, uuid.MustParse(record.RecordID))
	}
	for _, record := range batch.ProductRecords {
		recordIDs = append(recordIDs, uuid.MustParse(record.RecordID))
	}
	for _, record := range batch.OutcomeRecords {
		recordIDs = append(recordIDs, uuid.MustParse(record.RecordID))
	}
	// Retried registry rows have the same batch and body. Reading every matching
	// version avoids an expensive FINAL merge while preserving conflict checks.
	rows, err := s.connection.Query(ctx, `
		SELECT batch_id, body_sha256
		FROM record_registry_v2
		WHERE product = ? AND record_id IN (?)`, string(batch.Header.Scope.Product), recordIDs)
	if err != nil {
		return fmt.Errorf("query v2 record registry: %w", err)
	}
	defer rows.Close()
	batchID := uuid.MustParse(batch.Header.BatchID)
	for rows.Next() {
		var existingBatch uuid.UUID
		var existingSHA string
		if err := rows.Scan(&existingBatch, &existingSHA); err != nil {
			return fmt.Errorf("scan v2 record registry: %w", err)
		}
		if existingBatch != batchID || existingSHA != bodySHA {
			return v2.ErrDuplicateRecord
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate v2 record registry: %w", err)
	}
	registry, err := s.connection.PrepareBatch(ctx, `INSERT INTO record_registry_v2 (
		product, record_id, batch_id, stream, body_sha256, registered_at,
		product_key, subject_key, person_key, org_key
	)`)
	if err != nil {
		return fmt.Errorf("prepare v2 record registry: %w", err)
	}
	for _, recordID := range recordIDs {
		if err := registry.Append(string(batch.Header.Scope.Product), recordID, batchID, string(batch.Header.Stream), bodySHA, registeredAt,
			&batch.ProductKey, &batch.Header.Scope.SubjectKey, batch.Header.Scope.PersonKey, batch.Header.Scope.OrgKey); err != nil {
			return fmt.Errorf("append v2 record registry: %w", err)
		}
	}
	if err := registry.Send(); err != nil {
		return fmt.Errorf("send v2 record registry: %w", err)
	}
	return nil
}

func (s *Store) batchLedgerV2(ctx context.Context, productID, batchID string) (string, string, time.Time, bool, error) {
	var bodySHA, status string
	var ingestedAt time.Time
	err := s.connection.QueryRow(ctx, `
		SELECT body_sha256, status, ingested_at
		FROM ingest_batches_v2 FINAL
		WHERE product = ? AND batch_id = ?
		ORDER BY ingested_at DESC
		LIMIT 1`, productID, uuid.MustParse(batchID)).Scan(&bodySHA, &status, &ingestedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", time.Time{}, false, nil
	}
	if err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("query v2 batch ledger: %w", err)
	}
	return bodySHA, status, ingestedAt, true, nil
}

func (s *Store) isSuppressed(ctx context.Context, scope v2.Scope, productKey string) (bool, error) {
	var count uint64
	err := s.connection.QueryRow(ctx, `
		SELECT count()
		FROM privacy_suppressions_v2 FINAL
		WHERE active = true AND product = ? AND product_key = ? AND (
			subject_key = ? OR
			(? != '' AND ifNull(person_key, '') = ?) OR
			(? != '' AND ifNull(org_key, '') = ?)
		)`, string(scope.Product), productKey, scope.SubjectKey, stringValue(scope.PersonKey), stringValue(scope.PersonKey), stringValue(scope.OrgKey), stringValue(scope.OrgKey)).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("query privacy suppression: %w", err)
	}
	return count > 0, nil
}

func (s *Store) insertBatchLedgerV2(ctx context.Context, batch v2.ValidatedBatch, bodySHA string, ingestedAt time.Time, status string) error {
	err := s.connection.Exec(ctx, `INSERT INTO ingest_batches_v2 (
		batch_id, idempotency_key, product, product_key, subject_key, person_key, org_key, stream,
		body_sha256, record_count, sent_at, ingested_at, expires_at, status
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.MustParse(batch.Header.BatchID), uuid.MustParse(batch.Header.BatchID), string(batch.Header.Scope.Product), &batch.ProductKey,
		batch.Header.Scope.SubjectKey, batch.Header.Scope.PersonKey, batch.Header.Scope.OrgKey, string(batch.Header.Stream), bodySHA,
		uint16(batch.RecordCount()), batch.SentAtTime, ingestedAt, expiresAt(ingestedAt, s.retention.IngestBatches), status,
	)
	if err != nil {
		return fmt.Errorf("insert v2 batch ledger: %w", err)
	}
	return nil
}

func (s *Store) insertCommonV2(ctx context.Context, input v2.ValidatedBatch, ingestedAt time.Time) error {
	batch, err := s.connection.PrepareBatch(ctx, `INSERT INTO common_events_v2 (
		product, product_key, subject_key, person_key, org_key, batch_id, record_id, occurred_at, kind,
		app_session_id, app_version, app_build, platform, os_version, locale, page, mode, ingested_at, expires_at
	)`)
	if err != nil {
		return fmt.Errorf("prepare common v2 batch: %w", err)
	}
	for _, record := range input.CommonRecords {
		if err := batch.Append(
			string(input.Header.Scope.Product), &input.ProductKey, input.Header.Scope.SubjectKey, input.Header.Scope.PersonKey, input.Header.Scope.OrgKey,
			uuid.MustParse(input.Header.BatchID), uuid.MustParse(record.RecordID), record.OccurredAtTime, record.Kind, uuid.MustParse(record.AppSessionID),
			record.App.Version, record.App.Build, record.App.Platform, record.App.OSVersion, record.App.Locale, record.Page, record.Mode,
			ingestedAt, expiresAt(ingestedAt, s.retention.Common),
		); err != nil {
			return fmt.Errorf("append common v2 record: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send common v2 batch: %w", err)
	}
	return nil
}

func (s *Store) insertTechnicalV2(ctx context.Context, input v2.ValidatedBatch, ingestedAt time.Time) error {
	batch, err := s.connection.PrepareBatch(ctx, `INSERT INTO technical_events_v2 (
		product, product_key, subject_key, person_key, org_key, batch_id, record_id, occurred_at, kind,
		app_session_id, app_version, app_build, platform, os_version, locale, component, state,
		error_fingerprint, dropped_count, drop_reason, ingested_at, expires_at
	)`)
	if err != nil {
		return fmt.Errorf("prepare technical v2 batch: %w", err)
	}
	for _, record := range input.TechnicalRecords {
		if err := batch.Append(
			string(input.Header.Scope.Product), &input.ProductKey, input.Header.Scope.SubjectKey, input.Header.Scope.PersonKey, input.Header.Scope.OrgKey,
			uuid.MustParse(input.Header.BatchID), uuid.MustParse(record.RecordID), record.OccurredAtTime, record.Kind, uuid.MustParse(record.AppSessionID),
			record.App.Version, record.App.Build, record.App.Platform, record.App.OSVersion, record.App.Locale, record.Component, record.State,
			record.ErrorFingerprint, record.DroppedCount, record.DropReason, ingestedAt, expiresAt(ingestedAt, s.retention.Technical),
		); err != nil {
			return fmt.Errorf("append technical v2 record: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send technical v2 batch: %w", err)
	}
	return nil
}

func (s *Store) insertPlaysV2(ctx context.Context, input v2.ValidatedBatch, ingestedAt time.Time) error {
	batch, err := s.connection.PrepareBatch(ctx, `INSERT INTO plays_events_v2 (
		product, product_key, subject_key, person_key, org_key, batch_id, record_id, occurred_at, kind,
		app_session_id, game_session_id, app_version, app_build, platform, os_version, locale, game_id,
		game_category, input_method, level_index, outcome, duration_ms, success_count, mistake_count,
		hint_count, valid_gaze_ratio, ingested_at, expires_at
	)`)
	if err != nil {
		return fmt.Errorf("prepare plays v2 batch: %w", err)
	}
	for _, record := range input.PlaysRecords {
		if err := batch.Append(
			string(input.Header.Scope.Product), &input.ProductKey, input.Header.Scope.SubjectKey, input.Header.Scope.PersonKey, input.Header.Scope.OrgKey,
			uuid.MustParse(input.Header.BatchID), uuid.MustParse(record.RecordID), record.OccurredAtTime, record.Kind, uuid.MustParse(record.AppSessionID),
			uuid.MustParse(record.GameSessionID), record.App.Version, record.App.Build, record.App.Platform, record.App.OSVersion, record.App.Locale,
			record.GameID, record.GameCategory, record.InputMethod, record.LevelIndex, record.Outcome, record.DurationMS, record.SuccessCount,
			record.MistakeCount, record.HintCount, record.ValidGazeRatio, ingestedAt, expiresAt(ingestedAt, s.retention.Plays),
		); err != nil {
			return fmt.Errorf("append plays v2 record: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send plays v2 batch: %w", err)
	}
	return nil
}

func (s *Store) insertProductV2(ctx context.Context, input v2.ValidatedBatch, ingestedAt time.Time) error {
	batch, err := s.connection.PrepareBatch(ctx, `INSERT INTO product_events_v2 (
		product, product_key, subject_key, person_key, org_key, batch_id, record_id, occurred_at, kind,
		app_session_id, app_version, app_build, platform, os_version, locale, ingested_at, expires_at
	)`)
	if err != nil {
		return fmt.Errorf("prepare product v2 batch: %w", err)
	}
	for _, record := range input.ProductRecords {
		if err := batch.Append(
			string(input.Header.Scope.Product), &input.ProductKey, input.Header.Scope.SubjectKey, input.Header.Scope.PersonKey, input.Header.Scope.OrgKey,
			uuid.MustParse(input.Header.BatchID), uuid.MustParse(record.RecordID), record.OccurredAtTime, record.Kind, uuid.MustParse(record.AppSessionID),
			record.App.Version, record.App.Build, record.App.Platform, record.App.OSVersion, record.App.Locale,
			ingestedAt, expiresAt(ingestedAt, s.retention.Product),
		); err != nil {
			return fmt.Errorf("append product v2 record: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send product v2 batch: %w", err)
	}
	return nil
}

func (s *Store) insertOutcomeV2(ctx context.Context, input v2.ValidatedBatch, ingestedAt time.Time) error {
	batch, err := s.connection.PrepareBatch(ctx, `INSERT INTO product_outcomes_v2 (
		product, product_key, subject_key, person_key, org_key, batch_id, record_id, occurred_at, kind,
		app_session_id, app_version, app_build, platform, os_version, locale, result, source, mode,
		count_bucket, duration_bucket, failure_code, ingested_at, expires_at
	)`)
	if err != nil {
		return fmt.Errorf("prepare outcome v2 batch: %w", err)
	}
	for _, record := range input.OutcomeRecords {
		if err := batch.Append(
			string(input.Header.Scope.Product), &input.ProductKey, input.Header.Scope.SubjectKey, input.Header.Scope.PersonKey, input.Header.Scope.OrgKey,
			uuid.MustParse(input.Header.BatchID), uuid.MustParse(record.RecordID), record.OccurredAtTime, record.Kind, uuid.MustParse(record.AppSessionID),
			record.App.Version, record.App.Build, record.App.Platform, record.App.OSVersion, record.App.Locale, record.Result, record.Source, record.Mode,
			record.CountBucket, record.DurationBucket, record.FailureCode, ingestedAt, expiresAt(ingestedAt, s.retention.Outcome),
		); err != nil {
			return fmt.Errorf("append outcome v2 record: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send outcome v2 batch: %w", err)
	}
	return nil
}

func expiresAt(from time.Time, retention time.Duration) *time.Time {
	if retention <= 0 {
		return nil
	}
	value := from.Add(retention)
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
