package clickhouse

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
	"github.com/linkasu/linka.plays-metric/internal/privacy"
	"github.com/linkasu/linka.plays-metric/migrations"
)

func retentionIntegrationStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	address := os.Getenv("CLICKHOUSE_INTEGRATION_ADDR")
	if address == "" {
		t.Skip("CLICKHOUSE_INTEGRATION_ADDR is not configured")
	}
	config := Config{
		Addresses: []string{address}, Database: "default",
		Username: os.Getenv("CLICKHOUSE_INTEGRATION_USER"), Password: os.Getenv("CLICKHOUSE_INTEGRATION_PASSWORD"),
		Retention: Retention{
			IngestBatches: 180 * 24 * time.Hour, Common: 180 * 24 * time.Hour,
			Technical: 180 * 24 * time.Hour, Plays: 180 * 24 * time.Hour,
			Product: 180 * 24 * time.Hour, Outcome: 180 * 24 * time.Hour, Registry: 180 * 24 * time.Hour,
			Privacy: 1095 * 24 * time.Hour, DeletionProgress: 1095 * 24 * time.Hour,
		},
	}
	admin, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	loaded, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	database := "retention_integration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.connection.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := admin.connection.Exec(cleanup, "DROP DATABASE "+database+" SYNC"); err != nil {
			t.Errorf("drop isolated test database: %v", err)
		}
	})
	for _, migration := range loaded {
		for _, statement := range migration.Statements {
			if err := admin.connection.Exec(ctx, strings.ReplaceAll(statement, "linka_metric", database)); err != nil {
				t.Fatalf("apply %s: %v", migration.Name, err)
			}
		}
	}
	config.Database = database
	store, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, ctx
}

func TestRetentionSchemaAndRawTTLIntegration(t *testing.T) {
	store, ctx := retentionIntegrationStore(t)
	for _, table := range []string{
		"ingest_batches_v2", "record_registry_v2", "privacy_suppressions_v2", "privacy_deletion_progress_v2",
		"common_events_v2", "technical_events_v2", "plays_events_v2", "product_events_v2", "product_outcomes_v2",
	} {
		t.Run(table, func(t *testing.T) {
			var definition string
			if err := store.connection.QueryRow(ctx, "SHOW CREATE TABLE "+table).Scan(&definition); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(definition, "TTL ") {
				t.Errorf("table has no TTL: %s", definition)
			}
			var expiryType string
			if err := store.connection.QueryRow(ctx, `SELECT type FROM system.columns
				WHERE database = currentDatabase() AND table = ? AND name = 'expires_at'`, table).Scan(&expiryType); err != nil {
				t.Fatal(err)
			}
			if expiryType != "Nullable(DateTime64(3, 'UTC'))" {
				t.Errorf("expires_at type = %q", expiryType)
			}
		})
	}
	for _, fixture := range []struct{ table, view string }{
		{"product_events_v2", "datalens_product_v2"},
		{"product_outcomes_v2", "datalens_outcomes_v1"},
	} {
		t.Run(fixture.view, func(t *testing.T) {
			if err := store.connection.Exec(ctx, "SYSTEM STOP MERGES "+fixture.table); err != nil {
				t.Fatal(err)
			}
			// Keep expired rows physically present so the view cannot pass by relying on merges.
			for _, expiry := range []string{"now64(3) - INTERVAL 1 DAY", "NULL", "now64(3) + INTERVAL 180 DAY"} {
				if err := store.connection.Exec(ctx, `INSERT INTO `+fixture.table+`
					(product, subject_key, record_id, occurred_at, ingested_at, expires_at)
					VALUES ('linka-looks', ?, ?, now64(3), now64(3), `+expiry+`)`, strings.Repeat("a", 64), uuid.New()); err != nil {
					t.Fatal(err)
				}
			}
			var raw, visible uint64
			if err := store.connection.QueryRow(ctx, "SELECT count() FROM "+fixture.table+" FINAL").Scan(&raw); err != nil || raw != 3 {
				t.Fatalf("before TTL raw=%d, want 3; err=%v", raw, err)
			}
			if err := store.connection.QueryRow(ctx, "SELECT count() FROM "+fixture.view).Scan(&visible); err != nil || visible != 2 {
				t.Errorf("before TTL visible=%d, want NULL history + unexpired row; err=%v", visible, err)
			}
			if err := store.connection.Exec(ctx, "SYSTEM START MERGES "+fixture.table); err != nil {
				t.Fatal(err)
			}
			if err := store.connection.Exec(ctx, "ALTER TABLE "+fixture.table+" MATERIALIZE TTL SETTINGS mutations_sync = 2"); err != nil {
				t.Fatal(err)
			}
			var historical uint64
			if err := store.connection.QueryRow(ctx, "SELECT count(), countIf(expires_at IS NULL) FROM "+fixture.table+" FINAL").Scan(&raw, &historical); err != nil || raw != 2 || historical != 1 {
				t.Errorf("after TTL raw=%d historical=%d, want 2/1; err=%v", raw, historical, err)
			}
		})
	}
}

