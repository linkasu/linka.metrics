package ga4

import (
	"context"
	"crypto"
	"crypto/ed25519"
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
	"reflect"
	"strings"
	"testing"
	"time"
)

const fixtureTokenURL = "https://oauth2.googleapis.com/token"
const fixtureServiceEmail = "svc@fixture-project.iam.gserviceaccount.com"

func serviceAccountFixture(t *testing.T) (map[string]string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"type": "service_account", "client_email": fixtureServiceEmail,
		"private_key": fixturePrivateKey(t, key), "private_key_id": "fixture-key-id",
		"token_uri": fixtureTokenURL,
	}, key
}

func fixturePrivateKey(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func fixtureCredentialsJSON(t *testing.T, credentials map[string]string) []byte {
	t.Helper()
	data, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertCredentialError(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected rejection")
	}
	for _, secret := range append(secrets, fixtureServiceEmail, "fixture-key-id", "PRIVATE KEY") {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatal("error leaked credential material")
		}
	}
}

func TestServiceAccountTokenSourceAssertionAndCache(t *testing.T) {
	credentials, key := serviceAccountFixture(t)
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.URL.String() != fixtureTokenURL {
			t.Fatal("request must use the fixed Google token endpoint and POST")
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected authentication headers")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if len(r.PostForm) != 2 || len(r.PostForm["grant_type"]) != 1 || len(r.PostForm["assertion"]) != 1 || r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Fatal("expected only JWT bearer grant and assertion")
		}
		parts := strings.Split(r.PostForm.Get("assertion"), ".")
		if len(parts) != 3 {
			t.Fatal("assertion is not a JWT")
		}
		decode := func(part string, target any) {
			t.Helper()
			data, err := base64.RawURLEncoding.DecodeString(part)
			if err != nil || json.Unmarshal(data, target) != nil {
				t.Fatal("invalid JWT encoding")
			}
		}
		var header map[string]string
		decode(parts[0], &header)
		if !reflect.DeepEqual(header, map[string]string{"alg": "RS256", "typ": "JWT", "kid": "fixture-key-id"}) {
			t.Fatal("unexpected JWT header")
		}
		var claims struct {
			Issuer   string `json:"iss"`
			Scope    string `json:"scope"`
			Audience string `json:"aud"`
			Issued   int64  `json:"iat"`
			Expires  int64  `json:"exp"`
		}
		decode(parts[1], &claims)
		if claims.Issuer != fixtureServiceEmail || claims.Scope != "https://www.googleapis.com/auth/analytics.readonly" || claims.Audience != fixtureTokenURL {
			t.Fatal("unexpected issuer, scope or audience")
		}
		now := time.Now().Unix()
		if claims.Issued < now-60 || claims.Issued > now+5 || claims.Expires <= now || claims.Expires <= claims.Issued || claims.Expires-claims.Issued > 3600 {
			t.Fatal("JWT lifetime must be current and at most one hour")
		}
		signature, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			t.Fatal("invalid JWT signature encoding")
		}
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature) != nil {
			t.Fatal("JWT signature verification failed")
		}
		return response(http.StatusOK, `{"access_token":"fixture-bearer","token_type":"Bearer","expires_in":3600}`), nil
	})}
	source, err := ServiceAccountTokenSource(fixtureCredentialsJSON(t, credentials), client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if token, err := source(ctx); token != "" || !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("canceled context must not exchange credentials")
	}
	for range 2 {
		if token, err := source(context.Background()); token != "fixture-bearer" || err != nil {
			t.Fatalf("token exchange failed: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("cache made %d exchanges, want 1", calls)
	}
	if token, err := source(ctx); token != "" || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("canceled context must not return cached credentials")
	}
}

func TestServiceAccountTokenSourceRejectsCredentials(t *testing.T) {
	credentials, _ := serviceAccountFixture(t)
	_, nonRSA, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	weakRSA, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string]map[string]string{
		"wrong type":         {"type": "authorized_user"},
		"arbitrary endpoint": {"token_uri": "https://fixture.invalid/steal"},
		"http endpoint":      {"token_uri": "http://oauth2.googleapis.com/token"},
		"invalid key":        {"private_key": "fixture-secret-invalid-key"},
		"non RSA":            {"private_key": fixturePrivateKey(t, nonRSA)},
		"weak RSA":           {"private_key": fixturePrivateKey(t, weakRSA)},
		"invalid email":      {"client_email": "svc@fixture.invalid"},
		"oversize JSON":      {"padding": strings.Repeat("x", 64<<10)},
	} {
		t.Run(name, func(t *testing.T) {
			copy := make(map[string]string, len(credentials))
			for k, v := range credentials {
				copy[k] = v
			}
			for k, v := range mutation {
				copy[k] = v
			}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid credentials must not send a request")
				return nil, errors.New("unexpected request")
			})}
			source, err := ServiceAccountTokenSource(fixtureCredentialsJSON(t, copy), client)
			assertCredentialError(t, err, copy["private_key"], "fixture-secret-invalid-key", "fixture.invalid")
			if source != nil {
				t.Fatal("invalid credentials returned a token source")
			}
		})
	}
}

