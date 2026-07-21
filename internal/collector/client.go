package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/auth"
)

type HTTPWriter struct {
	v1Endpoint          string
	v2BatchEndpoint     string
	v2PrivacyEndpoint   string
	v1PrivacyEndpoint   string
	fundraisingEndpoint string
	secret              []byte
	serviceSigner       *auth.ServiceSigner
	client              *http.Client
	now                 func() time.Time
}

var (
	ErrWriterConflict   = errors.New("writer idempotency conflict")
	ErrWriterSuppressed = errors.New("writer scope suppressed")
)

func NewHTTPWriter(baseURL string, secret []byte, timeout time.Duration) (*HTTPWriter, error) {
	return NewHTTPWriterWithServiceKey(baseURL, secret, auth.ServiceKey{ID: "default", Secret: secret}, timeout)
}

func NewHTTPWriterWithServiceKey(baseURL string, v1Secret []byte, serviceKey auth.ServiceKey, timeout time.Duration) (*HTTPWriter, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("invalid WRITER_URL")
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	endpoint := func(path string) string {
		copy := *parsed
		copy.Path = basePath + path
		return copy.String()
	}
	signer, err := auth.NewServiceSigner(serviceKey, "collector")
	if err != nil {
		return nil, fmt.Errorf("configure service signer: %w", err)
	}
	return &HTTPWriter{
		v1Endpoint: endpoint("/internal/v1/events"), v2BatchEndpoint: endpoint("/internal/v2/batches"),
		v2PrivacyEndpoint: endpoint("/internal/v2/privacy/requests"), v1PrivacyEndpoint: endpoint("/internal/v1/privacy/requests"),
		fundraisingEndpoint: endpoint("/internal/fundraising/batches"),
		secret:              append([]byte(nil), v1Secret...),
		serviceSigner:       signer, client: &http.Client{Timeout: timeout}, now: time.Now,
	}, nil
}

func (w *HTTPWriter) WriteFundraising(ctx context.Context, batchID string, body []byte) (fundraisingWriteResult, error) {
	var response struct {
		AcceptedRecords int  `json:"accepted_records"`
		Replayed        bool `json:"replayed"`
	}
	if err := w.callV2(ctx, w.fundraisingEndpoint, batchID, body, &response); err != nil {
		return fundraisingWriteResult{}, err
	}
	return fundraisingWriteResult{Count: response.AcceptedRecords, Replayed: response.Replayed}, nil
}

func (w *HTTPWriter) WriteLegacyPrivacy(ctx context.Context, requestID string, body []byte) (privacyWriteResult, error) {
	var response struct {
		Status   string `json:"status"`
		Replayed bool   `json:"replayed"`
	}
	if err := w.callV2(ctx, w.v1PrivacyEndpoint, requestID, body, &response); err != nil {
		return privacyWriteResult{}, err
	}
	return privacyWriteResult{Status: response.Status, Replayed: response.Replayed}, nil
}

func (w *HTTPWriter) Write(ctx context.Context, body []byte) error {
	timestamp, bodySHA, signature := auth.SignWriterRequest(w.secret, body, w.now())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.v1Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create writer request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.WriterTimestampHeader, timestamp)
	request.Header.Set(auth.WriterBodySHAHeader, bodySHA)
	request.Header.Set(auth.WriterSignatureHeader, signature)
	response, err := w.client.Do(request)
	if err != nil {
		return fmt.Errorf("call writer: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return ErrWriterSuppressed
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("writer returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (w *HTTPWriter) WriteV2(ctx context.Context, requestID string, body []byte) (v2WriteResult, error) {
	var response struct {
		AcceptedRecords int  `json:"accepted_records"`
		Replayed        bool `json:"replayed"`
	}
	if err := w.callV2(ctx, w.v2BatchEndpoint, requestID, body, &response); err != nil {
		return v2WriteResult{}, err
	}
	return v2WriteResult{Count: response.AcceptedRecords, Replayed: response.Replayed}, nil
}

func (w *HTTPWriter) WritePrivacy(ctx context.Context, requestID string, body []byte) (privacyWriteResult, error) {
	var response struct {
		Status   string `json:"status"`
		Replayed bool   `json:"replayed"`
	}
	if err := w.callV2(ctx, w.v2PrivacyEndpoint, requestID, body, &response); err != nil {
		return privacyWriteResult{}, err
	}
	return privacyWriteResult{Status: response.Status, Replayed: response.Replayed}, nil
}

func (w *HTTPWriter) callV2(ctx context.Context, endpoint, requestID string, body []byte, destination any) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse writer endpoint: %w", err)
	}
	headers, err := w.serviceSigner.Sign(http.MethodPost, parsed.EscapedPath(), requestID, body)
	if err != nil {
		return fmt.Errorf("sign writer request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create writer request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", requestID)
	auth.ApplyServiceHeaders(request.Header, headers)
	response, err := w.client.Do(request)
	if err != nil {
		return fmt.Errorf("call writer: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return ErrWriterConflict
	}
	if response.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return ErrWriterSuppressed
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("writer returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16*1024))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode writer response: %w", err)
	}
	return nil
}
