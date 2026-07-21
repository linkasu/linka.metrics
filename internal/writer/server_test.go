package writer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/auth"
	"github.com/linkasu/linka.plays-metric/internal/contract/fundraising"
	v1 "github.com/linkasu/linka.plays-metric/internal/contract/v1"
	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
)

type fakeStore struct {
	inserted int
	err      error
}

type fakeStoreV2 struct {
	result v2.IngestResult
	err    error
	calls  int
}

type fakeFundraisingStore struct {
	result fundraising.IngestResult
	err    error
	calls  int
}

func (s *fakeFundraisingStore) InsertFundraising(context.Context, fundraising.ValidatedBatch, string) (fundraising.IngestResult, error) {
	s.calls++
	return s.result, s.err
}

func (s *fakeStoreV2) InsertV2(context.Context, v2.ValidatedBatch, string) (v2.IngestResult, error) {
	s.calls++
	return s.result, s.err
}

func (s *fakeStoreV2) CreatePrivacyRequest(context.Context, v2.ValidatedPrivacyRequest, string) (v2.PrivacyResult, error) {
	s.calls++
	return v2.PrivacyResult{Status: "pending"}, s.err
}

func (s *fakeStoreV2) CreateLegacyPrivacyRequest(context.Context, v1.ValidatedPrivacyRequest, string) (v2.PrivacyResult, error) {
	s.calls++
	return v2.PrivacyResult{Status: "pending"}, s.err
}

func (s *fakeStore) Ping(context.Context) error {
	return s.err
}

func (s *fakeStore) Insert(_ context.Context, batch v1.ValidatedBatch) error {
	s.inserted += len(batch.Events) + len(batch.SessionSummaries)
	return s.err
}

func TestWriterVerifiesAndInsertsBatch(t *testing.T) {
	store := &fakeStore{}
	secret := []byte(strings.Repeat("w", 32))
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	body := writerValidBatch()
	timestamp, bodySHA, signature := auth.SignWriterRequest(secret, body, now)
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/events", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.WriterTimestampHeader, timestamp)
	request.Header.Set(auth.WriterBodySHAHeader, bodySHA)
	request.Header.Set(auth.WriterSignatureHeader, signature)
	response := httptest.NewRecorder()
	server := &Server{store: store, secret: secret, logger: writerTestLogger(), maxSkew: 5 * time.Minute, now: func() time.Time { return now }}

	server.events(response, request)

	if response.Code != http.StatusAccepted || store.inserted != 1 {
		t.Fatalf("status = %d, inserted = %d, body = %s", response.Code, store.inserted, response.Body.String())
	}
}

func TestWriterRejectsInvalidSignatureWithoutInsert(t *testing.T) {
	store := &fakeStore{}
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/events", bytes.NewReader(writerValidBatch()))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler := NewServer(store, []byte(strings.Repeat("w", 32)), writerTestLogger(), 5*time.Minute)

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || store.inserted != 0 {
		t.Fatalf("status = %d, inserted = %d", response.Code, store.inserted)
	}
}

func TestWriterV2VerifiesBoundRequestAndReturnsReplay(t *testing.T) {
	storeV2 := &fakeStoreV2{result: v2.IngestResult{Count: 1, Replayed: true}}
	key := auth.ServiceKey{ID: "current", Secret: []byte(strings.Repeat("k", 32))}
	signer, _ := auth.NewServiceSigner(key, "collector")
	verifier, _ := auth.NewServiceVerifier(key, nil, "collector", 5*time.Minute)
	body := writerV2Batch(time.Now().UTC())
	requestID := "10000000-0000-4000-8000-000000000001"
	headers, err := signer.Sign(http.MethodPost, "/internal/v2/batches", requestID, body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/v2/batches", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", requestID)
	auth.ApplyServiceHeaders(request.Header, headers)
	response := httptest.NewRecorder()

	NewServerWithV2(&fakeStore{}, storeV2, []byte(strings.Repeat("w", 32)), verifier, writerTestLogger(), 5*time.Minute).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || storeV2.calls != 1 || !strings.Contains(response.Body.String(), `"replayed":true`) {
		t.Fatalf("status = %d, calls = %d, body = %s", response.Code, storeV2.calls, response.Body.String())
	}
}

