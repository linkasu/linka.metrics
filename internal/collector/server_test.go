package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/auth"
	v1 "github.com/linkasu/linka.plays-metric/internal/contract/v1"
	"github.com/linkasu/linka.plays-metric/internal/product"
)

type fakeEventWriter struct {
	body []byte
	err  error
}

type fakeV2Writer struct {
	calls      int
	legacyBody []byte
}

type fakeFundraisingWriter struct {
	calls int
	body  []byte
	err   error
}

func (w *fakeFundraisingWriter) WriteFundraising(_ context.Context, _ string, body []byte) (fundraisingWriteResult, error) {
	w.calls++
	w.body = append([]byte(nil), body...)
	return fundraisingWriteResult{Count: 1}, w.err
}

func (w *fakeV2Writer) WriteV2(context.Context, string, []byte) (v2WriteResult, error) {
	w.calls++
	return v2WriteResult{Count: 1}, nil
}

func (w *fakeV2Writer) WritePrivacy(context.Context, string, []byte) (privacyWriteResult, error) {
	w.calls++
	return privacyWriteResult{Status: "pending"}, nil
}

func (w *fakeV2Writer) WriteLegacyPrivacy(_ context.Context, _ string, body []byte) (privacyWriteResult, error) {
	w.calls++
	w.legacyBody = append([]byte(nil), body...)
	return privacyWriteResult{Status: "pending"}, nil
}

func (w *fakeEventWriter) Write(_ context.Context, body []byte) error {
	w.body = append([]byte(nil), body...)
	return w.err
}