func TestServiceAccountTokenSourceRejectsTokenResponses(t *testing.T) {
	credentials, _ := serviceAccountFixture(t)
	data := fixtureCredentialsJSON(t, credentials)
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"unauthorized":        {401, `{"error":"fixture-response-secret"}`},
		"rate limited":        {429, `{"error":"fixture-response-secret"}`},
		"malformed":           {200, `{"access_token":"fixture-response-secret"`},
		"oversize":            {200, strings.Repeat("x", (128<<10)+1)},
		"expired":             {200, `{"access_token":"fixture-response-secret","token_type":"Bearer","expires_in":0}`},
		"negative expiry":     {200, `{"access_token":"fixture-response-secret","token_type":"Bearer","expires_in":-1}`},
		"too short expiry":    {200, `{"access_token":"fixture-response-secret","token_type":"Bearer","expires_in":60}`},
		"too long expiry":     {200, `{"access_token":"fixture-response-secret","token_type":"Bearer","expires_in":3601}`},
		"missing expiry":      {200, `{"access_token":"fixture-response-secret","token_type":"Bearer"}`},
		"invalid expiry type": {200, `{"access_token":"fixture-response-secret","token_type":"Bearer","expires_in":"3600"}`},
		"wrong token type":    {200, `{"access_token":"fixture-response-secret","token_type":"Basic","expires_in":3600}`},
		"empty token":         {200, `{"access_token":"","token_type":"Bearer","expires_in":3600}`},
		"unsafe token":        {200, `{"access_token":"fixture-response-secret\n","token_type":"Bearer","expires_in":3600}`},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			source, err := ServiceAccountTokenSource(data, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != fixtureTokenURL {
					t.Fatal("unexpected outbound URL")
				}
				return response(tc.status, tc.body), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				token, err := source(context.Background())
				assertCredentialError(t, err, credentials["private_key"], "fixture-response-secret")
				if token != "" {
					t.Fatal("invalid response returned a token")
				}
			}
			if calls != 2 {
				t.Fatal("failed exchange must not be cached")
			}
		})
	}
}

type credentialFixtureJar struct{ calls int }

func (j *credentialFixtureJar) SetCookies(*url.URL, []*http.Cookie) { j.calls++ }
func (j *credentialFixtureJar) Cookies(*url.URL) []*http.Cookie {
	j.calls++
	return []*http.Cookie{{Name: "fixture-cookie", Value: "fixture-cookie-secret"}}
}

func TestServiceAccountTokenSourceIsolatesClientAndDeniesRedirect(t *testing.T) {
	credentials, _ := serviceAccountFixture(t)
	for _, timeout := range []time.Duration{0, time.Hour, 3 * time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			jar := &credentialFixtureJar{}
			redirectCalls, transportCalls := 0, 0
			redirect := func(*http.Request, []*http.Request) error { redirectCalls++; return nil }
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				transportCalls++
				if r.URL.String() != fixtureTokenURL {
					t.Fatal("redirect leaked assertion to arbitrary endpoint")
				}
				if r.Header.Get("Cookie") != "" {
					t.Fatal("caller cookies leaked to token endpoint")
				}
				deadline, ok := r.Context().Deadline()
				remaining := time.Until(deadline)
				bound := 20 * time.Second
				if timeout > 0 && timeout < bound {
					bound = timeout
				}
				if !ok || remaining <= 0 || remaining > bound {
					t.Fatal("token request has no bounded client timeout")
				}
				res := response(http.StatusTemporaryRedirect, "fixture-response-secret")
				res.Header.Set("Location", "https://fixture.invalid/steal")
				res.Header.Set("Set-Cookie", "fixture-cookie=server-secret")
				return res, nil
			})
			client := &http.Client{Transport: transport, Timeout: timeout, Jar: jar, CheckRedirect: redirect}
			source, err := ServiceAccountTokenSource(fixtureCredentialsJSON(t, credentials), client)
			if err != nil {
				t.Fatal(err)
			}
			token, err := source(context.Background())
			assertCredentialError(t, err, credentials["private_key"], "fixture-response-secret", "fixture-cookie-secret", "fixture.invalid")
			if token != "" || transportCalls != 1 || redirectCalls != 0 || jar.calls != 0 {
				t.Fatal("redirect or cookie isolation failed")
			}
			if client.Timeout != timeout || client.Jar != jar || reflect.ValueOf(client.Transport).Pointer() != reflect.ValueOf(transport).Pointer() || reflect.ValueOf(client.CheckRedirect).Pointer() != reflect.ValueOf(redirect).Pointer() {
				t.Fatal("caller HTTP client was mutated")
			}
		})
	}
}

func TestServiceAccountTokenSourceRedactsTransportError(t *testing.T) {
	credentials, _ := serviceAccountFixture(t)
	var assertion string
	source, err := ServiceAccountTokenSource(fixtureCredentialsJSON(t, credentials), &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		assertion = form.Get("assertion")
		return nil, errors.New("fixture-transport-secret " + assertion + " " + credentials["private_key"])
	})})
	if err != nil {
		t.Fatal(err)
	}
	token, err := source(context.Background())
	assertCredentialError(t, err, assertion, credentials["private_key"], "fixture-transport-secret")
	if token != "" {
		t.Fatal("failed transport returned a token")
	}
}
