package ga4

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const googleTokenEndpoint = "https://oauth2.googleapis.com/token"

// ServiceAccountTokenSource exchanges a service-account JWT for a short-lived,
// analytics.readonly token. Credentials never leave the process; only the signed
// assertion goes to Google's fixed HTTPS endpoint. Secrets belong in Lockbox.
func ServiceAccountTokenSource(data []byte, transport *http.Client) (func(context.Context) (string, error), error) {
	var credentials struct {
		Type         string `json:"type"`
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		PrivateKeyID string `json:"private_key_id"`
		TokenURI     string `json:"token_uri"`
	}
	if len(data) > 64<<10 || json.Unmarshal(data, &credentials) != nil || credentials.Type != "service_account" ||
		!regexp.MustCompile(`^[a-z0-9-]+@[a-z0-9.-]+\.iam\.gserviceaccount\.com$`).MatchString(credentials.ClientEmail) ||
		len(credentials.ClientEmail) > 254 || len(credentials.PrivateKeyID) > 128 ||
		(credentials.TokenURI != "" && credentials.TokenURI != googleTokenEndpoint) {
		return nil, errors.New("invalid GA4 service account credentials")
	}
	block, rest := pem.Decode([]byte(credentials.PrivateKey))
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 || block.Type != "PRIVATE KEY" {
		return nil, errors.New("invalid GA4 service account key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid GA4 service account key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 2048 || key.N.BitLen() > 8192 || key.Validate() != nil {
		return nil, errors.New("invalid GA4 RSA key")
	}
	h := http.Client{}
	if transport != nil {
		h = *transport
	}
	if h.Timeout <= 0 || h.Timeout > 20*time.Second {
		h.Timeout = 20 * time.Second
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	h.Jar = nil
	var mu sync.Mutex
	var cached string
	var expires time.Time
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		now := time.Now().UTC()
		if cached != "" && now.Add(time.Minute).Before(expires) {
			return cached, nil
		}
		header, _ := json.Marshal(struct {
			Algorithm string `json:"alg"`
			Type      string `json:"typ"`
			KeyID     string `json:"kid,omitempty"`
		}{"RS256", "JWT", credentials.PrivateKeyID})
		claims, _ := json.Marshal(struct {
			Issuer   string `json:"iss"`
			Scope    string `json:"scope"`
			Audience string `json:"aud"`
			Issued   int64  `json:"iat"`
			Expires  int64  `json:"exp"`
		}{credentials.ClientEmail, "https://www.googleapis.com/auth/analytics.readonly", googleTokenEndpoint, now.Add(-30 * time.Second).Unix(), now.Add(59 * time.Minute).Unix()})
		unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
		digest := sha256.Sum256([]byte(unsigned))
		signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			return "", errors.New("GA4 assertion unavailable")
		}
		assertion := unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
		form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenEndpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return "", errors.New("GA4 token request unavailable")
		}
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := h.Do(r)
		if err != nil {
			return "", errors.New("GA4 token transport unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return "", errors.New("GA4 token exchange denied")
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
		if err != nil || len(body) > 128<<10 {
			return "", errors.New("invalid GA4 token response")
		}
		var result struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
			ExpiresIn   int64  `json:"expires_in"`
		}
		if json.Unmarshal(body, &result) != nil || result.AccessToken == "" || len(result.AccessToken) > 8192 || strings.ContainsAny(result.AccessToken, " \r\n\t") || !strings.EqualFold(result.TokenType, "Bearer") || result.ExpiresIn <= 60 || result.ExpiresIn > 3600 {
			return "", errors.New("invalid GA4 token response")
		}
		cached, expires = result.AccessToken, now.Add(time.Duration(result.ExpiresIn)*time.Second)
		return cached, nil
	}, nil
}
