package clickhouse

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	v1 "github.com/linkasu/linka.plays-metric/internal/contract/v1"
	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
	"github.com/linkasu/linka.plays-metric/internal/privacy"
	"github.com/linkasu/linka.plays-metric/internal/product"
)

func (s *Store) CreateLegacyPrivacyRequest(ctx context.Context, request v1.ValidatedPrivacyRequest, bodySHA string) (v2.PrivacyResult, error) {
	s.v1Mu.Lock()
	defer s.v1Mu.Unlock()
	s.v2Mu.Lock()
	defer s.v2Mu.Unlock()
	productSpec, _ := product.Lookup(product.LinkaPlays)
	var existingSHA, status string
	err := s.connection.QueryRow(ctx, `
		SELECT body_sha256, status FROM privacy_suppressions_v2 FINAL
		WHERE product = ? AND request_id = ? LIMIT 1`, string(product.LinkaPlays), uuid.MustParse(request.RequestID)).Scan(&existingSHA, &status)
	if err == nil {
		if existingSHA != bodySHA {
			return v2.PrivacyResult{}, v2.ErrIdempotencyConflict
		}
		return v2.PrivacyResult{Replayed: true, Status: status}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return v2.PrivacyResult{}, fmt.Errorf("query V1 privacy idempotency ledger: %w", err)
	}
	digest := sha256.Sum256([]byte(request.InstallationID))
	subjectKey := hex.EncodeToString(digest[:])
	updatedAt := s.now().UTC()
	err = s.connection.Exec(ctx, `INSERT INTO privacy_suppressions_v2 (
		request_id, product, product_key, subject_key, person_key, org_key, action, status,
		body_sha256, requested_at, ingested_at, updated_at, active, failure_code, expires_at,
		legacy_installation_id
	) VALUES (?, ?, ?, ?, NULL, NULL, 'delete', 'pending', ?, ?, ?, ?, true, NULL, ?, ?)`,
		uuid.MustParse(request.RequestID), string(product.LinkaPlays), &productSpec.OpaqueKey, subjectKey, bodySHA,
		request.RequestedAtTime, updatedAt, updatedAt, expiresAt(updatedAt, s.retention.Privacy), uuid.MustParse(request.InstallationID))
	if err != nil {
		return v2.PrivacyResult{}, fmt.Errorf("insert V1 privacy request: %w", err)
	}
	return v2.PrivacyResult{Status: "pending"}, nil
}

func (s *Store) CreatePrivacyRequest(ctx context.Context, request v2.ValidatedPrivacyRequest, bodySHA string) (v2.PrivacyResult, error) {
	s.v2Mu.Lock()
	defer s.v2Mu.Unlock()

	var existingSHA, status string
	err := s.connection.QueryRow(ctx, `
		SELECT body_sha256, status
		FROM privacy_suppressions_v2 FINAL
		WHERE product = ? AND request_id = ?
		ORDER BY updated_at DESC
		LIMIT 1`, string(request.Scope.Product), uuid.MustParse(request.RequestID)).Scan(&existingSHA, &status)
	if err == nil {
		if existingSHA != bodySHA {
			return v2.PrivacyResult{}, v2.ErrIdempotencyConflict
		}
		return v2.PrivacyResult{Replayed: true, Status: status}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return v2.PrivacyResult{}, fmt.Errorf("query privacy idempotency ledger: %w", err)
	}

	updatedAt := s.now().UTC()
	err = s.connection.Exec(ctx, `INSERT INTO privacy_suppressions_v2 (
		request_id, product, product_key, subject_key, person_key, org_key, action, status,
		body_sha256, requested_at, ingested_at, updated_at, active, failure_code, expires_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, true, NULL, ?)`,
		uuid.MustParse(request.RequestID), string(request.Scope.Product), &request.ProductKey, request.Scope.SubjectKey,
		request.Scope.PersonKey, request.Scope.OrgKey, string(request.Action), bodySHA, request.RequestedAtTime,
		updatedAt, updatedAt, expiresAt(updatedAt, s.retention.Privacy),
	)
	if err != nil {
		return v2.PrivacyResult{}, fmt.Errorf("insert privacy request: %w", err)
	}
	return v2.PrivacyResult{Status: "pending"}, nil
}

