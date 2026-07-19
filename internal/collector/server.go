package collector

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/linkasu/linka.plays-metric/internal/auth"
	v1 "github.com/linkasu/linka.plays-metric/internal/contract/v1"
	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
	"github.com/linkasu/linka.plays-metric/internal/httpx"
	"github.com/linkasu/linka.plays-metric/internal/jsonstrict"
	"github.com/linkasu/linka.plays-metric/internal/product"
)

const privacyText = `Политика LINKa Metrics, версия 2026-07-19-v3, действует с 19 июля 2026 года.

LINKa Metrics принимает только предусмотренную закрытыми контрактами v1 и v2 обезличенную техническую, продуктовую и игровую телеметрию зарегистрированных продуктов LINKa.

Сервис не принимает имя, контакты, текст или произнесённые фразы, координаты взгляда и указателя, ожидаемые и фактические ответы, идентификаторы целей, пути файлов, сообщения и стеки ошибок. Ошибки передаются только как заранее вычисленный стабильный fingerprint и безопасное имя компонента.

Идентификатор установки создаётся случайно и не связан с учётной записью, устройством или IP-адресом. HMAC-токен имеет ограниченный срок, может быть обновлён без смены идентификатора и не подтверждает подлинность приложения или пользователя. V2 использует короткоживущие pairwise JWT от LINKa Identity. IP-адрес, заголовки авторизации и тела запросов не записываются в журналы приложения.

В v1 поддерживается удаление по installation token, в v2 — opt-out и удаление по непрозрачным ключам. Завершение фиксируется только после подтверждённых mutations. До юридического утверждения сроков автоматический TTL не активирован; expires_at остаётся пустым. Технический контакт: ivan@aacidov.ru.
`

type EventWriter interface {
	Write(context.Context, []byte) error
}

type v2WriteResult struct {
	Count    int
	Replayed bool
}

type privacyWriteResult struct {
	Status   string
	Replayed bool
}

type V2Writer interface {
	WriteV2(context.Context, string, []byte) (v2WriteResult, error)
	WritePrivacy(context.Context, string, []byte) (privacyWriteResult, error)
	WriteLegacyPrivacy(context.Context, string, []byte) (privacyWriteResult, error)
}

type Server struct {
	writer              EventWriter
	tokens              *auth.InstallationTokens
	v2Writer            V2Writer
	productTokens       *auth.ProductTokens
	identityTokens      *auth.IdentityJWTVerifier
	legacyProductTokens bool
	logger              *slog.Logger
	now                 func() time.Time
}

func NewServer(writer EventWriter, tokens *auth.InstallationTokens, logger *slog.Logger) http.Handler {
	return newServer(writer, tokens, nil, nil, nil, false, logger)
}

func NewServerWithV2(writer EventWriter, v2Writer V2Writer, tokens *auth.InstallationTokens, productTokens *auth.ProductTokens, logger *slog.Logger) http.Handler {
	return newServer(writer, tokens, v2Writer, productTokens, nil, true, logger)
}

func NewServerWithIdentityV2(writer EventWriter, v2Writer V2Writer, tokens *auth.InstallationTokens, identityTokens *auth.IdentityJWTVerifier,
	legacyTokens *auth.ProductTokens, legacyEnabled bool, logger *slog.Logger) http.Handler {
	return newServer(writer, tokens, v2Writer, legacyTokens, identityTokens, legacyEnabled, logger)
}

func newServer(writer EventWriter, tokens *auth.InstallationTokens, v2Writer V2Writer, productTokens *auth.ProductTokens,
	identityTokens *auth.IdentityJWTVerifier, legacyEnabled bool, logger *slog.Logger) http.Handler {
	server := &Server{
		writer: writer, tokens: tokens, v2Writer: v2Writer, productTokens: productTokens, identityTokens: identityTokens,
		legacyProductTokens: legacyEnabled, logger: logger, now: time.Now,
	}
	router := chi.NewRouter()
	router.Use(httpx.SecurityHeaders)
	router.Use(httpx.RequestLog(logger))
	router.Get("/healthz", server.health)
	router.Get("/privacy", server.privacy)
	router.Post("/v1/installations", server.installation)
	router.Post("/v1/installations/renew", server.renewInstallation)
	router.Post("/v1/events", server.events)
	if v2Writer != nil {
		router.Post("/v1/privacy/requests", server.privacyRequestV1)
	}
	if v2Writer != nil && (identityTokens != nil || (legacyEnabled && productTokens != nil)) {
		if legacyEnabled && productTokens != nil {
			router.Post("/v2/tokens", server.productToken)
		}
		router.Post("/v2/batches", server.batchesV2)
		router.Post("/v2/privacy/requests", server.privacyRequestV2)
	}
	return router
}

func (s *Server) health(response http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) privacy(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte(privacyText))
}

