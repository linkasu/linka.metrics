package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"
)

func IsJSON(request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func CORS(origins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		allowed[origin] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			origin := request.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(response, request)
				return
			}
			if _, ok := allowed[origin]; !ok {
				WriteError(response, http.StatusForbidden, "origin_not_allowed")
				return
			}
			response.Header().Set("Access-Control-Allow-Origin", origin)
			response.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			response.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
			response.Header().Set("Access-Control-Max-Age", "600")
			response.Header().Add("Vary", "Origin")
			if request.Method == http.MethodOptions {
				if request.Header.Get("Access-Control-Request-Method") != http.MethodPost || !allowedCORSHeaders(request.Header.Get("Access-Control-Request-Headers")) {
					WriteError(response, http.StatusForbidden, "cors_preflight_not_allowed")
					return
				}
				response.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(response, request)
		})
	}
}

func allowedCORSHeaders(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	allowed := map[string]struct{}{"authorization": {}, "content-type": {}, "idempotency-key": {}}
	requested := strings.Split(strings.ToLower(value), ",")
	for _, header := range requested {
		if _, ok := allowed[strings.TrimSpace(header)]; !ok {
			return false
		}
	}
	return true
}

func DecodeStrict(data []byte, destination any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("expected JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after JSON object")
	}
	return nil
}

func ReadBody(response http.ResponseWriter, request *http.Request, limit int64) ([]byte, error) {
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	return body, nil
}

func WriteJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func WriteError(response http.ResponseWriter, status int, code string) {
	WriteJSON(response, status, map[string]string{"error": code})
}

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}

func RequestLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: response, status: http.StatusOK}
			next.ServeHTTP(recorder, request)
			logger.Info("http request",
				"method", request.Method,
				"path", request.URL.Path,
				"status", recorder.status,
				"duration_ms", time.Since(started).Milliseconds(),
			)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