func TestEventsForwardsExactBodyAfterWriterSuccess(t *testing.T) {
	writer := &fakeEventWriter{}
	tokens, err := auth.NewInstallationTokens([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	claims, token, err := tokens.Issue()
	if err != nil {
		t.Fatal(err)
	}
	body := validBatch(claims.InstallationID)
	request := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()

	NewServer(writer, tokens, testLogger()).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !bytes.Equal(writer.body, body) {
		t.Fatal("collector changed the forwarded body")
	}
}

func TestEventsDoesNotSucceedWhenWriterFails(t *testing.T) {
	writer := &fakeEventWriter{err: errors.New("unavailable")}
	tokens, err := auth.NewInstallationTokens([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	claims, token, err := tokens.Issue()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(validBatch(claims.InstallationID)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()

	NewServer(writer, tokens, testLogger()).ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestInstallationEndpointRejectsUnknownFields(t *testing.T) {
	tokens, err := auth.NewInstallationTokens([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/installations", strings.NewReader(`{"device_id":"forbidden"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	NewServer(&fakeEventWriter{}, tokens, testLogger()).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestV1PrivacyRequestBindsInstallationFromToken(t *testing.T) {
	tokens, err := auth.NewInstallationTokens([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	claims, token, err := tokens.Issue()
	if err != nil {
		t.Fatal(err)
	}
	requestID := "10000000-0000-4000-8000-000000000001"
	body := []byte(fmt.Sprintf(`{"schema_version":1,"request_id":%q,"action":"delete","requested_at":%q}`,
		requestID, time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)))
	request := httptest.NewRequest(http.MethodPost, "/v1/privacy/requests", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", requestID)
	response := httptest.NewRecorder()
	writer := &fakeV2Writer{}

	NewServerWithV2(&fakeEventWriter{}, writer, tokens, nil, testLogger()).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || writer.calls != 1 {
		t.Fatalf("status = %d, calls = %d, body = %s", response.Code, writer.calls, response.Body.String())
	}
	var forwarded v1.InternalPrivacyRequest
	if err := json.Unmarshal(writer.legacyBody, &forwarded); err != nil {
		t.Fatal(err)
	}
	if forwarded.InstallationID != claims.InstallationID {
		t.Fatalf("forwarded installation = %s, want %s", forwarded.InstallationID, claims.InstallationID)
	}
}

func TestV2BatchRequiresExactProductTokenScope(t *testing.T) {
	installationTokens, err := auth.NewInstallationTokens([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	installationClaims, _, err := installationTokens.Issue()
	if err != nil {
		t.Fatal(err)
	}
	productTokens, err := auth.NewProductTokens(auth.ServiceKey{ID: "current", Secret: []byte(strings.Repeat("p", 32))}, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	productClaims, token, err := productTokens.IssueAnonymous(product.LinkaPlays, installationClaims.InstallationID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	body := collectorV2Batch(now, productClaims.SubjectKey)
	request := httptest.NewRequest(http.MethodPost, "/v2/batches", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", "10000000-0000-4000-8000-000000000001")
	response := httptest.NewRecorder()
	v2Writer := &fakeV2Writer{}

	NewServerWithV2(&fakeEventWriter{}, v2Writer, installationTokens, productTokens, testLogger()).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || v2Writer.calls != 1 {
		t.Fatalf("status = %d, calls = %d, body = %s", response.Code, v2Writer.calls, response.Body.String())
	}
}

func TestV2BatchRejectsScopeMismatchBeforeWriter(t *testing.T) {
	installationTokens, _ := auth.NewInstallationTokens([]byte(strings.Repeat("s", 32)))
	installationClaims, _, _ := installationTokens.Issue()
	productTokens, _ := auth.NewProductTokens(auth.ServiceKey{ID: "current", Secret: []byte(strings.Repeat("p", 32))}, nil, time.Hour)
	_, token, _ := productTokens.IssueAnonymous(product.LinkaPlays, installationClaims.InstallationID)
	body := collectorV2Batch(time.Now().UTC(), strings.Repeat("0", 64))
	request := httptest.NewRequest(http.MethodPost, "/v2/batches", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", "10000000-0000-4000-8000-000000000001")
	response := httptest.NewRecorder()
	v2Writer := &fakeV2Writer{}

	NewServerWithV2(&fakeEventWriter{}, v2Writer, installationTokens, productTokens, testLogger()).ServeHTTP(response, request)

	if response.Code != http.StatusForbidden || v2Writer.calls != 0 {
		t.Fatalf("status = %d, calls = %d", response.Code, v2Writer.calls)
	}
}

func TestFundraisingBatchRequiresDonationHMACAndForwardsExactBody(t *testing.T) {
	key := auth.ServiceKey{ID: "donations", Secret: []byte(strings.Repeat("d", 32))}
	signer, err := auth.NewServiceSigner(key, "nko-donations")
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewServiceVerifier(key, nil, "nko-donations", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	body := fundraisingBatch(time.Now().UTC())
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
	writer := &fakeFundraisingWriter{}

	NewServerWithIdentityV2AndFundraising(&fakeEventWriter{}, nil, writer, nil, nil, nil, false, verifier, testLogger()).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || writer.calls != 1 || !bytes.Equal(writer.body, body) {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, writer.calls, response.Body.String())
	}
}

func TestFundraisingBatchRejectsIdentityBearerWithoutDonationHMAC(t *testing.T) {
	key := auth.ServiceKey{ID: "donations", Secret: []byte(strings.Repeat("d", 32))}
	verifier, _ := auth.NewServiceVerifier(key, nil, "nko-donations", 5*time.Minute)
	request := httptest.NewRequest(http.MethodPost, "/internal/fundraising/batches", bytes.NewReader(fundraisingBatch(time.Now().UTC())))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer not-used-here")
	response := httptest.NewRecorder()
	writer := &fakeFundraisingWriter{}

	NewServerWithIdentityV2AndFundraising(&fakeEventWriter{}, nil, writer, nil, nil, nil, false, verifier, testLogger()).ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || writer.calls != 0 {
		t.Fatalf("status=%d calls=%d", response.Code, writer.calls)
	}
}

func validBatch(installationID string) []byte {
	return []byte(fmt.Sprintf(`{
  "schema_version":1,
  "events":[{
    "event_id":"10000000-0000-4000-8000-000000000001",
    "event_name":"app_started",
    "occurred_at":"2026-07-18T12:00:00.123Z",
    "installation_id":%q,
    "app_session_id":"30000000-0000-4000-8000-000000000003",
    "app":{"version":"1.2.3","build":"42","platform":"linux","os_version":"6.8","locale":"ru-RU"},
    "properties":{}
  }]
}`, installationID))
}

func collectorV2Batch(now time.Time, subjectKey string) []byte {
	return []byte(fmt.Sprintf(`{
  "schema_version":2,
  "batch_id":"10000000-0000-4000-8000-000000000001",
  "scope":{"product":"linka-plays","subject_key":%q},
  "stream":"common",
  "sent_at":%q,
  "records":[{
    "record_id":"20000000-0000-4000-8000-000000000002",
    "occurred_at":%q,
    "kind":"app_started",
    "app_session_id":"30000000-0000-4000-8000-000000000003",
    "app":{"version":"1","build":"1","platform":"linux","os_version":"1","locale":"ru"}
  }]
}`, subjectKey, now.UTC().Truncate(time.Second).Format(time.RFC3339), now.UTC().Add(-time.Minute).Truncate(time.Second).Format(time.RFC3339)))
}

func fundraisingBatch(now time.Time) []byte {
	return []byte(fmt.Sprintf(`{
  "schema_version":1,
  "batch_id":"10000000-0000-4000-8000-000000000001",
  "sent_at":%q,
  "records":[{
    "event_id":"20000000-0000-4000-8000-000000000002",
    "occurred_at":%q,
    "kind":"payment_succeeded",
    "amount":"500.00",
    "currency":"RUB",
    "frequency":"one_time",
    "attribution_source":"direct",
    "attribution_campaign":null
  }]
}`, now.UTC().Truncate(time.Second).Format(time.RFC3339), now.UTC().Add(-time.Minute).Truncate(time.Second).Format(time.RFC3339)))
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
