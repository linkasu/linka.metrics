package writer

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/linkasu/linka.plays-metric/internal/auth"
	"github.com/linkasu/linka.plays-metric/internal/contract/fundraising"
	v1 "github.com/linkasu/linka.plays-metric/internal/contract/v1"
	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
	"github.com/linkasu/linka.plays-metric/internal/httpx"
)

type Store interface {
	Ping(context.Context) error
	Insert(context.Context, v1.ValidatedBatch) error
}

type StoreV2 interface {
	InsertV2(context.Context, v2.ValidatedBatch, string) (v2.IngestResult, error)
	CreatePrivacyRequest(context.Context, v2.ValidatedPrivacyRequest, string) (v2.PrivacyResult, error)
	CreateLegacyPrivacyRequest(context.Context, v1.ValidatedPrivacyRequest, string) (v2.PrivacyResult, error)
}

type FundraisingStore interface {
	InsertFundraising(context.Context, fundraising.ValidatedBatch, string) (fundraising.IngestResult, error)
}

type Server struct {
	store            Store
	storeV2          StoreV2
	fundraisingStore FundraisingStore
	secret           []byte
	serviceVerifier  *auth.ServiceVerifier
	logger           *slog.Logger
	maxSkew          time.Duration
	now              func() time.Time
}

func NewServer(store Store, secret []byte, logger *slog.Logger, maxSkew time.Duration) http.Handler {
	return newServer(store, nil, nil, secret, nil, logger, maxSkew)
}

func NewServerWithV2(store Store, storeV2 StoreV2, secret []byte, serviceVerifier *auth.ServiceVerifier, logger *slog.Logger, maxSkew time.Duration) http.Handler {
	return newServer(store, storeV2, nil, secret, serviceVerifier, logger, maxSkew)
}

func NewServerWithV2AndFundraising(store Store, storeV2 StoreV2, fundraisingStore FundraisingStore, secret []byte, serviceVerifier *auth.ServiceVerifier, logger *slog.Logger, maxSkew time.Duration) http.Handler {
	return newServer(store, storeV2, fundraisingStore, secret, serviceVerifier, logger, maxSkew)
}

func newServer(store Store, storeV2 StoreV2, fundraisingStore FundraisingStore, secret []byte, serviceVerifier *auth.ServiceVerifier, logger *slog.Logger, maxSkew time.Duration) http.Handler {
	server := &Server{
		store:            store,
		storeV2:          storeV2,
		fundraisingStore: fundraisingStore,
		secret:           append([]byte(nil), secret...),
		serviceVerifier:  serviceVerifier,
		logger:           logger,
		maxSkew:          maxSkew,
		now:              time.Now,
	}
	router := chi.NewRouter()
	router.Use(httpx.SecurityHeaders)
	router.Use(httpx.RequestLog(logger))
	router.Get("/healthz", server.health)
	router.Post("/internal/v1/events", server.events)
	if storeV2 != nil && serviceVerifier != nil {
		router.Post("/internal/v2/batches", server.batchesV2)
		router.Post("/internal/v2/privacy/requests", server.privacyRequestV2)
		router.Post("/internal/v1/privacy/requests", server.privacyRequestV1)
	}
	if fundraisingStore != nil && serviceVerifier != nil {
		router.Post("/internal/fundraising/batches", server.fundraisingBatches)
	}
	return router
}

func (s *Server) fundraisingBatches(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	body, err := httpx.ReadBody(response, request, fundraising.MaxBatchBytes)
	if err != nil {
		writeV2BodyError(response, err)
		return
	}
	serviceHeaders := auth.ServiceHeadersFromRequest(request)
	if err := s.serviceVerifier.Verify(request.Method, request.URL.EscapedPath(), body, serviceHeaders); err != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_service_signature")
		return
	}
	batch, err := fundraising.ParseBatch(body, s.now())
	if err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_fundraising_batch")
		return
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if err := fundraising.ValidateIdempotencyKey(idempotencyKey, batch.BatchID); err != nil || serviceHeaders.RequestID != idempotencyKey {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	result, err := s.fundraisingStore.InsertFundraising(request.Context(), batch, fundraising.BodySHA256(body))
	if err != nil {
		if errors.Is(err, fundraising.ErrIdempotencyConflict) {
			httpx.WriteError(response, http.StatusConflict, "idempotency_conflict")
			return
		}
		s.logger.Error("insert fundraising batch", "error", err)
		httpx.WriteError(response, http.StatusServiceUnavailable, "store_unavailable")
		return
	}
	httpx.WriteJSON(response, http.StatusAccepted, map[string]any{
		"batch_id": batch.BatchID, "accepted_records": result.Count, "replayed": result.Replayed,
	})
}

func (s *Server) health(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		httpx.WriteError(response, http.StatusServiceUnavailable, "store_unavailable")
		return
	}
	httpx.WriteJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) events(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	body, err := httpx.ReadBody(response, request, v1.MaxBatchBytes)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			httpx.WriteError(response, http.StatusRequestEntityTooLarge, "body_too_large")
			return
		}
		httpx.WriteError(response, http.StatusBadRequest, "invalid_body")
		return
	}
	if err := auth.VerifyWriterRequest(
		s.secret,
		body,
		s.now(),
		s.maxSkew,
		request.Header.Get(auth.WriterTimestampHeader),
		request.Header.Get(auth.WriterBodySHAHeader),
		request.Header.Get(auth.WriterSignatureHeader),
	); err != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_writer_signature")
		return
	}
	batch, err := v1.ParseBatch(body)
	if err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_event_batch")
		return
	}
	if err := s.store.Insert(request.Context(), batch); err != nil {
		if errors.Is(err, v2.ErrSuppressed) {
			httpx.WriteError(response, http.StatusForbidden, "telemetry_suppressed")
			return
		}
		s.logger.Error("insert event batch", "error", err)
		httpx.WriteError(response, http.StatusServiceUnavailable, "store_unavailable")
		return
	}
	httpx.WriteJSON(response, http.StatusAccepted, map[string]int{
		"inserted_events":            len(batch.Events),
		"inserted_session_summaries": len(batch.SessionSummaries),
	})
}

