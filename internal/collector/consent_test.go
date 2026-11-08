package collector

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/auth"
	"github.com/linkasu/linka.plays-metric/internal/product"
)

type consentJWKS map[string]ed25519.PublicKey

func (s consentJWKS) Keys(context.Context, bool) (map[string]ed25519.PublicKey, error) { return s, nil }

func consentToken(t *testing.T, key ed25519.PrivateKey, claims auth.IdentityJWTClaims) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "test"})
	body, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(unsigned)))
}

func consentVerifier(t *testing.T, key ed25519.PrivateKey) *auth.IdentityJWTVerifier {
	t.Helper()
	verifier, err := auth.NewIdentityJWTVerifierWithSource(consentJWKS{"test": key.Public().(ed25519.PublicKey)}, "identity.test", map[product.ID]string{
		product.LinkaPlays: "plays-metrics", product.LinkaPictures: "pictures-metrics",
	}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func TestConsentRequiresExactTargetServiceScopeAndForwardsUnchanged(t *testing.T) {
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("k", 32)))
	now := time.Now().UTC()
	subject := strings.Repeat("a", 64)
	claims := auth.IdentityJWTClaims{Issuer: "identity.test", Subject: subject, Audience: "pictures-metrics", Product: product.LinkaPictures,
		SubjectType: "service", Scopes: []string{"consent:write"}, IssuedAt: now.Unix(), ExpiresAt: now.Add(5 * time.Minute).Unix(), TokenID: "10000000-0000-4000-8000-000000000001"}
	body := []byte(fmt.Sprintf(`{"schema_version":2,"request_id":%q,"scope":{"product":"linka-pictures","subject_key":%q},"consent_epoch":1,"version":1,"preference":"allowed","recorded_at":"2026-11-08T12:00:00.000Z"}`, claims.TokenID, subject))
	for _, tc := range []struct {
		name   string
		mutate func(*auth.IdentityJWTClaims)
		status int
	}{
		{"service", func(*auth.IdentityJWTClaims) {}, http.StatusOK},
		{"client", func(c *auth.IdentityJWTClaims) { c.SubjectType = "installation" }, http.StatusUnauthorized},
		{"telemetry", func(c *auth.IdentityJWTClaims) { c.Scopes = []string{"telemetry:write"} }, http.StatusUnauthorized},
		{"privacy", func(c *auth.IdentityJWTClaims) { c.Scopes = []string{"privacy:write"} }, http.StatusUnauthorized},
		{"extra scope", func(c *auth.IdentityJWTClaims) { c.Scopes = []string{"consent:write", "telemetry:write"} }, http.StatusUnauthorized},
		{"wrong subject", func(c *auth.IdentityJWTClaims) { c.Subject = strings.Repeat("b", 64) }, http.StatusForbidden},
		{"wrong product", func(c *auth.IdentityJWTClaims) { c.Product = product.LinkaPlays; c.Audience = "plays-metrics" }, http.StatusForbidden},
		{"wrong audience", func(c *auth.IdentityJWTClaims) { c.Audience = "plays-metrics" }, http.StatusUnauthorized},
		{"person scope", func(c *auth.IdentityJWTClaims) { c.PersonKey = &subject }, http.StatusUnauthorized},
		{"org scope", func(c *auth.IdentityJWTClaims) { c.OrgKey = &subject }, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := claims
			tc.mutate(&c)
			writer := &fakeV2Writer{}
			handler := NewServerWithIdentityV2(nil, writer, nil, consentVerifier(t, key), nil, false, testLogger())
			request := httptest.NewRequest(http.MethodPost, "/v2/consents", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+consentToken(t, key, c))
			request.Header.Set("Idempotency-Key", claims.TokenID)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if tc.status == http.StatusOK {
				if writer.calls != 1 || !bytes.Equal(writer.body, body) || !strings.Contains(response.Body.String(), `"status":"completed"`) {
					t.Fatalf("receipt/body mismatch: %s", response.Body)
				}
			} else if writer.calls != 0 {
				t.Fatal("unauthorized control forwarded")
			}
		})
	}
}