func (s *Server) installation(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	body, err := httpx.ReadBody(response, request, 4096)
	if err != nil {
		writeBodyError(response, err)
		return
	}
	var input struct{}
	if err := httpx.DecodeStrict(body, &input); err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_json")
		return
	}
	claims, token, err := s.tokens.Issue()
	if err != nil {
		s.logger.Error("issue installation token", "error", err)
		httpx.WriteError(response, http.StatusInternalServerError, "internal_error")
		return
	}
	httpx.WriteJSON(response, http.StatusCreated, map[string]any{
		"installation_id": claims.InstallationID,
		"token":           token,
		"token_version":   "v1",
		"issued_at":       claims.IssuedAt,
	})
}

func (s *Server) renewInstallation(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	body, err := httpx.ReadBody(response, request, 4096)
	if err != nil {
		writeBodyError(response, err)
		return
	}
	var input struct{}
	if err := httpx.DecodeStrict(body, &input); err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_json")
		return
	}
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_installation_token")
		return
	}
	claims, renewed, err := s.tokens.Renew(strings.TrimPrefix(header, "Bearer "))
	if err != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_installation_token")
		return
	}
	httpx.WriteJSON(response, http.StatusCreated, map[string]any{
		"installation_id": claims.InstallationID, "token": renewed, "token_version": "v1", "issued_at": claims.IssuedAt,
	})
}

func (s *Server) events(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	claims, err := s.authenticate(request.Header.Get("Authorization"))
	if err != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_installation_token")
		return
	}
	body, err := httpx.ReadBody(response, request, v1.MaxBatchBytes)
	if err != nil {
		writeBodyError(response, err)
		return
	}
	batch, err := v1.ParseBatch(body)
	if err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_event_batch")
		return
	}
	for _, event := range batch.Events {
		if event.InstallationID != claims.InstallationID {
			httpx.WriteError(response, http.StatusForbidden, "installation_id_mismatch")
			return
		}
	}
	for _, summary := range batch.SessionSummaries {
		if summary.InstallationID != claims.InstallationID {
			httpx.WriteError(response, http.StatusForbidden, "installation_id_mismatch")
			return
		}
	}
	if err := s.writer.Write(request.Context(), body); err != nil {
		if errors.Is(err, ErrWriterSuppressed) {
			httpx.WriteError(response, http.StatusForbidden, "telemetry_suppressed")
			return
		}
		s.logger.Error("writer rejected event batch", "error", err)
		httpx.WriteError(response, http.StatusBadGateway, "writer_unavailable")
		return
	}
	httpx.WriteJSON(response, http.StatusAccepted, map[string]int{
		"accepted_events":            len(batch.Events),
		"accepted_session_summaries": len(batch.SessionSummaries),
	})
}

func (s *Server) privacyRequestV1(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	claims, err := s.authenticate(request.Header.Get("Authorization"))
	if err != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_installation_token")
		return
	}
	body, err := httpx.ReadBody(response, request, 16*1024)
	if err != nil {
		writeBodyError(response, err)
		return
	}
	privacyRequest, err := v1.ParsePublicPrivacyRequest(body, claims.InstallationID, s.now())
	if err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_privacy_request")
		return
	}
	if err := v2.ValidateIdempotencyKey(request.Header.Get("Idempotency-Key"), privacyRequest.RequestID); err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	internalBody, err := json.Marshal(privacyRequest.InternalPrivacyRequest)
	if err != nil {
		httpx.WriteError(response, http.StatusInternalServerError, "internal_error")
		return
	}
	result, err := s.v2Writer.WriteLegacyPrivacy(request.Context(), privacyRequest.RequestID, internalBody)
	if err != nil {
		if errors.Is(err, ErrWriterConflict) {
			httpx.WriteError(response, http.StatusConflict, "idempotency_conflict")
			return
		}
		s.logger.Error("writer rejected V1 privacy request", "error", err)
		httpx.WriteError(response, http.StatusBadGateway, "writer_unavailable")
		return
	}
	httpx.WriteJSON(response, http.StatusAccepted, map[string]any{
		"request_id": privacyRequest.RequestID, "status": result.Status, "replayed": result.Replayed,
	})
}

func (s *Server) authenticate(header string) (auth.InstallationClaims, error) {
	prefix := "Bearer "
	if !strings.HasPrefix(header, prefix) || strings.Contains(strings.TrimPrefix(header, prefix), " ") {
		return auth.InstallationClaims{}, errors.New("missing bearer token")
	}
	return s.tokens.Verify(strings.TrimPrefix(header, prefix))
}

func (s *Server) productToken(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	installationClaims, err := s.authenticate(request.Header.Get("Authorization"))
	if err != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_installation_token")
		return
	}
	body, err := httpx.ReadBody(response, request, 4096)
	if err != nil {
		writeBodyError(response, err)
		return
	}
	var input struct {
		Product product.ID `json:"product"`
	}
	if err := jsonstrict.DecodeObject(body, &input, v2.MaxJSONDepth); err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_token_request")
		return
	}
	claims, token, err := s.productTokens.IssueAnonymous(input.Product, installationClaims.InstallationID)
	if err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "unknown_product")
		return
	}
	httpx.WriteJSON(response, http.StatusCreated, map[string]any{
		"token": token, "token_version": "v2", "product": claims.Product, "subject_key": claims.SubjectKey,
		"issued_at": claims.IssuedAt, "expires_at": claims.ExpiresAt,
	})
}

