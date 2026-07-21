package clickhouse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/linkasu/linka.plays-metric/internal/contract/fundraising"
)

// InsertFundraising persists only closed financial dimensions. It is deliberately
// separate from telemetry ledgers and privacy deletion scopes because no identity
// or user-level telemetry key is accepted by the fundraising contract.
func (s *Store) InsertFundraising(ctx context.Context, input fundraising.ValidatedBatch, bodySHA string) (fundraising.IngestResult, error) {
	s.fundraisingMu.Lock()
	defer s.fundraisingMu.Unlock()

	var existingSHA, status string
	var reservedAt time.Time
	err := s.connection.QueryRow(ctx, `
		SELECT body_sha256, status, ingested_at
		FROM fundraising_ingest_batches_v1 FINAL
		WHERE batch_id = ?
		ORDER BY ingested_at DESC
		LIMIT 1`, uuid.MustParse(input.BatchID)).Scan(&existingSHA, &status, &reservedAt)
	exists := err == nil
	if exists {
		if existingSHA != bodySHA {
			return fundraising.IngestResult{}, fundraising.ErrIdempotencyConflict
		}
		if status == "completed" {
			return fundraising.IngestResult{Count: len(input.Records), Replayed: true}, nil
		}
		if status != "reserved" {
			return fundraising.IngestResult{}, errors.New("invalid fundraising batch ledger status")
		}
	}
	if !exists && !errors.Is(err, sql.ErrNoRows) {
		return fundraising.IngestResult{}, fmt.Errorf("query fundraising batch ledger: %w", err)
	}

	ingestedAt := reservedAt
	if !exists {
		ingestedAt = s.now().UTC().Truncate(time.Millisecond)
		if err := s.insertFundraisingBatchLedger(ctx, input, bodySHA, ingestedAt, "reserved"); err != nil {
			return fundraising.IngestResult{}, err
		}
	}
	if err := s.insertFundraisingEvents(ctx, input, ingestedAt); err != nil {
		return fundraising.IngestResult{}, err
	}
	completedAt := s.now().UTC().Truncate(time.Millisecond)
	if !completedAt.After(ingestedAt) {
		completedAt = ingestedAt.Add(time.Millisecond)
	}
	if err := s.insertFundraisingBatchLedger(ctx, input, bodySHA, completedAt, "completed"); err != nil {
		return fundraising.IngestResult{}, err
	}
	return fundraising.IngestResult{Count: len(input.Records)}, nil
}

func (s *Store) insertFundraisingBatchLedger(ctx context.Context, input fundraising.ValidatedBatch, bodySHA string, ingestedAt time.Time, status string) error {
	if err := s.connection.Exec(ctx, `INSERT INTO fundraising_ingest_batches_v1 (
		batch_id, body_sha256, record_count, sent_at, ingested_at, status
	) VALUES (?, ?, ?, ?, ?, ?)`, uuid.MustParse(input.BatchID), bodySHA, uint16(len(input.Records)), input.SentAtTime, ingestedAt, status); err != nil {
		return fmt.Errorf("insert fundraising batch ledger: %w", err)
	}
	return nil
}

func (s *Store) insertFundraisingEvents(ctx context.Context, input fundraising.ValidatedBatch, ingestedAt time.Time) error {
	batch, err := s.connection.PrepareBatch(ctx, `INSERT INTO fundraising_events_v1 (
		batch_id, event_id, occurred_at, kind, amount, currency, frequency,
		attribution_source, attribution_campaign, failure_code, ingested_at
	)`)
	if err != nil {
		return fmt.Errorf("prepare fundraising events batch: %w", err)
	}
	for _, event := range input.Records {
		if err := batch.Append(uuid.MustParse(input.BatchID), uuid.MustParse(event.EventID), event.OccurredAtTime, event.Kind,
			event.Amount, nullableFundraisingCurrency(event.Currency), event.Frequency, event.AttributionSource,
			event.AttributionCampaign, event.FailureCode, ingestedAt); err != nil {
			return fmt.Errorf("append fundraising event: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send fundraising events batch: %w", err)
	}
	return nil
}

func nullableFundraisingCurrency(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
