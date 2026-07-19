package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/linkasu/linka.plays-metric/internal/product"
)

type IdentityJWTClaims struct {
	Issuer      string     `json:"iss"`
	Subject     string     `json:"sub"`
	Audience    string     `json:"aud"`
	Product     product.ID `json:"product"`
	SubjectType string     `json:"subject_type"`
	Scopes      []string   `json:"scope"`
	PersonKey   *string    `json:"person_key,omitempty"`
	OrgKey      *string    `json:"org_key,omitempty"`
	IssuedAt    int64      `json:"iat"`
	ExpiresAt   int64      `json:"exp"`
	TokenID     string     `json:"jti"`
}

type JWKSKeySource interface {
	Keys(context.Context, bool) (map[string]ed25519.PublicKey, error)
}

type IdentityJWTVerifier struct {
	source      JWKSKeySource
	issuer      string
	audiences   map[product.ID]string
	maxLifetime time.Duration
	maxSkew     time.Duration
	now         func() time.Time
}

func NewIdentityJWTVerifier(jwksURL, issuer string, audiences map[product.ID]string, maxLifetime time.Duration, allowHTTP bool) (*IdentityJWTVerifier, error) {
	source, err := NewHTTPJWKS(jwksURL, allowHTTP)
	if err != nil {
		return nil, err
	}
	return NewIdentityJWTVerifierWithSource(source, issuer, audiences, maxLifetime)
}

func NewIdentityJWTVerifierWithSource(source JWKSKeySource, issuer string, audiences map[product.ID]string, maxLifetime time.Duration) (*IdentityJWTVerifier, error) {
	if source == nil || issuer == "" || len(audiences) == 0 || maxLifetime <= 0 || maxLifetime > time.Hour {
		return nil, errors.New("invalid Identity JWT verifier configuration")
	}
	configured := make(map[product.ID]string, len(audiences))
	for productID, audience := range audiences {
		if _, ok := product.Lookup(productID); !ok || strings.TrimSpace(audience) == "" {
			return nil, errors.New("invalid Identity JWT product audience")
		}
		configured[productID] = audience
	}
	return &IdentityJWTVerifier{source: source, issuer: issuer, audiences: configured, maxLifetime: maxLifetime, maxSkew: 30 * time.Second, now: time.Now}, nil
}

