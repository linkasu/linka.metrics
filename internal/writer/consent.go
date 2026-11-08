package writer

import (
	"errors"
	"net/http"

	"github.com/linkasu/linka.plays-metric/internal/auth"
	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
	"github.com/linkasu/linka.plays-metric/internal/httpx"
)

func (s *Server) consentsV2(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	body, err := httpx.ReadBody(response, request, 16*1024)
	if err != nil {
		writeV2BodyError(response, err)
		return
	}
	headers := auth.ServiceHeadersFromRequest(request)
	if err := s.serviceVerifier.Verify(request.Method, request.URL.EscapedPath(), body, headers); err != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_service_signature")
		return
	}
	control, err := v2.ParseConsentRequest(body)
	if err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_consent_request")
		return
	}
	key := request.Header.Get("Idempotency-Key")
	if v2.ValidateIdempotencyKey(key, control.RequestID) != nil || headers.RequestID != key {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	result, err := s.storeV2.CreateConsent(request.Context(), control, v2.BodySHA256(body))
	if err != nil {
		switch {
		case errors.Is(err, v2.ErrIdempotencyConflict), errors.Is(err, v2.ErrConsentConflict):
			httpx.WriteError(response, http.StatusConflict, "consent_conflict")
		case errors.Is(err, v2.ErrSuppressed):
			httpx.WriteError(response, http.StatusForbidden, "telemetry_suppressed")
		default:
			// Driver errors can include bound opaque identifiers; log no payload.
			s.logger.Error("persist consent request failed")
			httpx.WriteError(response, http.StatusServiceUnavailable, "store_unavailable")
		}
		return
	}
	httpx.WriteJSON(response, http.StatusOK, result)
}
