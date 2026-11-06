package analyticsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/auth"
)

const httpTestRequestID = "203e5283-5b07-45e0-99f1-424bbf66da83"

type fakeBackend struct {
	calls     []string
	query     Query
	requestID string
	runID     string
	report    Report
	run       SyncRun
	err       error
}

var _ Backend = (*fakeBackend)(nil)

func (b *fakeBackend) AnalyticsReport(_ context.Context, q Query) (Report, error) {
	b.calls = append(b.calls, "report")
	b.query = q
	return b.report, b.err
}

func (b *fakeBackend) AnalyticsEnqueue(_ context.Context, q Query, requestID string) (SyncRun, error) {
	b.calls = append(b.calls, "enqueue")
	b.query, b.requestID = q, requestID
	return b.run, b.err
}

func (b *fakeBackend) AnalyticsRun(_ context.Context, id string) (SyncRun, error) {
	b.calls = append(b.calls, "run")
	b.runID = id
	return b.run, b.err
}

func (b *fakeBackend) AnalyticsNext(context.Context) (SyncRun, error) {
	b.calls = append(b.calls, "next")
	return SyncRun{}, b.err
}

func (b *fakeBackend) AnalyticsUpdate(context.Context, SyncRun) error {
	b.calls = append(b.calls, "update")
	return b.err
}

func (b *fakeBackend) AnalyticsPublish(context.Context, SyncRun, Report, string, time.Time) error {
	b.calls = append(b.calls, "publish")
	return b.err
}

func (b *fakeBackend) AnalyticsPrivacy(context.Context) (string, time.Time, error) {
	b.calls = append(b.calls, "privacy")
	return "", time.Time{}, b.err
}