func (s *Server) batchesV2(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	claims, err := s.authenticateProduct(request, "telemetry:write")
	if err != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_product_token")
		return
	}
	body, err := httpx.ReadBody(response, request, v2.MaxBatchBytes)
	if err != nil {
		writeBodyError(response, err)
		return
	}
	batch, err := v2.ParseBatch(body, s.now())
	if err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_batch")
		return
	}
	if err := v2.ValidateIdempotencyKey(request.Header.Get("Idempotency-Key"), batch.Header.BatchID); err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	if !scopeMatchesClaims(batch.Header.Scope, claims) {
		httpx.WriteError(response, http.StatusForbidden, "token_scope_mismatch")
		return
	}
	result, err := s.v2Writer.WriteV2(request.Context(), batch.Header.BatchID, body)
	if err != nil {
		switch {
		case errors.Is(err, ErrWriterConflict):
			httpx.WriteError(response, http.StatusConflict, "idempotency_conflict")
		case errors.Is(err, ErrWriterSuppressed):
			httpx.WriteError(response, http.StatusForbidden, "telemetry_suppressed")
		default:
			s.logger.Error("writer rejected v2 batch", "error", err)
			httpx.WriteError(response, http.StatusBadGateway, "writer_unavailable")
		}
		return
	}
	httpx.WriteJSON(response, http.StatusAccepted, map[string]any{
		"batch_id": batch.Header.BatchID, "accepted_records": result.Count, "replayed": result.Replayed,
	})
}

func (s *Server) privacyRequestV2(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	claims, err := s.authenticateProduct(request, "privacy:write")
	if err != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_product_token")
		return
	}
	body, err := httpx.ReadBody(response, request, 16*1024)
	if err != nil {
		writeBodyError(response, err)
		return
	}
	privacyRequest, err := v2.ParsePrivacyRequest(body, s.now())
	if err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_privacy_request")
		return
	}
	if err := v2.ValidateIdempotencyKey(request.Header.Get("Idempotency-Key"), privacyRequest.RequestID); err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	if !scopeMatchesClaims(privacyRequest.Scope, claims) {
		httpx.WriteError(response, http.StatusForbidden, "token_scope_mismatch")
		return
	}
	result, err := s.v2Writer.WritePrivacy(request.Context(), privacyRequest.RequestID, body)
	if err != nil {
		if errors.Is(err, ErrWriterConflict) {
			httpx.WriteError(response, http.StatusConflict, "idempotency_conflict")
			return
		}
		s.logger.Error("writer rejected privacy request", "error", err)
		httpx.WriteError(response, http.StatusBadGateway, "writer_unavailable")
		return
	}
	httpx.WriteJSON(response, http.StatusAccepted, map[string]any{
		"request_id": privacyRequest.RequestID, "status": result.Status, "replayed": result.Replayed,
	})
}

func (s *Server) authenticateProduct(request *http.Request, requiredScope string) (auth.ProductClaims, error) {
	header := request.Header.Get("Authorization")
	prefix := "Bearer "
	if !strings.HasPrefix(header, prefix) || strings.Contains(strings.TrimPrefix(header, prefix), " ") {
		return auth.ProductClaims{}, errors.New("missing bearer token")
	}
	encoded := strings.TrimPrefix(header, prefix)
	if s.identityTokens != nil {
		claims, err := s.identityTokens.Verify(request.Context(), encoded, requiredScope)
		if err == nil {
			return auth.ProductClaims{
				Product: claims.Product, SubjectKey: claims.Subject, PersonKey: claims.PersonKey, OrgKey: claims.OrgKey,
				IssuedAt: time.Unix(claims.IssuedAt, 0), ExpiresAt: time.Unix(claims.ExpiresAt, 0),
			}, nil
		}
		if !s.legacyProductTokens {
			return auth.ProductClaims{}, err
		}
	}
	if s.legacyProductTokens && s.productTokens != nil {
		return s.productTokens.Verify(encoded)
	}
	return auth.ProductClaims{}, errors.New("no product token verifier configured")
}

func scopeMatchesClaims(scope v2.Scope, claims auth.ProductClaims) bool {
	return scope.Product == claims.Product && scope.SubjectKey == claims.SubjectKey && optionalStringEqual(scope.PersonKey, claims.PersonKey) && optionalStringEqual(scope.OrgKey, claims.OrgKey)
}

func optionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func writeBodyError(response http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		httpx.WriteError(response, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}
	httpx.WriteError(response, http.StatusBadRequest, "invalid_body")
}