func (v *IdentityJWTVerifier) Verify(ctx context.Context, encoded, requiredScope string) (IdentityJWTClaims, error) {
	parts := strings.Split(encoded, ".")
	if len(parts) != 3 || len(encoded) > 8192 {
		return IdentityJWTClaims{}, errors.New("invalid Identity JWT")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return IdentityJWTClaims{}, errors.New("invalid Identity JWT header")
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
		KeyID     string `json:"kid"`
	}
	if err := decodeJWTPart(headerBytes, &header); err != nil || header.Algorithm != "EdDSA" || header.Type != "JWT" || header.KeyID == "" {
		return IdentityJWTClaims{}, errors.New("invalid Identity JWT header")
	}
	keys, err := v.source.Keys(ctx, false)
	if err != nil {
		return IdentityJWTClaims{}, fmt.Errorf("load Identity JWKS: %w", err)
	}
	publicKey, ok := keys[header.KeyID]
	if !ok {
		keys, err = v.source.Keys(ctx, true)
		if err != nil {
			return IdentityJWTClaims{}, fmt.Errorf("refresh Identity JWKS: %w", err)
		}
		publicKey, ok = keys[header.KeyID]
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ok || !ed25519.Verify(publicKey, []byte(parts[0]+"."+parts[1]), signature) {
		return IdentityJWTClaims{}, errors.New("invalid Identity JWT signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return IdentityJWTClaims{}, errors.New("invalid Identity JWT payload")
	}
	var claims IdentityJWTClaims
	if err := decodeJWTPart(payload, &claims); err != nil {
		return IdentityJWTClaims{}, errors.New("invalid Identity JWT claims")
	}
	now := v.now().UTC()
	issuedAt := time.Unix(claims.IssuedAt, 0)
	expiresAt := time.Unix(claims.ExpiresAt, 0)
	if claims.Issuer != v.issuer || claims.Subject == "" || !opaqueKey(claims.Subject) ||
		claims.TokenID == "" || !canonicalUUID(claims.TokenID) || issuedAt.After(now.Add(v.maxSkew)) || !expiresAt.After(now) ||
		!expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > v.maxLifetime || !containsScope(claims.Scopes, requiredScope) ||
		!validIdentitySubjectType(claims.SubjectType) {
		return IdentityJWTClaims{}, errors.New("invalid Identity JWT claims")
	}
	if _, ok := product.Lookup(claims.Product); !ok {
		return IdentityJWTClaims{}, errors.New("unknown Identity JWT product")
	}
	if audience, ok := v.audiences[claims.Product]; !ok || claims.Audience != audience {
		return IdentityJWTClaims{}, errors.New("invalid Identity JWT audience for product")
	}
	for _, key := range []*string{claims.PersonKey, claims.OrgKey} {
		if key != nil && !opaqueKey(*key) {
			return IdentityJWTClaims{}, errors.New("invalid Identity JWT opaque scope")
		}
	}
	return claims, nil
}

func validIdentitySubjectType(subjectType string) bool {
	return subjectType == "account" || subjectType == "installation" || subjectType == "service"
}

func decodeJWTPart(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return nil
}

func containsScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

type HTTPJWKS struct {
	url       string
	client    *http.Client
	cacheTTL  time.Duration
	maxStale  time.Duration
	mu        sync.Mutex
	keys      map[string]ed25519.PublicKey
	fetchedAt time.Time
}

func NewHTTPJWKS(rawURL string, allowHTTP bool) (*HTTPJWKS, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http")) || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("IDENTITY_JWKS_URL must be an absolute allowed URL without credentials, query, or fragment")
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &HTTPJWKS{url: parsed.String(), client: client, cacheTTL: 5 * time.Minute, maxStale: time.Hour}, nil
}

func (s *HTTPJWKS) Keys(ctx context.Context, force bool) (map[string]ed25519.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && len(s.keys) > 0 && time.Since(s.fetchedAt) < s.cacheTTL {
		return clonePublicKeys(s.keys), nil
	}
	fallback := func(err error) (map[string]ed25519.PublicKey, error) {
		if !force && len(s.keys) > 0 && time.Since(s.fetchedAt) < s.maxStale {
			return clonePublicKeys(s.keys), nil
		}
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return fallback(err)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return fallback(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fallback(fmt.Errorf("JWKS returned HTTP %d", response.StatusCode))
	}
	var document struct {
		Keys []struct {
			KeyType   string `json:"kty"`
			Curve     string `json:"crv"`
			Use       string `json:"use"`
			Algorithm string `json:"alg"`
			KeyID     string `json:"kid"`
			X         string `json:"x"`
			State     string `json:"x-linka-state"`
		} `json:"keys"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || len(document.Keys) == 0 {
		return fallback(errors.New("invalid Identity JWKS"))
	}
	keys := make(map[string]ed25519.PublicKey, len(document.Keys))
	for _, key := range document.Keys {
		decoded, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil || len(decoded) != ed25519.PublicKeySize || key.KeyType != "OKP" || key.Curve != "Ed25519" ||
			key.Use != "sig" || key.Algorithm != "EdDSA" || key.KeyID == "" || (key.State != "active" && key.State != "retiring") {
			return fallback(errors.New("invalid Identity JWKS key"))
		}
		if _, exists := keys[key.KeyID]; exists {
			return fallback(errors.New("duplicate Identity JWKS key ID"))
		}
		keys[key.KeyID] = ed25519.PublicKey(append([]byte(nil), decoded...))
	}
	s.keys = keys
	s.fetchedAt = time.Now()
	return clonePublicKeys(keys), nil
}

func clonePublicKeys(keys map[string]ed25519.PublicKey) map[string]ed25519.PublicKey {
	result := make(map[string]ed25519.PublicKey, len(keys))
	for keyID, key := range keys {
		result[keyID] = append(ed25519.PublicKey(nil), key...)
	}
	return result
}
