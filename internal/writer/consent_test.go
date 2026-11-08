package writer

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/auth"
	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
)

func TestWriterConsentRequiresBoundHMACAndMapsStoreResults(t *testing.T) {
	key := auth.ServiceKey{ID: "test", Secret: []byte(strings.Repeat("k", 32))}
	signer, _ := auth.NewServiceSigner(key, "collector")
	id := "10000000-0000-4000-8000-000000000001"
	body := []byte(fmt.Sprintf(`{"schema_version":2,"request_id":%q,"scope":{"product":"linka-pictures","subject_key":%q},"consent_epoch":1,"version":1,"preference":"allowed","recorded_at":"2026-11-08T12:00:00.000Z"}`, id, strings.Repeat("a", 64)))
	for _, tc := range []struct {
		name, path, headerID string
		signed, tamper       bool
		storeErr             error
		status, calls        int
	}{
		{"success", "/internal/v2/consents", id, true, false, nil, 200, 1},
		{"missing signature", "/internal/v2/consents", id, false, false, nil, 401, 0},
		{"path substitution", "/internal/v2/batches", id, true, false, nil, 401, 0},
		{"body substitution", "/internal/v2/consents", id, true, true, nil, 401, 0},
		{"id substitution", "/internal/v2/consents", "20000000-0000-4000-8000-000000000002", true, false, nil, 400, 0},
		{"idempotency conflict", "/internal/v2/consents", id, true, false, v2.ErrIdempotencyConflict, 409, 1},
		{"closed epoch", "/internal/v2/consents", id, true, false, v2.ErrConsentConflict, 409, 1},
		{"delete dominates", "/internal/v2/consents", id, true, false, v2.ErrSuppressed, 403, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verifier, _ := auth.NewServiceVerifier(key, nil, "collector", 5*time.Minute)
			store := &fakeStoreV2{err: tc.storeErr}
			wire := body
			if tc.tamper {
				wire = append(append([]byte(nil), body...), ' ')
			}
			request := httptest.NewRequest(http.MethodPost, "/internal/v2/consents", bytes.NewReader(wire))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", tc.headerID)
			if tc.signed {
				headers, _ := signer.Sign(http.MethodPost, tc.path, id, body)
				auth.ApplyServiceHeaders(request.Header, headers)
			}
			response := httptest.NewRecorder()
			NewServerWithV2(&fakeStore{}, store, key.Secret, verifier, writerTestLogger(), 5*time.Minute).ServeHTTP(response, request)
			if response.Code != tc.status || store.calls != tc.calls {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, store.calls, response.Body)
			}
		})
	}
}
