package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSAllowsExactOriginAndPreflightHeaders(t *testing.T) {
	handler := CORS([]string{"https://linka.su"})(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusAccepted)
	}))
	request := httptest.NewRequest(http.MethodOptions, "/v2/batches", nil)
	request.Header.Set("Origin", "https://linka.su")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "authorization, content-type, idempotency-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "https://linka.su" {
		t.Fatalf("status=%d origin=%q", response.Code, response.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSRejectsUnknownOriginAndHeader(t *testing.T) {
	handler := CORS([]string{"https://linka.su"})(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusAccepted)
	}))
	for _, test := range []struct {
		origin  string
		headers string
	}{
		{"https://evil.example", "authorization"},
		{"https://linka.su", "x-private"},
	} {
		request := httptest.NewRequest(http.MethodOptions, "/v2/batches", nil)
		request.Header.Set("Origin", test.origin)
		request.Header.Set("Access-Control-Request-Method", http.MethodPost)
		request.Header.Set("Access-Control-Request-Headers", test.headers)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("origin=%q headers=%q status=%d", test.origin, test.headers, response.Code)
		}
	}
}

func TestCORSLeavesNativeRequestsUnchanged(t *testing.T) {
	handler := CORS(nil)(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusAccepted)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v2/batches", nil))
	if response.Code != http.StatusAccepted || response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("status=%d origin=%q", response.Code, response.Header().Get("Access-Control-Allow-Origin"))
	}
}