func newHTTPTestHandler(t *testing.T, backend Backend) (http.Handler, *auth.ServiceSigner) {
	t.Helper()
	key := auth.ServiceKey{ID: "test-crm", Secret: []byte("0123456789abcdef0123456789abcdef")}
	signer, err := auth.NewServiceSigner(key, "crm-analytics")
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewServiceVerifier(key, nil, "crm-analytics", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(backend, verifier, slog.New(slog.NewTextHandler(io.Discard, nil))), signer
}

func signedHTTPRequest(t *testing.T, signer *auth.ServiceSigner, path, requestID, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	headers, err := signer.Sign(r.Method, r.URL.EscapedPath(), requestID, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	auth.ApplyServiceHeaders(r.Header, headers)
	return r
}

func serveHTTPTest(t *testing.T, handler http.Handler, r *http.Request, status int) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != status {
		t.Errorf("status = %d, want %d; body = %s", w.Code, status, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	return w
}

func httpQueryBody(t *testing.T, query Query) string {
	t.Helper()
	body, err := json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func assertHTTPBackendCalls(t *testing.T, backend *fakeBackend, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(backend.calls, want) {
		t.Errorf("backend calls = %v, want %v", backend.calls, want)
	}
}

func TestHTTPQuerySignedReport(t *testing.T) {
	count := int64(7)
	report := Report{ContractVersion: Version, QueryHash: "synthetic-query-hash", Status: "ready", Results: []Result{{Product: "linka-type", Metric: "active_30d", Value: &count, Status: "ready"}}, Capabilities: []Capability{}, Insights: []Insight{}}
	backend := &fakeBackend{report: report}
	handler, signer := newHTTPTestHandler(t, backend)
	r := signedHTTPRequest(t, signer, "/internal/analytics/query", httpTestRequestID, httpQueryBody(t, validQuery()))
	w := serveHTTPTest(t, handler, r, http.StatusOK)
	var got Report
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, report) {
		t.Errorf("report = %+v, want %+v", got, report)
	}
	assertHTTPBackendCalls(t, backend, "report")
	if !reflect.DeepEqual(backend.query, validQuery()) {
		t.Errorf("backend query = %+v", backend.query)
	}
}

func TestHTTPRejectsInvalidAuthentication(t *testing.T) {
	for _, name := range []string{"missing headers", "bad signature", "wrong caller", "path mutation", "body mutation"} {
		t.Run(name, func(t *testing.T) {
			backend := &fakeBackend{}
			handler, signer := newHTTPTestHandler(t, backend)
			if name == "wrong caller" {
				var err error
				signer, err = auth.NewServiceSigner(auth.ServiceKey{ID: "test-crm", Secret: []byte("0123456789abcdef0123456789abcdef")}, "collector")
				if err != nil {
					t.Fatal(err)
				}
			}
			r := signedHTTPRequest(t, signer, "/internal/analytics/query", httpTestRequestID, httpQueryBody(t, validQuery()))
			switch name {
			case "missing headers":
				r.Header = http.Header{"Content-Type": {"application/json"}}
			case "bad signature":
				r.Header.Set(auth.ServiceSignatureHeader, "invalid")
			case "path mutation":
				r.URL.Path = "/internal/analytics/sync-requests"
			case "body mutation":
				r.Body = io.NopCloser(strings.NewReader(httpQueryBody(t, validQuery()) + " "))
			}
			serveHTTPTest(t, handler, r, http.StatusUnauthorized)
			assertHTTPBackendCalls(t, backend)
		})
	}
}

func TestHTTPRejectsInvalidRequests(t *testing.T) {
	valid := httpQueryBody(t, validQuery())
	for _, test := range []struct {
		name        string
		method      string
		contentType string
		body        string
		status      int
	}{
		{"GET", http.MethodGet, "application/json", valid, http.StatusMethodNotAllowed},
		{"content type", http.MethodPost, "text/plain", valid, http.StatusUnsupportedMediaType},
		{"missing content type", http.MethodPost, "", valid, http.StatusUnsupportedMediaType},
		{"oversized", http.MethodPost, "application/json", valid + strings.Repeat(" ", (16<<10)+1), http.StatusRequestEntityTooLarge},
		{"unknown field", http.MethodPost, "application/json", strings.TrimSuffix(valid, "}") + `,"unknown":true}`, http.StatusBadRequest},
		{"malformed", http.MethodPost, "application/json", `{"contract_version":`, http.StatusBadRequest},
		{"trailing JSON", http.MethodPost, "application/json", valid + `{}`, http.StatusBadRequest},
		{"invalid product", http.MethodPost, "application/json", strings.Replace(valid, "linka-type", "unknown-product", 1), http.StatusBadRequest},
		{"invalid date", http.MethodPost, "application/json", strings.Replace(valid, "2026-11-05", "2026-02-30", 1), http.StatusBadRequest},
		{"reversed dates", http.MethodPost, "application/json", strings.Replace(valid, "2026-10-01", "2026-11-06", 1), http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &fakeBackend{}
			handler, signer := newHTTPTestHandler(t, backend)
			r := signedHTTPRequest(t, signer, "/internal/analytics/query", httpTestRequestID, test.body)
			r.Method = test.method
			r.Header.Set("Content-Type", test.contentType)
			w := serveHTTPTest(t, handler, r, test.status)
			if test.status == http.StatusMethodNotAllowed && w.Header().Get("Allow") != http.MethodPost {
				t.Error("GET response must advertise Allow: POST")
			}
			assertHTTPBackendCalls(t, backend)
		})
	}
}

func TestHTTPQueryPendingDoesNotEnqueueOrInventCounts(t *testing.T) {
	for _, cause := range []error{ErrNotFound, ErrInvalidated} {
		t.Run(cause.Error(), func(t *testing.T) {
			count := int64(999)
			backend := &fakeBackend{err: fmt.Errorf("snapshot: %w", cause), report: Report{Status: "ready", Results: []Result{{Value: &count}}}}
			handler, signer := newHTTPTestHandler(t, backend)
			r := signedHTTPRequest(t, signer, "/internal/analytics/query", httpTestRequestID, httpQueryBody(t, validQuery()))
			w := serveHTTPTest(t, handler, r, http.StatusAccepted)
			var got Report
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			hash, err := validQuery().Hash()
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "pending" || got.ContractVersion != Version || got.QueryHash != hash || got.Results == nil || len(got.Results) != 0 {
				t.Errorf("pending report contains wrong identity/status or fabricated results: %+v", got)
			}
			assertHTTPBackendCalls(t, backend, "report")
		})
	}
}

func TestHTTPEnqueue(t *testing.T) {
	for _, test := range []struct {
		name      string
		requestID string
		err       error
		status    int
	}{
		{"accepted", httpTestRequestID, nil, http.StatusAccepted},
		{"conflict", httpTestRequestID, fmt.Errorf("enqueue: %w", ErrConflict), http.StatusConflict},
		{"invalid ID", "not-a-uuid", nil, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := SyncRun{ID: httpTestRequestID, State: "queued", Query: validQuery()}
			backend := &fakeBackend{run: run, err: test.err}
			handler, signer := newHTTPTestHandler(t, backend)
			r := signedHTTPRequest(t, signer, "/internal/analytics/sync-requests", test.requestID, httpQueryBody(t, validQuery()))
			w := serveHTTPTest(t, handler, r, test.status)
			if test.status == http.StatusBadRequest {
				assertHTTPBackendCalls(t, backend)
				return
			}
			assertHTTPBackendCalls(t, backend, "enqueue")
			if backend.requestID != test.requestID || !reflect.DeepEqual(backend.query, validQuery()) {
				t.Errorf("enqueue arguments = %q, %+v", backend.requestID, backend.query)
			}
			if test.status == http.StatusAccepted {
				var got SyncRun
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, run) {
					t.Errorf("run = %+v, want %+v", got, run)
				}
			}
		})
	}
}