func TestWriterV2ReturnsConflictForChangedBodyHash(t *testing.T) {
	storeV2 := &fakeStoreV2{err: v2.ErrIdempotencyConflict}
	key := auth.ServiceKey{ID: "current", Secret: []byte(strings.Repeat("k", 32))}
	signer, _ := auth.NewServiceSigner(key, "collector")
	verifier, _ := auth.NewServiceVerifier(key, nil, "collector", 5*time.Minute)
	body := writerV2Batch(time.Now().UTC())
	requestID := "10000000-0000-4000-8000-000000000001"
	headers, _ := signer.Sign(http.MethodPost, "/internal/v2/batches", requestID, body)
	request := httptest.NewRequest(http.MethodPost, "/internal/v2/batches", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", requestID)
	auth.ApplyServiceHeaders(request.Header, headers)
	response := httptest.NewRecorder()

	NewServerWithV2(&fakeStore{}, storeV2, []byte(strings.Repeat("w", 32)), verifier, writerTestLogger(), 5*time.Minute).ServeHTTP(response, request)

	if response.Code != http.StatusConflict || storeV2.calls != 1 {
		t.Fatalf("status = %d, calls = %d", response.Code, storeV2.calls)
	}
}

func TestWriterFundraisingRequiresCollectorSignatureAndPersists(t *testing.T) {
	store := &fakeFundraisingStore{result: fundraising.IngestResult{Count: 1}}
	key := auth.ServiceKey{ID: "current", Secret: []byte(strings.Repeat("k", 32))}
	signer, _ := auth.NewServiceSigner(key, "collector")
	verifier, _ := auth.NewServiceVerifier(key, nil, "collector", 5*time.Minute)
	body := writerFundraisingBatch(time.Now().UTC())
	batchID := "10000000-0000-4000-8000-000000000001"
	headers, err := signer.Sign(http.MethodPost, "/internal/fundraising/batches", batchID, body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/fundraising/batches", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", batchID)
	auth.ApplyServiceHeaders(request.Header, headers)
	response := httptest.NewRecorder()

	NewServerWithV2AndFundraising(&fakeStore{}, &fakeStoreV2{}, store, []byte(strings.Repeat("w", 32)), verifier, writerTestLogger(), 5*time.Minute).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || store.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, store.calls, response.Body.String())
	}
}

func TestWriterFundraisingRejectsNonCollectorSignature(t *testing.T) {
	store := &fakeFundraisingStore{}
	key := auth.ServiceKey{ID: "current", Secret: []byte(strings.Repeat("k", 32))}
	signer, _ := auth.NewServiceSigner(key, "nko-donations")
	verifier, _ := auth.NewServiceVerifier(key, nil, "collector", 5*time.Minute)
	body := writerFundraisingBatch(time.Now().UTC())
	headers, _ := signer.Sign(http.MethodPost, "/internal/fundraising/batches", "10000000-0000-4000-8000-000000000001", body)
	request := httptest.NewRequest(http.MethodPost, "/internal/fundraising/batches", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "10000000-0000-4000-8000-000000000001")
	auth.ApplyServiceHeaders(request.Header, headers)
	response := httptest.NewRecorder()

	NewServerWithV2AndFundraising(&fakeStore{}, &fakeStoreV2{}, store, []byte(strings.Repeat("w", 32)), verifier, writerTestLogger(), 5*time.Minute).ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || store.calls != 0 {
		t.Fatalf("status=%d calls=%d", response.Code, store.calls)
	}
}

func writerValidBatch() []byte {
	return []byte(fmt.Sprintf(`{
  "schema_version":1,
  "events":[{
    "event_id":"10000000-0000-4000-8000-000000000001",
    "event_name":"app_started",
    "occurred_at":"2026-07-18T12:00:00.123Z",
    "installation_id":"20000000-0000-4000-8000-000000000002",
    "app_session_id":"30000000-0000-4000-8000-000000000003",
    "app":{"version":"1","build":"1","platform":"linux","os_version":"1","locale":"ru"},
    "properties":{}
  }]
}`))
}

func writerV2Batch(now time.Time) []byte {
	return []byte(fmt.Sprintf(`{
  "schema_version":2,
  "batch_id":"10000000-0000-4000-8000-000000000001",
  "scope":{"product":"linka-plays","subject_key":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
  "stream":"common",
  "sent_at":%q,
  "records":[{
    "record_id":"20000000-0000-4000-8000-000000000002",
    "occurred_at":%q,
    "kind":"app_started",
    "app_session_id":"30000000-0000-4000-8000-000000000003",
    "app":{"version":"1","build":"1","platform":"linux","os_version":"1","locale":"ru"}
  }]
}`, now.UTC().Truncate(time.Second).Format(time.RFC3339), now.UTC().Add(-time.Minute).Truncate(time.Second).Format(time.RFC3339)))
}

func writerFundraisingBatch(now time.Time) []byte {
	return []byte(fmt.Sprintf(`{
  "schema_version":1,
  "batch_id":"10000000-0000-4000-8000-000000000001",
  "sent_at":%q,
  "records":[{
    "event_id":"20000000-0000-4000-8000-000000000002",
    "occurred_at":%q,
    "kind":"recurring_charge_failed",
    "amount":"500.00",
    "currency":"RUB",
    "frequency":"monthly",
    "attribution_source":"unknown",
    "attribution_campaign":null,
    "failure_code":"declined"
  }]
}`, now.UTC().Truncate(time.Second).Format(time.RFC3339), now.UTC().Add(-time.Minute).Truncate(time.Second).Format(time.RFC3339)))
}

func writerTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
