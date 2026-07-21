package clickhouse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/linkasu/linka.plays-metric/internal/contract/v1"
	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
	"github.com/linkasu/linka.plays-metric/internal/privacy"
)

func TestV2StoreIdempotencyAndSuppressionIntegration(t *testing.T) {
	address := os.Getenv("CLICKHOUSE_INTEGRATION_ADDR")
	if address == "" {
		t.Skip("CLICKHOUSE_INTEGRATION_ADDR is not configured")
	}
	store, err := Open(Config{
		Addresses: []string{address}, Database: "linka_metric", Username: os.Getenv("CLICKHOUSE_INTEGRATION_USER"),
		Password: os.Getenv("CLICKHOUSE_INTEGRATION_PASSWORD"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	store.now = func() time.Time { return now }
	subjectDigest := sha256.Sum256([]byte(uuid.NewString()))
	subjectKey := hex.EncodeToString(subjectDigest[:])
	batchID := uuid.NewString()
	recordID := uuid.NewString()
	body := integrationProductBatch(batchID, recordID, subjectKey, now, "1")
	batch, err := v2.ParseBatch(body, now)
	if err != nil {
		t.Fatal(err)
	}

	result, err := store.InsertV2(ctx, batch, v2.BodySHA256(body))
	if err != nil || result.Replayed || result.Count != 1 {
		t.Fatalf("first insert result = %+v, error = %v", result, err)
	}
	result, err = store.InsertV2(ctx, batch, v2.BodySHA256(body))
	if err != nil || !result.Replayed {
		t.Fatalf("replay result = %+v, error = %v", result, err)
	}
	changedBody := integrationProductBatch(batchID, uuid.NewString(), subjectKey, now, "2")
	changedBatch, err := v2.ParseBatch(changedBody, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertV2(ctx, changedBatch, v2.BodySHA256(changedBody)); !errors.Is(err, v2.ErrIdempotencyConflict) {
		t.Fatalf("changed body error = %v", err)
	}
	duplicateBody := integrationProductBatch(uuid.NewString(), recordID, subjectKey, now, "1")
	duplicateBatch, err := v2.ParseBatch(duplicateBody, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertV2(ctx, duplicateBatch, v2.BodySHA256(duplicateBody)); !errors.Is(err, v2.ErrDuplicateRecord) {
		t.Fatalf("duplicate record error = %v", err)
	}

	privacyBody := []byte(fmt.Sprintf(`{"schema_version":2,"request_id":%q,"scope":{"product":"linka-looks","subject_key":%q},"action":"delete","requested_at":%q}`,
		uuid.NewString(), subjectKey, now.Format(time.RFC3339)))
	privacyRequest, err := v2.ParsePrivacyRequest(privacyBody, now)
	if err != nil {
		t.Fatal(err)
	}
	privacyResult, err := store.CreatePrivacyRequest(ctx, privacyRequest, v2.BodySHA256(privacyBody))
	if err != nil || privacyResult.Status != "pending" {
		t.Fatalf("privacy result = %+v, error = %v", privacyResult, err)
	}

	suppressedBody := integrationProductBatch(uuid.NewString(), uuid.NewString(), subjectKey, now, "1")
	suppressedBatch, err := v2.ParseBatch(suppressedBody, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertV2(ctx, suppressedBatch, v2.BodySHA256(suppressedBody)); !errors.Is(err, v2.ErrSuppressed) {
		t.Fatalf("suppressed insert error = %v", err)
	}
	pending, err := store.PendingPrivacyRequests(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending privacy requests = %#v, error = %v", pending, err)
	}
	claimed, err := store.ClaimPrivacyRequest(ctx, pending[0])
	if err != nil || !claimed {
		t.Fatalf("claim privacy request = %v, error = %v", claimed, err)
	}
	pending[0].Attempts++
	if err := store.DeleteTelemetryRequest(ctx, pending[0]); err != nil {
		t.Fatalf("delete telemetry request: %v", err)
	}
	if err := store.CompletePrivacyRequest(ctx, pending[0]); err != nil {
		t.Fatalf("complete privacy request: %v", err)
	}
	privacyResult, err = store.CreatePrivacyRequest(ctx, privacyRequest, v2.BodySHA256(privacyBody))
	if err != nil || !privacyResult.Replayed || privacyResult.Status != "completed" {
		t.Fatalf("completed privacy receipt = %+v, error = %v", privacyResult, err)
	}
	var progressCount uint64
	if err := store.connection.QueryRow(ctx, `
		SELECT count() FROM privacy_deletion_progress_v2 FINAL
		WHERE request_id = ? AND status = 'completed'`, uuid.MustParse(privacyRequest.RequestID)).Scan(&progressCount); err != nil || progressCount != 7 {
		t.Fatalf("completed privacy table progress = %d, error = %v", progressCount, err)
	}
	var eventCount uint64
	if err := store.connection.QueryRow(ctx, `SELECT count() FROM product_events_v2 WHERE record_id = ?`, uuid.MustParse(batch.ProductRecords[0].RecordID)).Scan(&eventCount); err != nil || eventCount != 0 {
		t.Fatalf("remaining deleted records = %d, error = %v", eventCount, err)
	}
	if err := store.connection.QueryRow(ctx, `SELECT count() FROM record_registry_v2 WHERE record_id = ?`, uuid.MustParse(batch.ProductRecords[0].RecordID)).Scan(&eventCount); err != nil || eventCount != 0 {
		t.Fatalf("remaining deleted record registry rows = %d, error = %v", eventCount, err)
	}
}

var _ privacy.Repository = (*Store)(nil)

func TestV1PrivacyDeletionAndSuppressionIntegration(t *testing.T) {
	address := os.Getenv("CLICKHOUSE_INTEGRATION_ADDR")
	if address == "" {
		t.Skip("CLICKHOUSE_INTEGRATION_ADDR is not configured")
	}
	store, err := Open(Config{
		Addresses: []string{address}, Database: "linka_metric", Username: os.Getenv("CLICKHOUSE_INTEGRATION_USER"),
		Password: os.Getenv("CLICKHOUSE_INTEGRATION_PASSWORD"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	store.now = func() time.Time { return now }
	installationID := uuid.NewString()
	eventID := uuid.NewString()
	batch, err := v1.ParseBatch(integrationV1Batch(eventID, installationID, now))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(ctx, batch); err != nil {
		t.Fatalf("insert V1 telemetry: %v", err)
	}
	requestID := uuid.NewString()
	privacyBody := []byte(fmt.Sprintf(`{"schema_version":1,"request_id":%q,"installation_id":%q,"action":"delete","requested_at":%q}`,
		requestID, installationID, now.Format(time.RFC3339)))
	privacyRequest, err := v1.ParseInternalPrivacyRequest(privacyBody, now)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.CreateLegacyPrivacyRequest(ctx, privacyRequest, v2.BodySHA256(privacyBody))
	if err != nil || result.Status != "pending" {
		t.Fatalf("create V1 privacy request: result=%+v err=%v", result, err)
	}
	if err := store.Insert(ctx, batch); !errors.Is(err, v2.ErrSuppressed) {
		t.Fatalf("post-suppression V1 insert error = %v", err)
	}
	var wait sync.WaitGroup
	errorsAfterSuppression := make(chan error, 16)
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsAfterSuppression <- store.Insert(ctx, batch)
		}()
	}
	wait.Wait()
	close(errorsAfterSuppression)
	for insertErr := range errorsAfterSuppression {
		if !errors.Is(insertErr, v2.ErrSuppressed) {
			t.Fatalf("concurrent post-suppression V1 insert error = %v", insertErr)
		}
	}
	pending, err := store.PendingPrivacyRequests(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var deletion *privacy.Request
	for index := range pending {
		if pending[index].RequestID == requestID {
			deletion = &pending[index]
			break
		}
	}
	if deletion == nil || deletion.LegacyInstallationID == nil {
		t.Fatalf("V1 deletion not pending: %#v", pending)
	}
	claimed, err := store.ClaimPrivacyRequest(ctx, *deletion)
	if err != nil || !claimed {
		t.Fatalf("claim V1 deletion: claimed=%v err=%v", claimed, err)
	}
	deletion.Attempts++
	if err := store.DeleteTelemetryRequest(ctx, *deletion); err != nil {
		t.Fatalf("delete V1 telemetry: %v", err)
	}
	if err := store.CompletePrivacyRequest(ctx, *deletion); err != nil {
		t.Fatalf("complete V1 deletion: %v", err)
	}
	var eventCount, progressCount uint64
	if err := store.connection.QueryRow(ctx, `SELECT count() FROM events WHERE event_id = ?`, uuid.MustParse(eventID)).Scan(&eventCount); err != nil || eventCount != 0 {
		t.Fatalf("remaining V1 events = %d, err=%v", eventCount, err)
	}
	if err := store.connection.QueryRow(ctx, `
		SELECT count() FROM privacy_deletion_progress_v2 FINAL
		WHERE request_id = ? AND status = 'completed'`, uuid.MustParse(requestID)).Scan(&progressCount); err != nil || progressCount != 2 {
		t.Fatalf("completed V1 deletion progress = %d, err=%v", progressCount, err)
	}
}

func TestV2BatchReservationResumesSameBodyAndRejectsChangedBodyIntegration(t *testing.T) {
	address := os.Getenv("CLICKHOUSE_INTEGRATION_ADDR")
	if address == "" {
		t.Skip("CLICKHOUSE_INTEGRATION_ADDR is not configured")
	}
	store, err := Open(Config{
		Addresses: []string{address}, Database: "linka_metric", Username: os.Getenv("CLICKHOUSE_INTEGRATION_USER"),
		Password: os.Getenv("CLICKHOUSE_INTEGRATION_PASSWORD"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	store.now = func() time.Time { return now }
	subjectDigest := sha256.Sum256([]byte(uuid.NewString()))
	subjectKey := hex.EncodeToString(subjectDigest[:])
	batchID, recordID := uuid.NewString(), uuid.NewString()
	body := integrationBatch(batchID, recordID, subjectKey, now, "1")
	batch, err := v2.ParseBatch(body, now)
	if err != nil {
		t.Fatal(err)
	}
	bodySHA := v2.BodySHA256(body)
	reservedAt := now.UTC().Truncate(time.Millisecond)
	if err := store.insertBatchLedgerV2(ctx, batch, bodySHA, reservedAt, "reserved"); err != nil {
		t.Fatal(err)
	}
	if err := store.registerRecordsV2(ctx, batch, bodySHA, reservedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.insertCommonV2(ctx, batch, reservedAt); err != nil {
		t.Fatal(err)
	}

	changedBody := integrationBatch(batchID, uuid.NewString(), subjectKey, now, "2")
	changedBatch, err := v2.ParseBatch(changedBody, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertV2(ctx, changedBatch, v2.BodySHA256(changedBody)); !errors.Is(err, v2.ErrIdempotencyConflict) {
		t.Fatalf("changed body after reservation error = %v", err)
	}
	result, err := store.InsertV2(ctx, batch, bodySHA)
	if err != nil || result.Replayed || result.Count != 1 {
		t.Fatalf("same-body resume result=%+v err=%v", result, err)
	}
	var status string
	if err := store.connection.QueryRow(ctx, `SELECT status FROM ingest_batches_v2 FINAL WHERE product = ? AND batch_id = ?`,
		string(batch.Header.Scope.Product), uuid.MustParse(batchID)).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("batch ledger status=%s err=%v", status, err)
	}
	var count uint64
	if err := store.connection.QueryRow(ctx, `SELECT count() FROM common_events_v2 FINAL WHERE product = ? AND record_id = ?`,
		string(batch.Header.Scope.Product), uuid.MustParse(recordID)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("resumed record count=%d err=%v", count, err)
	}
}

func integrationV1Batch(eventID, installationID string, now time.Time) []byte {
	return []byte(fmt.Sprintf(`{
  "schema_version":1,
  "events":[{
    "event_id":%q,
    "event_name":"app_started",
    "occurred_at":%q,
    "installation_id":%q,
    "app_session_id":%q,
    "app":{"version":"1","build":"1","platform":"linux","os_version":"1","locale":"ru"},
    "properties":{}
  }]
}`, eventID, now.Format(time.RFC3339), installationID, uuid.NewString()))
}

func integrationBatch(batchID, recordID, subjectKey string, now time.Time, version string) []byte {
	return []byte(fmt.Sprintf(`{
  "schema_version":2,
  "batch_id":%q,
  "scope":{"product":"linka-plays","subject_key":%q},
  "stream":"common",
  "sent_at":%q,
  "records":[{
    "record_id":%q,
    "occurred_at":%q,
    "kind":"app_started",
    "app_session_id":%q,
    "app":{"version":%q,"build":"1","platform":"linux","os_version":"1","locale":"ru"}
  }]
}`, batchID, subjectKey, now.Format(time.RFC3339), recordID, now.Add(-time.Minute).Format(time.RFC3339), uuid.NewString(), version))
}

func integrationProductBatch(batchID, recordID, subjectKey string, now time.Time, version string) []byte {
	return []byte(fmt.Sprintf(`{
  "schema_version":2,
  "batch_id":%q,
  "scope":{"product":"linka-looks","subject_key":%q},
  "stream":"product",
  "sent_at":%q,
  "records":[{
    "record_id":%q,
    "occurred_at":%q,
    "kind":"start",
    "app_session_id":%q,
    "app":{"version":%q,"build":"1","platform":"windows","os_version":"11","locale":"ru"}
  }]
}`, batchID, subjectKey, now.Format(time.RFC3339), recordID, now.Add(-time.Minute).Format(time.RFC3339), uuid.NewString(), version))
}