func (s *Server) batchesV2(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	body, err := httpx.ReadBody(response, request, v2.MaxBatchBytes)
	if err != nil {
		writeV2BodyError(response, err)
		return
	}
	serviceHeaders := auth.ServiceHeadersFromRequest(request)
	if err := s.serviceVerifier.Verify(request.Method, request.URL.EscapedPath(), body, serviceHeaders); err != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_service_signature")
		return
	}
	batch, err := v2.ParseBatch(body, s.now())
	if err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_batch")
		return
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if err := v2.ValidateIdempotencyKey(idempotencyKey, batch.Header.BatchID); err != nil || serviceHeaders.RequestID != idempotencyKey {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	result, err := s.storeV2.InsertV2(request.Context(), batch, v2.BodySHA256(body))
	if err != nil {
		switch {
		case errors.Is(err, v2.ErrIdempotencyConflict):
			httpx.WriteError(response, http.StatusConflict, "idempotency_conflict")
		case errors.Is(err, v2.ErrDuplicateRecord):
			httpx.WriteError(response, http.StatusConflict, "duplicate_record_id")
		case errors.Is(err, v2.ErrSuppressed):
			httpx.WriteError(response, http.StatusForbidden, "telemetry_suppressed")
		default:
			s.logger.Error("insert v2 batch", "error", err)
			httpx.WriteError(response, http.StatusServiceUnavailable, "store_unavailable")
		}
		return
	}
	httpx.WriteJSON(response, http.StatusAccepted, map[string]any{
		"batch_id": batch.Header.BatchID, "accepted_records": result.Count, "replayed": result.Replayed,
	})
}

func (s *Server) privacyRequestV1(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	body, err := httpx.ReadBody(response, request, 16*1024)
	if err != nil {
		writeV2BodyError(response, err)
		return
	}
	serviceHeaders := auth.ServiceHeadersFromRequest(request)
	if err := s.serviceVerifier.Verify(request.Method, request.URL.EscapedPath(), body, serviceHeaders); err != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_service_signature")
		return
	}
	privacyRequest, err := v1.ParseInternalPrivacyRequest(body, s.now())
	if err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_privacy_request")
		return
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if err := v2.ValidateIdempotencyKey(idempotencyKey, privacyRequest.RequestID); err != nil || serviceHeaders.RequestID != idempotencyKey {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	result, err := s.storeV2.CreateLegacyPrivacyRequest(request.Context(), privacyRequest, v2.BodySHA256(body))
	if err != nil {
		if errors.Is(err, v2.ErrIdempotencyConflict) {
			httpx.WriteError(response, http.StatusConflict, "idempotency_conflict")
			return
		}
		s.logger.Error("persist V1 privacy request", "error", err)
		httpx.WriteError(response, http.StatusServiceUnavailable, "store_unavailable")
		return
	}
	httpx.WriteJSON(response, http.StatusAccepted, map[string]any{
		"request_id": privacyRequest.RequestID, "status": result.Status, "replayed": result.Replayed,
	})
}

func (s *Server) privacyRequestV2(response http.ResponseWriter, request *http.Request) {
	if !httpx.IsJSON(request) {
		httpx.WriteError(response, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	body, err := httpx.ReadBody(response, request, 16*1024)
	if err != nil {
		writeV2BodyError(response, err)
		return
	}
	serviceHeaders := auth.ServiceHeadersFromRequest(request)
	if err := s.serviceVerifier.Verify(request.Method, request.URL.EscapedPath(), body, serviceHeaders); err != nil {
		httpx.WriteError(response, http.StatusUnauthorized, "invalid_service_signature")
		return
	}
	privacyRequest, err := v2.ParsePrivacyRequest(body, s.now())
	if err != nil {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_privacy_request")
		return
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if err := v2.ValidateIdempotencyKey(idempotencyKey, privacyRequest.RequestID); err != nil || serviceHeaders.RequestID != idempotencyKey {
		httpx.WriteError(response, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}
	result, err := s.storeV2.CreatePrivacyRequest(request.Context(), privacyRequest, v2.BodySHA256(body))
	if err != nil {
		if errors.Is(err, v2.ErrIdempotencyConflict) {
			httpx.WriteError(response, http.StatusConflict, "idempotency_conflict")
			return
		}
		s.logger.Error("persist privacy request", "error", err)
		httpx.WriteError(response, http.StatusServiceUnavailable, "store_unavailable")
		return
	}
	httpx.WriteJSON(response, http.StatusAccepted, map[string]any{
		"request_id": privacyRequest.RequestID, "status": result.Status, "replayed": result.Replayed,
	})
}

func writeV2BodyError(response http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		httpx.WriteError(response, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}
	httpx.WriteError(response, http.StatusBadRequest, "invalid_body")
}
