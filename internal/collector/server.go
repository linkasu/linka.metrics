package collector

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/linkasu/linka.plays-metric/internal/auth"
	v1 "github.com/linkasu/linka.plays-metric/internal/contract/v1"
	"github.com/linkasu/linka.plays-metric/internal/httpx"
)

const privacyText = `LINKa Plays Metric принимает только предусмотренную контрактом v1 обезличенную техническую, продуктовую и игровую телеметрию.

Сервис не принимает имя, контакты, текст или произнесённые фразы, координаты взгляда и указателя, ожидаемые и фактические ответы, идентификаторы целей, пути файлов, сообщения и стеки ошибок. Ошибки передаются только как заранее вычисленный стабильный fingerprint и безопасное имя компонента.

Идентификатор установки создаётся случайно и не связан с учётной записью, устройством или IP-адресом. Выданный HMAC-токен лишь защищает этот случайный идентификатор от изменения и не подтверждает подлинность приложения или пользователя. IP-адрес, заголовки авторизации и тела запросов не записываются в журналы приложения.

Данные хранятся без автоматического TTL. Порядок хранения и удаления определяется оператором системы. Технический контакт: ivan@aacidov.ru.
`

type EventWriter interface {
	Write(context.Context, []byte) error
}

type Server struct {
	writer EventWriter
	tokens *auth.InstallationTokens
	logger *slog.Logger
}

func NewServer(writer EventWriter, tokens *auth.InstallationTokens, logger *slog.Logger) http.Handler {
	server := &Server{writer: writer, tokens: tokens, logger: logger}
	router := chi.NewRouter()
	router.Use(httpx.SecurityHeaders)
	router.Use(httpx.RequestLog(logger))
	router.Get("/healthz", server.health)
	router.Get("/privacy", server.privacy)
	router.Post("/v1/installations", server.installation)
	router.Post("/v1/events", server.events)
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
		s.logger.Error("writer rejected event batch", "error", err)
		httpx.WriteError(response, http.StatusBadGateway, "writer_unavailable")
		return
	}
	httpx.WriteJSON(response, http.StatusAccepted, map[string]int{
		"accepted_events":            len(batch.Events),
		"accepted_session_summaries": len(batch.SessionSummaries),
	})
}

func (s *Server) authenticate(header string) (auth.InstallationClaims, error) {
	prefix := "Bearer "
	if !strings.HasPrefix(header, prefix) || strings.Contains(strings.TrimPrefix(header, prefix), " ") {
		return auth.InstallationClaims{}, errors.New("missing bearer token")
	}
	return s.tokens.Verify(strings.TrimPrefix(header, prefix))
}

func writeBodyError(response http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		httpx.WriteError(response, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}
	httpx.WriteError(response, http.StatusBadRequest, "invalid_body")
}
