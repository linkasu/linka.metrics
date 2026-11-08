package clickhouse

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/linkasu/linka.plays-metric/internal/analyticsapi"
	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
	"github.com/linkasu/linka.plays-metric/internal/privacy"
	"github.com/linkasu/linka.plays-metric/internal/product"
)

func TestConsentLifecycleIntegration(t *testing.T) {
	store, ctx := retentionIntegrationStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	store.now = func() time.Time { return now }
	subject := strings.Repeat("a", 64)
	control := func(epoch, version uint64, preference string) (v2.ValidatedConsentRequest, string) {
		body := []byte(fmt.Sprintf(`{"schema_version":2,"request_id":%q,"scope":{"product":"linka-looks","subject_key":%q},"consent_epoch":%d,"version":%d,"preference":%q,"recorded_at":%q}`, uuid.NewString(), subject, epoch, version, preference, now.Format("2006-01-02T15:04:05.000Z")))
		request, err := v2.ParseConsentRequest(body)
		if err != nil {
			t.Fatal(err)
		}
		return request, v2.BodySHA256(body)
	}
	batch := func(epoch uint64) (v2.ValidatedBatch, string) {
		body := integrationProductBatch(uuid.NewString(), uuid.NewString(), subject, now, "1")
		if epoch > 0 {
			body = []byte(strings.Replace(string(body), `"scope":{`, fmt.Sprintf(`"scope":{"consent_epoch":%d,`, epoch), 1))
		}
		parsed, err := v2.ParseBatch(body, now)
		if err != nil {
			t.Fatal(err)
		}
		return parsed, v2.BodySHA256(body)
	}
	legacy, legacySHA := batch(0)
	if _, err := store.InsertV2(ctx, legacy, legacySHA); err != nil {
		t.Fatal(err)
	}
	grant1, grant1SHA := control(1, 1, "allowed")
	if _, err := store.CreateConsent(ctx, grant1, grant1SHA); err != nil {
		t.Fatal(err)
	}
	first, firstSHA := batch(1)
	if _, err := store.InsertV2(ctx, first, firstSHA); err != nil {
		t.Fatal(err)
	}
	if result, err := store.InsertV2(ctx, first, firstSHA); err != nil || !result.Replayed {
		t.Fatalf("batch replay=%+v %v", result, err)
	}
	if _, err := store.InsertV2(ctx, first, "changed-body"); !errors.Is(err, v2.ErrIdempotencyConflict) {
		t.Fatalf("changed batch=%v", err)
	}
	refresh, refreshSHA := control(1, 2, "allowed")
	if result, err := store.CreateConsent(ctx, refresh, refreshSHA); err != nil || result.Status != "completed" || result.Replayed {
		t.Fatalf("same-epoch allowed refresh=%+v %v", result, err)
	}
	if result, err := store.InsertV2(ctx, first, firstSHA); err != nil || !result.Replayed {
		t.Fatalf("refresh interrupted active epoch=%+v %v", result, err)
	}
	var afterRefresh uint64
	if err := store.connection.QueryRow(ctx, `SELECT count() FROM datalens_product_v2`).Scan(&afterRefresh); err != nil || afterRefresh != 1 {
		t.Fatalf("refresh exposed hidden legacy history: %d %v", afterRefresh, err)
	}
	revision1, err := store.analyticsPrivacyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revoke, revokeSHA := control(1, 3, "denied")
	if _, err := store.CreateConsent(ctx, revoke, revokeSHA); err != nil {
		t.Fatal(err)
	}
	revision2, err := store.analyticsPrivacyRevision(ctx)
	if err != nil || revision1 == revision2 {
		t.Fatalf("consent did not invalidate snapshot: %v", err)
	}
	if _, err := store.InsertV2(ctx, first, firstSHA); !errors.Is(err, v2.ErrSuppressed) {
		t.Fatalf("revoked completed replay=%v", err)
	}
	reopen, reopenSHA := control(1, 4, "allowed")
	if _, err := store.CreateConsent(ctx, reopen, reopenSHA); !errors.Is(err, v2.ErrConsentConflict) {
		t.Fatalf("reopened closed epoch: %v", err)
	}
	if result, err := store.CreateConsent(ctx, grant1, grant1SHA); err != nil || !result.Replayed {
		t.Fatalf("old receipt=%+v %v", result, err)
	}
	if _, err := store.InsertV2(ctx, first, firstSHA); !errors.Is(err, v2.ErrSuppressed) {
		t.Fatalf("historical receipt reopened revoked epoch: %v", err)
	}
	staleGrant, staleGrantSHA := control(1, 1, "allowed")
	if result, err := store.CreateConsent(ctx, staleGrant, staleGrantSHA); err != nil || result.Status != "completed" {
		t.Fatalf("delayed grant receipt=%+v %v", result, err)
	}
	if _, err := store.InsertV2(ctx, first, firstSHA); !errors.Is(err, v2.ErrSuppressed) {
		t.Fatalf("delayed grant reopened revoked epoch: %v", err)
	}
	if _, err := store.CreateConsent(ctx, grant1, "changed-body"); !errors.Is(err, v2.ErrIdempotencyConflict) {
		t.Fatalf("changed control=%v", err)
	}
	grant2, grant2SHA := control(2, 4, "allowed")
	if _, err := store.CreateConsent(ctx, grant2, grant2SHA); err != nil {
		t.Fatal(err)
	}
	// A delayed revoke is acknowledged durably, never deadlettered or reapplied.
	stale, staleSHA := control(1, 3, "denied")
	if result, err := store.CreateConsent(ctx, stale, staleSHA); err != nil || result.Status != "completed" {
		t.Fatalf("stale revoke=%+v %v", result, err)
	}
	for _, old := range []struct {
		b   v2.ValidatedBatch
		sha string
	}{{legacy, legacySHA}, {first, firstSHA}} {
		if _, err := store.InsertV2(ctx, old.b, old.sha); !errors.Is(err, v2.ErrSuppressed) {
			t.Fatalf("old/legacy batch reaccepted: %v", err)
		}
	}
	second, secondSHA := batch(2)
	if _, err := store.InsertV2(ctx, second, secondSHA); err != nil {
		t.Fatal(err)
	}
	var raw, visible, subjects uint64
	if err := store.connection.QueryRow(ctx, `SELECT count() FROM product_events_v2 FINAL`).Scan(&raw); err != nil || raw != 3 {
		t.Fatalf("raw=%d %v", raw, err)
	}
	if err := store.connection.QueryRow(ctx, `SELECT count(),uniqExact(subject_key) FROM datalens_product_v2`).Scan(&visible, &subjects); err != nil || visible != 1 || subjects != 1 {
		t.Fatalf("visible=%d subjects=%d %v", visible, subjects, err)
	}
	if err := store.connection.QueryRow(ctx, `SELECT sum(batch_count) FROM datalens_telemetry_quality_daily_v1`).Scan(&visible); err != nil || visible != 1 {
		t.Fatalf("quality exposed historical batches: %d %v", visible, err)
	}
	query := analyticsapi.Query{ContractVersion: 1, Products: []string{"linka-looks"}, Metrics: []string{"active_1d"}, CutoffDate: now.Format(time.DateOnly), SeriesFrom: now.Format(time.DateOnly), SeriesTo: now.Format(time.DateOnly)}
	report, err := store.ComputeAnalytics(ctx, query)
	if err != nil || len(report.Results) != 1 || report.Results[0].Value == nil || *report.Results[0].Value != 1 {
		t.Fatalf("consent cycles inflated analytical installs: %+v %v", report, err)
	}
	// Ingest and control contend on the same writer mutex. Every completed deny
	// must be authoritative even when an in-flight batch won the lock first.
	var wg sync.WaitGroup
	concurrentErrors := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := store.InsertV2(ctx, second, secondSHA); concurrentErrors <- err }()
	}
	revoke2, revoke2SHA := control(2, 5, "denied")
	if _, err := store.CreateConsent(ctx, revoke2, revoke2SHA); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(concurrentErrors)
	for err := range concurrentErrors {
		if err != nil && !errors.Is(err, v2.ErrSuppressed) {
			t.Fatalf("concurrent ingest: %v", err)
		}
	}
	if _, err := store.InsertV2(ctx, second, secondSHA); !errors.Is(err, v2.ErrSuppressed) {
		t.Fatalf("post-deny concurrent batch=%v", err)
	}
	report, err = store.ComputeAnalytics(ctx, query)
	if err != nil || len(report.Results) != 1 || report.Results[0].Value != nil {
		t.Fatalf("analytics exposed revoked history: %+v %v", report, err)
	}
	if err := store.connection.QueryRow(ctx, `SELECT sum(batch_count) FROM datalens_telemetry_quality_daily_v1`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("quality exposed revoked batches: %d %v", visible, err)
	}
	// Delete all epochs, retaining the minimal receipts and revocation tombstones.
	privacyBody := []byte(fmt.Sprintf(`{"schema_version":2,"request_id":%q,"scope":{"product":"linka-looks","subject_key":%q},"action":"delete","requested_at":%q}`, uuid.NewString(), subject, now.Format(time.RFC3339)))
	deletion, err := v2.ParsePrivacyRequest(privacyBody, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePrivacyRequest(ctx, deletion, v2.BodySHA256(privacyBody)); err != nil {
		t.Fatal(err)
	}
	request := privacy.Request{RequestID: deletion.RequestID, Scope: deletion.Scope, ProductKey: deletion.ProductKey, Action: deletion.Action, RequestedAt: deletion.RequestedAtTime, IngestedAt: now, Attempts: 1, BodySHA256: v2.BodySHA256(privacyBody)}
	if err := store.DeleteTelemetryRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.CompletePrivacyRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"product_events_v2", "record_registry_v2", "ingest_batches_v2"} {
		if err := store.connection.QueryRow(ctx, "SELECT count() FROM "+table+" FINAL").Scan(&raw); err != nil || raw != 0 {
			t.Fatalf("deleted %s rows=%d %v", table, raw, err)
		}
	}
	if err := store.connection.QueryRow(ctx, `SELECT count() FROM consent_ledger_v2 FINAL`).Scan(&raw); err != nil || raw != 7 {
		t.Fatalf("safety ledger lost: %d %v", raw, err)
	}
	// An old worker may set an already elapsed expiry; active deletion stays final.
	if err := store.connection.Exec(ctx, `INSERT INTO privacy_suppressions_v2 SELECT * REPLACE
		(now64(3) + INTERVAL 1 DAY AS updated_at, now64(3) - INTERVAL 1 DAY AS expires_at)
		FROM privacy_suppressions_v2 FINAL WHERE request_id = ?`, uuid.MustParse(deletion.RequestID)); err != nil {
		t.Fatal(err)
	}
	if err := store.connection.Exec(ctx, `ALTER TABLE privacy_suppressions_v2 MATERIALIZE TTL SETTINGS mutations_sync = 2`); err != nil {
		t.Fatal(err)
	}
	late, lateSHA := control(3, 6, "allowed")
	if _, err := store.CreateConsent(ctx, late, lateSHA); !errors.Is(err, v2.ErrSuppressed) {
		t.Fatalf("late grant bypassed deletion: %v", err)
	}
	if result, err := store.CreateConsent(ctx, grant2, grant2SHA); err != nil || !result.Replayed {
		t.Fatalf("post-delete receipt=%+v %v", result, err)
	}
	if _, err := store.InsertV2(ctx, second, secondSHA); !errors.Is(err, v2.ErrSuppressed) {
		t.Fatalf("post-delete replay=%v", err)
	}
}

