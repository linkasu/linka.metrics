package collector

import (
	"errors"
	"net/http"
	"strings"

	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
	"github.com/linkasu/linka.plays-metric/internal/httpx"
)

func (s *Server) consentsV2(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	// Never use authenticateProduct: its legacy fallback cannot authorize controls.
	header := request.Header.Get("Authorization")
	if s.identityTokens == nil || !strings.HasPrefix(header, "Bearer ") {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_consent_token")
		return
	}
	claims, err := s.identityTokens.Verify(request.Context(), strings.TrimPrefix(header, "Bearer "), "consent:write")
	if err != nil || claims.SubjectType != "service" || len(claims.Scopes) != 1 || claims.Scopes[0] != "consent:write" || claims.PersonKey != nil || claims.OrgKey != nil || claims.ConsentEpoch != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_consent_token")
		return
	}
	body, err := httpx.ReadBody(response, request, 16*1024)
	if err != nil {
		writeBodyError(response, err)
		return
	}
	control, err := v2.ParseConsentRequest(body)
	if err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_consent_request")
		return
	}
	if v2.ValidateIdempotencyKey(request.Header.Get("Idempotency-Key"), control.RequestID) != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	if control.Scope.Product != claims.Product || control.Scope.SubjectKey != claims.Subject {
		httpx.WriteError(response, http.StatusForbidden, "token_scope_mismatch")
		return
	}
	result, err := s.v2Writer.WriteConsent(request.Context(), control.RequestID, body)
	if err != nil {
		switch {
		case errors.Is(err, ErrWriterConflict):
			httpx.WriteError(response, http.StatusConflict, "consent_conflict")
		case errors.Is(err, ErrWriterSuppressed):
			httpx.WriteError(response, http.StatusForbidden, "telemetry_suppressed")
		default:
			s.logger.Error("writer rejected consent request", "error", err)
			httpx.WriteError(response, http.StatusBadGateway, "writer_unavailable")
		}
		return
	}
	httpx.WriteJSON(response, http.StatusOK, result)
}
