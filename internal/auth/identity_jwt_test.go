package auth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/product"
)

type staticJWKS map[string]ed25519.PublicKey

func (s staticJWKS) Keys(context.Context, bool) (map[string]ed25519.PublicKey, error) { return s, nil }

func TestIdentityJWTVerifierChecksSignatureAudienceScopeAndLifetime(t *testing.T) {
	seed := []byte(strings.Repeat("s", 32))
	privateKey := ed25519.NewKeyFromSeed(seed)
	verifier, err := NewIdentityJWTVerifierWithSource(staticJWKS{"active": privateKey.Public().(ed25519.PublicKey)}, "identity.test", map[product.ID]string{
		product.LinkaPlays: "linka-plays-metric",
		product.LinkaLooks: "linka-looks-metric",
	}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	verifier.now = func() time.Time { return now }
	claims := IdentityJWTClaims{
		Issuer: "identity.test", Subject: strings.Repeat("a", 64), Audience: "linka-plays-metric", Product: product.LinkaPlays,
		SubjectType: "installation", Scopes: []string{"telemetry:write"}, IssuedAt: now.Unix(), ExpiresAt: now.Add(5 * time.Minute).Unix(),
		TokenID: "10000000-0000-4000-8000-000000000001",
	}
	token := signIdentityJWT(t, privateKey, "active", claims)
	verified, err := verifier.Verify(context.Background(), token, "telemetry:write")
	if err != nil || verified.Subject != claims.Subject {
		t.Fatalf("verify claims: %+v %v", verified, err)
	}
	if _, err := verifier.Verify(context.Background(), token, "privacy:write"); err == nil {
		t.Fatal("wrong scope was accepted")
	}
	wrongAudience := claims
	wrongAudience.Audience = "another-service"
	if _, err := verifier.Verify(context.Background(), signIdentityJWT(t, privateKey, "active", wrongAudience), "telemetry:write"); err == nil {
		t.Fatal("wrong audience was accepted")
	}
	longLived := claims
	longLived.ExpiresAt = now.Add(16 * time.Minute).Unix()
	if _, err := verifier.Verify(context.Background(), signIdentityJWT(t, privateKey, "active", longLived), "telemetry:write"); err == nil {
		t.Fatal("excessive token lifetime was accepted")
	}
	missingSubjectType := claims
	missingSubjectType.SubjectType = ""
	if _, err := verifier.Verify(context.Background(), signIdentityJWT(t, privateKey, "active", missingSubjectType), "telemetry:write"); err == nil {
		t.Fatal("missing subject type was accepted")
	}
}

func TestIdentityJWTVerifierUsesProductSpecificAudience(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("a", 32)))
	verifier, err := NewIdentityJWTVerifierWithSource(staticJWKS{"active": privateKey.Public().(ed25519.PublicKey)}, "identity.test", map[product.ID]string{
		product.LinkaPlays: "linka-plays-metric",
		product.LinkaLooks: "linka-looks-metric",
	}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	verifier.now = func() time.Time { return now }
	claims := IdentityJWTClaims{
		Issuer: "identity.test", Subject: strings.Repeat("a", 64), Audience: "linka-looks-metric", Product: product.LinkaLooks,
		SubjectType: "installation", Scopes: []string{"telemetry:write"}, IssuedAt: now.Unix(), ExpiresAt: now.Add(5 * time.Minute).Unix(),
		TokenID: "10000000-0000-4000-8000-000000000001",
	}
	if _, err := verifier.Verify(context.Background(), signIdentityJWT(t, privateKey, "active", claims), "telemetry:write"); err != nil {
		t.Fatal(err)
	}
	claims.Audience = "linka-plays-metric"
	if _, err := verifier.Verify(context.Background(), signIdentityJWT(t, privateKey, "active", claims), "telemetry:write"); err == nil {
		t.Fatal("audience from another product was accepted")
	}
}

func TestHTTPJWKSDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte(`{"keys":[]}`))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Location", target.URL)
		response.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()
	source, err := NewHTTPJWKS(redirect.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Keys(context.Background(), false); err == nil {
		t.Fatal("JWKS redirect was followed")
	}
}

func TestHTTPJWKSUsesBoundedStaleCacheOnRefreshFailure(t *testing.T) {
	publicKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("k", 32))).Public().(ed25519.PublicKey)
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if fail {
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA", "kid": "active",
			"x": base64.RawURLEncoding.EncodeToString(publicKey), "x-linka-state": "active",
		}}})
	}))
	defer server.Close()
	source, err := NewHTTPJWKS(server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Keys(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	fail = true
	source.fetchedAt = time.Now().Add(-10 * time.Minute)
	if keys, err := source.Keys(context.Background(), false); err != nil || len(keys) != 1 {
		t.Fatalf("stale cache: keys=%d err=%v", len(keys), err)
	}
	if _, err := source.Keys(context.Background(), true); err == nil {
		t.Fatal("forced unknown-key refresh used stale cache")
	}
}

func signIdentityJWT(t *testing.T, privateKey ed25519.PrivateKey, keyID string, claims IdentityJWTClaims) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": keyID})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(unsigned)))
}