func (s *Store) PendingPrivacyRequests(ctx context.Context, limit int) ([]privacy.Request, error) {
	rows, err := s.connection.Query(ctx, `
		SELECT request_id, product, product_key, subject_key, person_key, org_key, action, body_sha256,
		       requested_at, ingested_at, expires_at, attempts, legacy_installation_id
		FROM privacy_suppressions_v2 FINAL
		WHERE (status IN ('pending', 'retry') AND available_at <= now64(3))
		   OR (status = 'processing' AND lease_until < now64(3))
		ORDER BY requested_at
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending privacy requests: %w", err)
	}
	defer rows.Close()
	var requests []privacy.Request
	for rows.Next() {
		var request privacy.Request
		var requestID uuid.UUID
		var productID string
		var productKey *string
		var action string
		if err := rows.Scan(
			&requestID, &productID, &productKey, &request.Scope.SubjectKey, &request.Scope.PersonKey, &request.Scope.OrgKey,
			&action, &request.BodySHA256, &request.RequestedAt, &request.IngestedAt, &request.ExpiresAt, &request.Attempts,
			&request.LegacyInstallationID,
		); err != nil {
			return nil, fmt.Errorf("scan pending privacy request: %w", err)
		}
		if productKey == nil {
			return nil, errors.New("pending privacy request has no product key")
		}
		request.RequestID = requestID.String()
		request.Scope.Product = product.ID(productID)
		request.ProductKey = *productKey
		request.Action = v2.PrivacyAction(action)
		requests = append(requests, request)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending privacy requests: %w", err)
	}
	return requests, nil
}

func (s *Store) ClaimPrivacyRequest(ctx context.Context, request privacy.Request) (bool, error) {
	s.privacyMu.Lock()
	defer s.privacyMu.Unlock()
	var status string
	var attempts uint16
	var availableAt time.Time
	var leaseUntil *time.Time
	err := s.connection.QueryRow(ctx, `
		SELECT status, attempts, available_at, lease_until
		FROM privacy_suppressions_v2 FINAL
		WHERE product = ? AND request_id = ? LIMIT 1`,
		string(request.Scope.Product), uuid.MustParse(request.RequestID)).Scan(&status, &attempts, &availableAt, &leaseUntil)
	if err != nil {
		return false, fmt.Errorf("query privacy request before claim: %w", err)
	}
	now := s.now().UTC()
	claimable := (status == "pending" || status == "retry") && !availableAt.After(now)
	claimable = claimable || status == "processing" && leaseUntil != nil && leaseUntil.Before(now)
	if !claimable {
		return false, nil
	}
	request.Attempts = attempts
	lease := now.Add(5 * time.Minute)
	if err := s.insertPrivacyStatus(ctx, request, "processing", nil, attempts+1, now, &lease); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) CompletePrivacyRequest(ctx context.Context, request privacy.Request) error {
	now := s.now().UTC()
	return s.insertPrivacyStatus(ctx, request, "completed", nil, request.Attempts, now, nil)
}

func (s *Store) RetryPrivacyRequest(ctx context.Context, request privacy.Request, failureCode string, maxAttempts int) error {
	now := s.now().UTC()
	status := "retry"
	if int(request.Attempts) >= maxAttempts {
		status = "failed"
	}
	return s.insertPrivacyStatus(ctx, request, status, &failureCode, request.Attempts, now.Add(privacyRetryDelay(request.Attempts)), nil)
}

func (s *Store) DeleteTelemetryRequest(ctx context.Context, request privacy.Request) error {
	s.privacyMu.Lock()
	defer s.privacyMu.Unlock()
	tables := []string{"common_events_v2", "technical_events_v2", "plays_events_v2", "product_events_v2", "record_registry_v2", "ingest_batches_v2"}
	if request.LegacyInstallationID != nil {
		s.v1Mu.Lock()
		defer s.v1Mu.Unlock()
		tables = []string{"events", "session_summaries"}
	}
	for _, table := range tables {
		if err := s.deleteTelemetryTable(ctx, request, table); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) deleteTelemetryTable(ctx context.Context, request privacy.Request, table string) error {
	now := s.now().UTC()
	status, attempts, err := s.deletionProgress(ctx, request, table)
	if err != nil {
		return err
	}
	if status == "completed" {
		return nil
	}
	lease := now.Add(5 * time.Minute)
	if err := s.insertDeletionProgress(ctx, request, table, "processing", attempts+1, now, &lease, nil, nil); err != nil {
		return err
	}
	scope := request.Scope
	if request.LegacyInstallationID != nil {
		query := "ALTER TABLE " + table + " DELETE WHERE installation_id = ? SETTINGS mutations_sync = 2"
		if err := s.connection.Exec(ctx, query, uuid.MustParse(*request.LegacyInstallationID)); err != nil {
			failureCode := "mutation_failed"
			_ = s.insertDeletionProgress(ctx, request, table, "retry", attempts+1, now.Add(privacyRetryDelay(attempts+1)), nil, &failureCode, nil)
			return fmt.Errorf("delete V1 privacy scope from %s: %w", table, err)
		}
		return s.insertDeletionProgress(ctx, request, table, "completed", attempts+1, now, nil, nil, &now)
	}
	condition := `product = ? AND product_key = ? AND (
		subject_key = ? OR
		(? != '' AND ifNull(person_key, '') = ?) OR
		(? != '' AND ifNull(org_key, '') = ?)
	)`
	arguments := []any{string(scope.Product), request.ProductKey, scope.SubjectKey, stringValue(scope.PersonKey), stringValue(scope.PersonKey), stringValue(scope.OrgKey), stringValue(scope.OrgKey)}
	query := "ALTER TABLE " + table + " DELETE WHERE " + condition + " SETTINGS mutations_sync = 2"
	if err := s.connection.Exec(ctx, query, arguments...); err != nil {
		failureCode := "mutation_failed"
		_ = s.insertDeletionProgress(ctx, request, table, "retry", attempts+1, now.Add(privacyRetryDelay(attempts+1)), nil, &failureCode, nil)
		return fmt.Errorf("delete privacy scope from %s: %w", table, err)
	}
	return s.insertDeletionProgress(ctx, request, table, "completed", attempts+1, now, nil, nil, &now)
}

func (s *Store) deletionProgress(ctx context.Context, request privacy.Request, table string) (string, uint16, error) {
	var status string
	var attempts uint16
	err := s.connection.QueryRow(ctx, `
		SELECT status, attempts
		FROM privacy_deletion_progress_v2 FINAL
		WHERE product = ? AND request_id = ? AND table_name = ?
		LIMIT 1`, string(request.Scope.Product), uuid.MustParse(request.RequestID), table).Scan(&status, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return "pending", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("query privacy deletion progress for %s: %w", table, err)
	}
	return status, attempts, nil
}

func (s *Store) insertDeletionProgress(ctx context.Context, request privacy.Request, table, status string, attempts uint16,
	availableAt time.Time, leaseUntil *time.Time, lastError *string, completedAt *time.Time) error {
	err := s.connection.Exec(ctx, `INSERT INTO privacy_deletion_progress_v2 (
		request_id, product, table_name, status, attempts, available_at, lease_until,
		last_error, updated_at, completed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, uuid.MustParse(request.RequestID), string(request.Scope.Product), table,
		status, attempts, availableAt, leaseUntil, lastError, privacyStatusVersion(request, status, attempts), completedAt)
	if err != nil {
		return fmt.Errorf("insert privacy deletion progress for %s: %w", table, err)
	}
	return nil
}

func (s *Store) insertPrivacyStatus(ctx context.Context, request privacy.Request, status string, failureCode *string,
	attempts uint16, availableAt time.Time, leaseUntil *time.Time) error {
	err := s.connection.Exec(ctx, `INSERT INTO privacy_suppressions_v2 (
		request_id, product, product_key, subject_key, person_key, org_key, action, status,
		body_sha256, requested_at, ingested_at, updated_at, active, failure_code, expires_at,
		attempts, available_at, lease_until, legacy_installation_id
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, true, ?, ?, ?, ?, ?, ?)`,
		uuid.MustParse(request.RequestID), string(request.Scope.Product), &request.ProductKey, request.Scope.SubjectKey,
		request.Scope.PersonKey, request.Scope.OrgKey, string(request.Action), status, request.BodySHA256,
		request.RequestedAt, request.IngestedAt, privacyStatusVersion(request, status, attempts), failureCode, request.ExpiresAt, attempts, availableAt, leaseUntil,
		request.LegacyInstallationID,
	)
	if err != nil {
		return fmt.Errorf("insert privacy request status %s: %w", status, err)
	}
	return nil
}

func privacyStatusVersion(request privacy.Request, status string, attempts uint16) time.Time {
	phase := time.Duration(1)
	switch status {
	case "retry", "failed":
		phase = 2
	case "completed":
		phase = 3
	}
	return request.IngestedAt.Add((time.Duration(attempts)*4 + phase) * time.Millisecond)
}

func privacyRetryDelay(attempt uint16) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	return time.Duration(1<<uint(attempt-1)) * time.Second
}