func TestConsentInitialDeniedAndLegacyOptOutIntegration(t *testing.T) {
	store, ctx := retentionIntegrationStore(t)
	now := time.Now().UTC()
	subject := strings.Repeat("c", 64)
	spec, _ := product.Lookup(product.LinkaLooks)
	request := v2.ValidatedConsentRequest{ConsentRequest: v2.ConsentRequest{RequestID: uuid.NewString(), Scope: v2.ConsentScope{Product: product.LinkaLooks, SubjectKey: subject}, ConsentEpoch: 1, Version: 1, Preference: "denied", RecordedAt: "2026-11-08T12:00:00.000Z"}, ProductKey: spec.OpaqueKey}
	if _, err := store.CreateConsent(ctx, request, v2.BodySHA256([]byte(request.RequestID))); err != nil {
		t.Fatal(err)
	}
	body := integrationProductBatch(uuid.NewString(), uuid.NewString(), subject, now, "1")
	legacy, err := v2.ParseBatch(body, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertV2(ctx, legacy, v2.BodySHA256(body)); !errors.Is(err, v2.ErrSuppressed) {
		t.Fatalf("initial denied accepted legacy batch: %v", err)
	}
	request.RequestID = uuid.NewString()
	request.Version = 2
	request.ConsentEpoch = 2
	request.Preference = "allowed"
	if _, err := store.CreateConsent(ctx, request, v2.BodySHA256([]byte(request.RequestID))); err != nil {
		t.Fatal(err)
	}
	managedBody := []byte(strings.Replace(string(body), `"scope":{`, `"scope":{"consent_epoch":2,`, 1))
	managed, err := v2.ParseBatch(managedBody, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertV2(ctx, managed, v2.BodySHA256(managedBody)); err != nil {
		t.Fatal(err)
	}
	privacyBody := []byte(fmt.Sprintf(`{"schema_version":2,"request_id":%q,"scope":{"product":"linka-looks","subject_key":%q},"action":"opt_out","requested_at":%q}`, uuid.NewString(), subject, now.Format(time.RFC3339)))
	optOut, err := v2.ParsePrivacyRequest(privacyBody, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePrivacyRequest(ctx, optOut, v2.BodySHA256(privacyBody)); err != nil {
		t.Fatal(err)
	}
	request.RequestID = uuid.NewString()
	request.Version = 3
	request.ConsentEpoch = 3
	if _, err := store.CreateConsent(ctx, request, v2.BodySHA256([]byte(request.RequestID))); !errors.Is(err, v2.ErrSuppressed) {
		t.Fatalf("grant cleared legacy opt-out: %v", err)
	}
	if _, err := store.InsertV2(ctx, managed, v2.BodySHA256(managedBody)); !errors.Is(err, v2.ErrSuppressed) {
		t.Fatalf("epoch bypassed legacy opt-out: %v", err)
	}
}

func TestConsentAllSafeViewsNeverReopenOldEpochsIntegration(t *testing.T) {
	store, ctx := retentionIntegrationStore(t)
	subject := strings.Repeat("b", 64)
	spec, _ := product.Lookup(product.LinkaLooks)
	fixtures := []struct {
		table string
		views []string
	}{
		{"common_events_v2", []string{"datalens_common_v2", "datalens_common_v3"}},
		{"technical_events_v2", []string{"datalens_technical_v2", "datalens_technical_v3"}},
		{"plays_events_v2", []string{"datalens_plays_v2", "datalens_plays_v3", "datalens_game_sessions_v3"}},
		{"product_events_v2", []string{"datalens_product_v2"}},
		{"product_outcomes_v2", []string{"datalens_outcomes_v1", "datalens_outcomes_daily_v1"}},
	}
	for _, epoch := range []uint64{0, 1, 2} {
		for _, fixture := range fixtures {
			// Epoch zero omits the new column, exactly as an old writer does.
			columns, values := "", ""
			var args []any
			args = append(args, string(product.LinkaLooks), spec.OpaqueKey, subject, uuid.New(), uuid.New())
			if epoch > 0 {
				columns = ", consent_epoch"
				values = ", ?"
				args = append(args, epoch)
			}
			query := `INSERT INTO ` + fixture.table + ` (product,product_key,subject_key,record_id,app_session_id,kind,occurred_at,ingested_at` + columns + `) VALUES (?, ?, ?, ?, ?, 'session_started', now64(3), now64(3)` + values + `)`
			if err := store.connection.Exec(ctx, query, args...); err != nil {
				t.Fatalf("fixture %s: %v", fixture.table, err)
			}
		}
	}
	for _, step := range []struct {
		epoch, version uint64
		preference     string
		visible        uint64
	}{
		{0, 0, "", 1}, {1, 1, "allowed", 1}, {1, 2, "denied", 0}, {2, 3, "allowed", 1}, {2, 4, "denied", 0},
	} {
		if step.epoch > 0 {
			request := v2.ValidatedConsentRequest{ConsentRequest: v2.ConsentRequest{RequestID: uuid.NewString(), Scope: v2.ConsentScope{Product: product.LinkaLooks, SubjectKey: subject}, ConsentEpoch: step.epoch, Version: step.version, Preference: step.preference, RecordedAt: "2026-11-08T12:00:00.000Z"}, ProductKey: spec.OpaqueKey}
			if _, err := store.CreateConsent(ctx, request, v2.BodySHA256([]byte(request.RequestID))); err != nil {
				t.Fatal(err)
			}
		}
		for _, fixture := range fixtures {
			for _, view := range fixture.views {
				var count uint64
				if err := store.connection.QueryRow(ctx, "SELECT count() FROM "+view).Scan(&count); err != nil || count != step.visible {
					t.Fatalf("%s epoch=%d preference=%s count=%d want=%d err=%v", view, step.epoch, step.preference, count, step.visible, err)
				}
			}
		}
	}
}