func TestHTTPSyncRun(t *testing.T) {
	for _, test := range []struct {
		name string
		id   string
		body string
	}{
		{"valid", httpTestRequestID, `{}`},
		{"invalid ID", "not-a-uuid", `{}`},
		{"empty ID", "", `{}`},
		{"nonempty object", httpTestRequestID, `{"unknown":true}`},
		{"trailing JSON", httpTestRequestID, `{} {}`},
		{"malformed JSON", httpTestRequestID, `{`},
		{"null", httpTestRequestID, `null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := SyncRun{ID: httpTestRequestID, State: "running"}
			backend := &fakeBackend{run: run}
			handler, signer := newHTTPTestHandler(t, backend)
			r := signedHTTPRequest(t, signer, "/internal/analytics/sync-runs/"+test.id, httpTestRequestID, test.body)
			status := http.StatusBadRequest
			if test.name == "valid" {
				status = http.StatusOK
			}
			w := serveHTTPTest(t, handler, r, status)
			if status == http.StatusBadRequest {
				assertHTTPBackendCalls(t, backend)
				return
			}
			assertHTTPBackendCalls(t, backend, "run")
			if backend.runID != test.id {
				t.Errorf("run ID = %q, want %q", backend.runID, test.id)
			}
			var got SyncRun
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, run) {
				t.Errorf("run = %+v, want %+v", got, run)
			}
		})
	}
}

func TestHTTPBackendErrorsAreSanitized(t *testing.T) {
	for _, path := range []string{"/internal/analytics/query", "/internal/analytics/sync-requests", "/internal/analytics/sync-runs/" + httpTestRequestID} {
		t.Run(path, func(t *testing.T) {
			backend := &fakeBackend{err: errors.New("provider-secret=synthetic-secret email=synthetic@example.test")}
			handler, signer := newHTTPTestHandler(t, backend)
			body := httpQueryBody(t, validQuery())
			if strings.Contains(path, "/sync-runs/") {
				body = `{}`
			}
			r := signedHTTPRequest(t, signer, path, httpTestRequestID, body)
			w := serveHTTPTest(t, handler, r, http.StatusServiceUnavailable)
			for _, forbidden := range []string{"provider-secret", "synthetic-secret", "synthetic@example.test"} {
				if strings.Contains(w.Body.String(), forbidden) {
					t.Errorf("response leaked backend details: %s", w.Body.String())
				}
			}
			if !json.Valid(w.Body.Bytes()) || !strings.Contains(w.Body.String(), "analytics_unavailable") {
				t.Errorf("expected sanitized JSON error, got %s", w.Body.String())
			}
		})
	}
}