func TestBatchJWTAndScopeConsentEpochMustMatch(t *testing.T) {
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("k", 32)))
	now := time.Now().UTC()
	subject := strings.Repeat("a", 64)
	one, two, zero := uint64(1), uint64(2), uint64(0)
	for _, tc := range []struct {
		name        string
		claim, body *uint64
		status      int
	}{
		{"legacy", nil, nil, 202}, {"managed", &one, &one, 202}, {"missing claim", nil, &one, 403},
		{"missing body", &one, nil, 403}, {"different epoch", &one, &two, 403},
		{"invalid claim", &zero, &one, 401}, {"invalid batch", &one, &zero, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := auth.IdentityJWTClaims{Issuer: "identity.test", Subject: subject, Audience: "plays-metrics", Product: product.LinkaPlays,
				SubjectType: "installation", Scopes: []string{"telemetry:write"}, ConsentEpoch: tc.claim, IssuedAt: now.Unix(), ExpiresAt: now.Add(5 * time.Minute).Unix(), TokenID: "10000000-0000-4000-8000-000000000001"}
			body := collectorV2Batch(now, subject)
			if tc.body != nil {
				body = bytes.Replace(body, []byte(`"scope":{`), []byte(fmt.Sprintf(`"scope":{"consent_epoch":%d,`, *tc.body)), 1)
			}
			writer := &fakeV2Writer{}
			handler := NewServerWithIdentityV2(nil, writer, nil, consentVerifier(t, key), nil, false, testLogger())
			request := httptest.NewRequest(http.MethodPost, "/v2/batches", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+consentToken(t, key, claims))
			request.Header.Set("Idempotency-Key", claims.TokenID)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if tc.status != 202 && writer.calls != 0 {
				t.Fatal("mismatched epoch forwarded")
			}
		})
	}
}

func TestHTTPWriterConsentUsesExistingBoundHMAC(t *testing.T) {
	key := auth.ServiceKey{ID: "test", Secret: []byte(strings.Repeat("k", 32))}
	verifier, _ := auth.NewServiceVerifier(key, nil, "collector", 5*time.Minute)
	id := "10000000-0000-4000-8000-000000000001"
	body := []byte(`{"exact":"body"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/internal/v2/consents" || !bytes.Equal(got, body) || r.Header.Get("Idempotency-Key") != id || verifier.Verify(r.Method, r.URL.EscapedPath(), got, auth.ServiceHeadersFromRequest(r)) != nil {
			t.Error("consent forwarding lost HMAC/body/id binding")
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"request_id": id, "status": "completed", "replayed": true})
	}))
	defer server.Close()
	writer, err := NewHTTPWriterWithServiceKey(server.URL, key.Secret, key, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := writer.WriteConsent(t.Context(), id, body)
	if err != nil || result.RequestID != id || result.Status != "completed" || !result.Replayed {
		t.Fatalf("receipt=%+v err=%v", result, err)
	}
}

func TestConsentNeverFallsBackToLegacyToken(t *testing.T) {
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("k", 32)))
	legacy, err := auth.NewProductTokens(auth.ServiceKey{ID: "test", Secret: []byte(strings.Repeat("s", 32))}, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := legacy.IssueAnonymous(product.LinkaPictures, "10000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	writer := &fakeV2Writer{}
	handler := NewServerWithIdentityV2(nil, writer, nil, consentVerifier(t, key), legacy, true, testLogger())
	request := httptest.NewRequest(http.MethodPost, "/v2/consents", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || writer.calls != 0 {
		t.Fatalf("legacy control forwarded: status=%d calls=%d", response.Code, writer.calls)
	}
}

func TestHTTPWriterRejectsIncompleteOrMismatchedConsentReceipt(t *testing.T) {
	id := "10000000-0000-4000-8000-000000000001"
	for _, body := range []string{`{}`, `{"request_id":"wrong","status":"completed","replayed":false}`, fmt.Sprintf(`{"request_id":%q,"status":"pending","replayed":false}`, id), `not JSON`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		writer, err := NewHTTPWriter(server.URL, []byte(strings.Repeat("s", 32)), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, err = writer.WriteConsent(t.Context(), id, []byte(`{}`))
		server.Close()
		if err == nil {
			t.Fatalf("invalid writer receipt accepted: %s", body)
		}
	}
}
