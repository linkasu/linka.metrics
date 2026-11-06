package analyticsapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/linkasu/linka.plays-metric/internal/auth"
	"github.com/linkasu/linka.plays-metric/internal/httpx"
)

const maxRequestBytes = 16 << 10

// NewHandler exposes aggregate snapshots, not ClickHouse SQL or source events.
// Its verifier must have a dedicated CRM-only keyring and caller; collector or
// telemetry JWT credentials are never accepted by this surface.
func NewHandler(backend Backend, verifier *auth.ServiceVerifier, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return httpx.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			httpx.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !httpx.IsJSON(r) {
			httpx.WriteError(w, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
			return
		}
		body, err := httpx.ReadBody(w, r, maxRequestBytes)
		if err != nil {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "invalid_or_oversized_body")
			return
		}
		headers := auth.ServiceHeadersFromRequest(r)
		if verifier == nil || verifier.Verify(r.Method, r.URL.EscapedPath(), body, headers) != nil {
			httpx.WriteError(w, http.StatusUnauthorized, "invalid_service_signature")
			return
		}
		if backend == nil {
			httpx.WriteError(w, http.StatusServiceUnavailable, "analytics_not_configured")
			return
		}
		writeFailure := func(err error) {
			switch {
			case errors.Is(err, ErrConflict):
				httpx.WriteError(w, http.StatusConflict, "idempotency_conflict")
			case errors.Is(err, ErrNotFound):
				httpx.WriteError(w, http.StatusNotFound, "run_not_found")
			default:
				// Driver/provider errors can contain query parameters or credentials.
				logger.Warn("analytics request failed", "operation", "snapshot")
				httpx.WriteError(w, http.StatusServiceUnavailable, "analytics_unavailable")
			}
		}
		if strings.HasPrefix(r.URL.Path, "/internal/analytics/sync-runs/") {
			id := strings.TrimPrefix(r.URL.Path, "/internal/analytics/sync-runs/")
			if _, err := uuid.Parse(id); err != nil || string(bytes.TrimSpace(body)) != "{}" {
				httpx.WriteError(w, http.StatusBadRequest, "invalid_run_request")
				return
			}
			run, err := backend.AnalyticsRun(r.Context(), id)
			if err != nil {
				writeFailure(err)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, run)
			return
		}
		if r.URL.Path != "/internal/analytics/query" && r.URL.Path != "/internal/analytics/sync-requests" && r.URL.Path != "/internal/analytics/capabilities" {
			httpx.WriteError(w, http.StatusNotFound, "not_found")
			return
		}
		var query Query
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&query) != nil || decoder.Decode(new(any)) != io.EOF || query.Validate() != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_analytics_query")
			return
		}
		if r.URL.Path == "/internal/analytics/capabilities" {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"contract_version": Version, "products": Products()})
			return
		}
		if r.URL.Path == "/internal/analytics/sync-requests" {
			if _, err := uuid.Parse(headers.RequestID); err != nil {
				httpx.WriteError(w, http.StatusBadRequest, "invalid_request_id")
				return
			}
			run, err := backend.AnalyticsEnqueue(r.Context(), query, headers.RequestID)
			if err != nil {
				writeFailure(err)
				return
			}
			httpx.WriteJSON(w, http.StatusAccepted, run)
			return
		}
		report, err := backend.AnalyticsReport(r.Context(), query)
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidated) {
			hash, _ := query.Hash()
			httpx.WriteJSON(w, http.StatusAccepted, Report{ContractVersion: Version, QueryHash: hash, Status: "pending", GeneratedAt: time.Now().UTC(), Results: []Result{}, Capabilities: []Capability{}, Insights: []Insight{{Kind: "data_quality", Message: "Срез ещё не подготовлен или требует обновления после изменения privacy-состояния."}}})
			return
		}
		if err != nil {
			writeFailure(err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, report)
	}))
}