func TestRetentionPrivacyTTLIntegration(t *testing.T) {
	store, ctx := retentionIntegrationStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	subject := strings.Repeat("b", 64)
	body := integrationProductBatch(uuid.NewString(), uuid.NewString(), subject, now, "1")
	batch, err := v2.ParseBatch(body, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertV2(ctx, batch, v2.BodySHA256(body)); err != nil {
		t.Fatal(err)
	}
	if err := store.connection.Exec(ctx, "ALTER TABLE product_events_v2 UPDATE expires_at = NULL WHERE 1 SETTINGS mutations_sync = 2"); err != nil {
		t.Fatal(err)
	}
	var beforeSuppression uint64
	if err := store.connection.QueryRow(ctx, "SELECT count() FROM datalens_product_v2").Scan(&beforeSuppression); err != nil || beforeSuppression != 1 {
		t.Fatalf("NULL history before suppression=%d, want 1; err=%v", beforeSuppression, err)
	}
	privacyBody := []byte(fmt.Sprintf(`{"schema_version":2,"request_id":%q,"scope":{"product":"linka-looks","subject_key":%q},"action":"opt_out","requested_at":%q}`,
		uuid.NewString(), subject, now.Format(time.RFC3339)))
	request, err := v2.ParsePrivacyRequest(privacyBody, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePrivacyRequest(ctx, request, v2.BodySHA256(privacyBody)); err != nil {
		t.Fatal(err)
	}
	var expiry *time.Time
	if err := store.connection.QueryRow(ctx, "SELECT expires_at FROM privacy_suppressions_v2 FINAL WHERE request_id = ?", uuid.MustParse(request.RequestID)).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	if expiry != nil {
		t.Errorf("new active opt_out expiry = %v, want NULL", expiry)
	}
	if err := store.connection.Exec(ctx, "SYSTEM STOP MERGES privacy_suppressions_v2"); err != nil {
		t.Fatal(err)
	}
	// This is a persisted old opt-out, not a request created by the new writer.
	if err := store.connection.Exec(ctx, `INSERT INTO privacy_suppressions_v2
		SELECT * REPLACE ('completed' AS status, now64(3) + INTERVAL 1 SECOND AS updated_at,
			now64(3) - INTERVAL 1 DAY AS expires_at)
		FROM privacy_suppressions_v2 FINAL WHERE request_id = ?`, uuid.MustParse(request.RequestID)); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"pending", "processing", "retry", "failed", "completed"} {
		if err := store.connection.Exec(ctx, `INSERT INTO privacy_suppressions_v2
			(request_id, product, subject_key, action, status, active, ingested_at, updated_at, expires_at)
			VALUES (?, 'linka-looks', ?, 'delete', ?, false, now64(3), now64(3), now64(3) - INTERVAL 1 DAY)`,
			uuid.New(), strings.Repeat("c", 64), status); err != nil {
			t.Fatal(err)
		}
		if err := store.connection.Exec(ctx, `INSERT INTO privacy_deletion_progress_v2
			(request_id, product, table_name, status, updated_at, completed_at, expires_at)
			VALUES (?, 'linka-looks', 'product_events_v2', ?, now64(3), ?, now64(3) - INTERVAL 1 DAY)`,
			uuid.New(), status, now.Add(-24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"privacy_suppressions_v2", "privacy_deletion_progress_v2"} {
		if err := store.connection.Exec(ctx, "SYSTEM START MERGES "+table); err != nil {
			t.Fatal(err)
		}
		if err := store.connection.Exec(ctx, "ALTER TABLE "+table+" MATERIALIZE TTL SETTINGS mutations_sync = 2"); err != nil {
			t.Fatal(err)
		}
		var nonterminal, completed uint64
		if err := store.connection.QueryRow(ctx, "SELECT countIf(status != 'completed'), countIf(status = 'completed') FROM "+table+" FINAL").Scan(&nonterminal, &completed); err != nil {
			t.Fatal(err)
		}
		wantCompleted := uint64(0)
		if table == "privacy_suppressions_v2" {
			wantCompleted = 1 // The completed active opt-out is permanent.
		}
		if nonterminal != 4 || completed != wantCompleted {
			t.Errorf("%s after TTL: unfinished=%d completed=%d, want 4/%d", table, nonterminal, completed, wantCompleted)
		}
	}
	var raw, visible uint64
	if err := store.connection.QueryRow(ctx, "SELECT countIf(expires_at IS NULL) FROM product_events_v2 FINAL").Scan(&raw); err != nil || raw != 1 {
		t.Fatalf("historical raw=%d, want 1; err=%v", raw, err)
	}
	if err := store.connection.QueryRow(ctx, "SELECT count() FROM datalens_product_v2").Scan(&visible); err != nil || visible != 0 {
		t.Errorf("suppressed history visible=%d, want 0; err=%v", visible, err)
	}
	body = integrationProductBatch(uuid.NewString(), uuid.NewString(), subject, now, "1")
	batch, err = v2.ParseBatch(body, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertV2(ctx, batch, v2.BodySHA256(body)); err != v2.ErrSuppressed {
		t.Errorf("opt_out after TTL: insert error=%v, want suppressed", err)
	}
}

func TestRetentionPrivacyWorkerCompletionIntegration(t *testing.T) {
	store, ctx := retentionIntegrationStore(t)
	worker, err := privacy.NewWorker(store, store, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			completion := time.Now().UTC().Truncate(time.Millisecond)
			created := completion.Add(-48 * time.Hour)
			store.now = func() time.Time { return created }
			body := []byte(fmt.Sprintf(`{"schema_version":2,"request_id":%q,"scope":{"product":"linka-looks","subject_key":%q},"action":"delete","requested_at":%q}`,
				uuid.NewString(), v2.BodySHA256([]byte(uuid.NewString())), created.Format(time.RFC3339)))
			request, err := v2.ParsePrivacyRequest(body, created)
			if err != nil {
				t.Fatal(err)
			}
			result, err := store.CreatePrivacyRequest(ctx, request, v2.BodySHA256(body))
			if err != nil || result.Replayed || result.Status != "pending" {
				t.Fatalf("create delete request: result=%+v err=%v", result, err)
			}
			requestID := uuid.MustParse(request.RequestID)
			if legacy {
				// Persist the pre-retention NULL marker before the new worker resumes it.
				if err := store.connection.Exec(ctx, `ALTER TABLE privacy_suppressions_v2
					UPDATE expires_at = NULL WHERE request_id = ? SETTINGS mutations_sync = 2`, requestID); err != nil {
					t.Fatal(err)
				}
			}
			var initialNull uint8
			if err := store.connection.QueryRow(ctx, `SELECT isNull(expires_at) FROM privacy_suppressions_v2 FINAL
				WHERE request_id = ?`, requestID).Scan(&initialNull); err != nil {
				t.Fatal(err)
			}
			if (initialNull == 1) != legacy {
				t.Errorf("before worker: legacy=%v expiry IS NULL=%d", legacy, initialNull)
			}
			store.now = func() time.Time { return completion }
			if err := worker.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			result, err = store.CreatePrivacyRequest(ctx, request, v2.BodySHA256(body))
			if err != nil || !result.Replayed || result.Status != "completed" {
				t.Fatalf("completed privacy receipt: result=%+v err=%v", result, err)
			}
			wantExpiry := completion.Add(1095 * 24 * time.Hour)
			var receiptNull uint8
			var receiptExpiry int64
			if err := store.connection.QueryRow(ctx, `SELECT isNull(expires_at), ifNull(toUnixTimestamp64Milli(expires_at), 0)
				FROM privacy_suppressions_v2 FINAL WHERE request_id = ?`, requestID).Scan(&receiptNull, &receiptExpiry); err != nil {
				t.Fatal(err)
			}
			if legacy {
				if receiptNull != 1 {
					t.Errorf("legacy receipt expiry=%v, want NULL", receiptExpiry)
				}
			} else if receiptNull != 0 || receiptExpiry != wantExpiry.UnixMilli() {
				t.Errorf("new receipt expiry=%v, want completion + 1095d (%v)", receiptExpiry, wantExpiry)
			}
			rows, err := store.connection.Query(ctx, `SELECT table_name, status,
				isNull(completed_at), ifNull(toUnixTimestamp64Milli(completed_at), 0),
				isNull(expires_at), ifNull(toUnixTimestamp64Milli(expires_at), 0)
				FROM privacy_deletion_progress_v2 FINAL WHERE request_id = ?`, requestID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			remaining := map[string]bool{
				"common_events_v2": true, "technical_events_v2": true, "plays_events_v2": true,
				"product_events_v2": true, "product_outcomes_v2": true, "record_registry_v2": true, "ingest_batches_v2": true,
			}
			for rows.Next() {
				var table, status string
				var completedNull, expiryNull uint8
				var completedAt, expiry int64
				if err := rows.Scan(&table, &status, &completedNull, &completedAt, &expiryNull, &expiry); err != nil {
					t.Fatal(err)
				}
				if !remaining[table] {
					t.Errorf("unexpected or duplicate progress table %q", table)
				}
				delete(remaining, table)
				if status != "completed" || completedNull != 0 || completedAt != completion.UnixMilli() {
					t.Errorf("%s progress status=%q completed_at=%v, want completed at %v", table, status, completedAt, completion)
				}
				if legacy {
					if expiryNull != 1 {
						t.Errorf("%s legacy progress expiry=%v, want NULL", table, expiry)
					}
				} else if expiryNull != 0 || expiry != wantExpiry.UnixMilli() {
					t.Errorf("%s new progress expiry=%v, want completion + 1095d (%v)", table, expiry, wantExpiry)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if len(remaining) != 0 {
				t.Errorf("missing completed deletion progress: %v", remaining)
			}
		})
	}
}

func TestRetentionLegacyReservationRetryIntegration(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%v", partial), func(t *testing.T) {
			store, ctx := retentionIntegrationStore(t)
			now := time.Now().UTC().Truncate(time.Millisecond)
			body := integrationBatch(uuid.NewString(), uuid.NewString(), strings.Repeat("d", 64), now, "1")
			batch, err := v2.ParseBatch(body, now)
			if err != nil {
				t.Fatal(err)
			}
			bodySHA := v2.BodySHA256(body)
			batchID := uuid.MustParse(batch.Header.BatchID)
			record := batch.CommonRecords[0]
			recordID := uuid.MustParse(record.RecordID)
			reservedAt := now.Add(-time.Hour)
			if err := store.connection.Exec(ctx, `INSERT INTO ingest_batches_v2
				(batch_id, idempotency_key, product, product_key, subject_key, stream, body_sha256,
				 record_count, sent_at, ingested_at, expires_at, status)
				VALUES (?, ?, 'linka-plays', ?, ?, 'common', ?, 1, ?, ?, NULL, 'reserved')`,
				batchID, batchID, batch.ProductKey, batch.Header.Scope.SubjectKey, bodySHA, batch.SentAtTime, reservedAt); err != nil {
				t.Fatal(err)
			}
			if partial {
				if err := store.connection.Exec(ctx, `INSERT INTO record_registry_v2
					(product, record_id, batch_id, stream, body_sha256, registered_at, product_key, subject_key, expires_at)
					VALUES ('linka-plays', ?, ?, 'common', ?, ?, ?, ?, NULL)`,
					recordID, batchID, bodySHA, reservedAt, batch.ProductKey, batch.Header.Scope.SubjectKey); err != nil {
					t.Fatal(err)
				}
				if err := store.connection.Exec(ctx, `INSERT INTO common_events_v2
					(product, product_key, subject_key, batch_id, record_id, occurred_at, kind, app_session_id,
					 app_version, app_build, platform, os_version, locale, ingested_at, expires_at)
					VALUES ('linka-plays', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
					batch.ProductKey, batch.Header.Scope.SubjectKey, batchID, recordID, record.OccurredAtTime, record.Kind,
					uuid.MustParse(record.AppSessionID), record.App.Version, record.App.Build, record.App.Platform,
					record.App.OSVersion, record.App.Locale, reservedAt); err != nil {
					t.Fatal(err)
				}
			}
			result, err := store.InsertV2(ctx, batch, bodySHA)
			if err != nil || result.Replayed || result.Count != 1 {
				t.Fatalf("resume result=%+v err=%v", result, err)
			}
			result, err = store.InsertV2(ctx, batch, bodySHA)
			if err != nil || !result.Replayed || result.Count != 1 {
				t.Fatalf("replay result=%+v err=%v", result, err)
			}
			for _, table := range []string{"ingest_batches_v2", "record_registry_v2", "common_events_v2"} {
				var rows, nullExpiry uint64
				if err := store.connection.QueryRow(ctx, "SELECT count(), countIf(expires_at IS NULL) FROM "+table+" FINAL").Scan(&rows, &nullExpiry); err != nil {
					t.Fatal(err)
				}
				if rows != 1 || nullExpiry != 1 {
					t.Errorf("%s retry rows=%d NULL expiry=%d, want 1/1", table, rows, nullExpiry)
				}
			}
			var status string
			if err := store.connection.QueryRow(ctx, "SELECT status FROM ingest_batches_v2 FINAL").Scan(&status); err != nil || status != "completed" {
				t.Errorf("ledger status=%q, want completed; err=%v", status, err)
			}
		})
	}
}

func TestRetentionNewBatchExpiryIntegration(t *testing.T) {
	store, ctx := retentionIntegrationStore(t)
	now := time.Now().UTC().Truncate(time.Second).Add(123 * time.Millisecond)
	store.now = func() time.Time { return now }
	body := integrationProductBatch(uuid.NewString(), uuid.NewString(), strings.Repeat("e", 64), now, "1")
	batch, err := v2.ParseBatch(body, now)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		result, err := store.InsertV2(ctx, batch, v2.BodySHA256(body))
		if err != nil || result.Replayed != (attempt == 1) {
			t.Fatalf("ingest attempt %d: %+v %v", attempt, result, err)
		}
		for _, table := range []string{"ingest_batches_v2", "record_registry_v2", "product_events_v2"} {
			var missing uint8
			var expiry int64
			if err := store.connection.QueryRow(ctx, "SELECT isNull(expires_at), ifNull(toUnixTimestamp64Milli(expires_at),0) FROM "+table+" FINAL").Scan(&missing, &expiry); err != nil {
				t.Fatal(err)
			}
			if missing != 0 || expiry != now.Add(180*24*time.Hour).UnixMilli() {
				t.Errorf("%s expiry=%d nullable=%d, want exact ingestion+180d", table, expiry, missing)
			}
		}
	}
}
