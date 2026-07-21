package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/app"
	"github.com/linkasu/linka.plays-metric/internal/auth"
	"github.com/linkasu/linka.plays-metric/internal/collector"
	"github.com/linkasu/linka.plays-metric/internal/httpx"
	"github.com/linkasu/linka.plays-metric/internal/product"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("collector stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	installationSecret := []byte(os.Getenv("INSTALLATION_HMAC_ACTIVE_SECRET"))
	if len(installationSecret) == 0 {
		var err error
		installationSecret, err = app.Secret("INSTALLATION_HMAC_SECRET")
		if err != nil {
			return err
		}
	} else if len(installationSecret) < 32 {
		return fmt.Errorf("INSTALLATION_HMAC_ACTIVE_SECRET must contain at least 32 bytes")
	}
	writerSecret, err := app.Secret("WRITER_HMAC_SECRET")
	if err != nil {
		return err
	}
	writerURL, err := app.Env("WRITER_URL")
	if err != nil {
		return err
	}
	installationMaxAge := 30 * 24 * time.Hour
	if value := os.Getenv("INSTALLATION_TOKEN_MAX_AGE"); value != "" {
		installationMaxAge, err = time.ParseDuration(value)
		if err != nil {
			return err
		}
	}
	installationKey, previousInstallationKey, err := app.HMACKeyring("INSTALLATION_HMAC", installationSecret)
	if err != nil {
		return err
	}
	var previousInstallationSecret []byte
	if previousInstallationKey != nil {
		previousInstallationSecret = previousInstallationKey.Secret
	}
	tokens, err := auth.NewInstallationTokensWithKeyring(installationKey.Secret, previousInstallationSecret, installationMaxAge)
	if err != nil {
		return err
	}
	serviceKey, _, err := app.HMACKeyring("SERVICE_HMAC", writerSecret)
	if err != nil {
		return err
	}
	var donationVerifier *auth.ServiceVerifier
	if donationSecret := os.Getenv("DONATION_INGEST_HMAC_ACTIVE_SECRET"); donationSecret != "" {
		donationKey, previousDonationKey, err := app.HMACKeyring("DONATION_INGEST_HMAC", []byte(donationSecret))
		if err != nil {
			return err
		}
		donationVerifier, err = auth.NewServiceVerifier(donationKey, previousDonationKey, "nko-donations", 5*time.Minute)
		if err != nil {
			return err
		}
	}
	ttsOutcomeVerifier, ttsOutcomeSubjectKey, err := configureTTSOutcomeIngress()
	if err != nil {
		return err
	}
	deploymentEnvironment := os.Getenv("DEPLOYMENT_ENVIRONMENT")
	if deploymentEnvironment == "" {
		deploymentEnvironment = "production"
	}
	if deploymentEnvironment != "production" && deploymentEnvironment != "staging" {
		return fmt.Errorf("DEPLOYMENT_ENVIRONMENT must be production or staging")
	}
	legacyEnabled := false
	switch os.Getenv("ALLOW_LEGACY_PRODUCT_TOKENS") {
	case "", "false":
	case "true":
		legacyEnabled = true
	default:
		return fmt.Errorf("ALLOW_LEGACY_PRODUCT_TOKENS must be true or false")
	}
	if legacyEnabled && deploymentEnvironment != "staging" {
		return fmt.Errorf("ALLOW_LEGACY_PRODUCT_TOKENS is permitted only in staging")
	}
	var productTokens *auth.ProductTokens
	if legacyEnabled {
		productKey, previousProductKey, err := app.HMACKeyring("PRODUCT_TOKEN_HMAC", installationSecret)
		if err != nil {
			return err
		}
		productTokenTTL := 24 * time.Hour
		if value := os.Getenv("PRODUCT_TOKEN_TTL"); value != "" {
			productTokenTTL, err = time.ParseDuration(value)
			if err != nil {
				return err
			}
		}
		subjectSecret := installationSecret
		if value := os.Getenv("SUBJECT_KEY_HMAC_SECRET"); value != "" {
			subjectSecret = []byte(value)
		}
		productTokens, err = auth.NewProductTokensWithSubjectSecret(productKey, previousProductKey, subjectSecret, productTokenTTL)
		if err != nil {
			return err
		}
	}
	var identityVerifier *auth.IdentityJWTVerifier
	if !legacyEnabled || os.Getenv("IDENTITY_JWKS_URL") != "" {
		jwksURL, err := app.Env("IDENTITY_JWKS_URL")
		if err != nil {
			return err
		}
		issuer, err := app.Env("IDENTITY_TOKEN_ISSUER")
		if err != nil {
			return err
		}
		audiences, err := identityAudiences()
		if err != nil {
			return err
		}
		maxLifetime := 15 * time.Minute
		if value := os.Getenv("IDENTITY_TOKEN_MAX_LIFETIME"); value != "" {
			maxLifetime, err = time.ParseDuration(value)
			if err != nil {
				return err
			}
		}
		identityVerifier, err = auth.NewIdentityJWTVerifier(jwksURL, issuer, audiences, maxLifetime, deploymentEnvironment == "staging")
		if err != nil {
			return err
		}
	}
	writerClient, err := collector.NewHTTPWriterWithServiceKey(writerURL, writerSecret, serviceKey, 10*time.Second)
	if err != nil {
		return err
	}
	address := os.Getenv("LISTEN_ADDR")
	if address == "" {
		address = ":8080"
	}
	handler := collector.NewServerWithIdentityV2AndFundraisingAndTTSOutcome(writerClient, writerClient, writerClient, tokens, identityVerifier, productTokens, legacyEnabled, donationVerifier, ttsOutcomeVerifier, ttsOutcomeSubjectKey, logger)
	origins, err := corsOrigins(deploymentEnvironment)
	if err != nil {
		return err
	}
	return app.Serve(address, httpx.CORS(origins)(handler), logger)
}

var opaqueSubjectKeyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func configureTTSOutcomeIngress() (*auth.ServiceVerifier, string, error) {
	secret := os.Getenv("TTS_OUTCOME_HMAC_ACTIVE_SECRET")
	if secret == "" {
		return nil, "", nil
	}
	subjectKey := os.Getenv("TTS_OUTCOME_SUBJECT_KEY")
	if !opaqueSubjectKeyPattern.MatchString(subjectKey) {
		return nil, "", errors.New("TTS_OUTCOME_SUBJECT_KEY must be a lowercase 64-character hexadecimal key")
	}
	key, previousKey, err := app.HMACKeyring("TTS_OUTCOME_HMAC", []byte(secret))
	if err != nil {
		return nil, "", err
	}
	verifier, err := auth.NewServiceVerifier(key, previousKey, "tts-echo", 5*time.Minute)
	if err != nil {
		return nil, "", err
	}
	return verifier, subjectKey, nil
}

func identityAudiences() (map[product.ID]string, error) {
	value := os.Getenv("IDENTITY_TELEMETRY_AUDIENCES_JSON")
	if value == "" {
		encoded := os.Getenv("IDENTITY_TELEMETRY_AUDIENCES_BASE64")
		if encoded != "" {
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, errors.New("IDENTITY_TELEMETRY_AUDIENCES_BASE64 is invalid")
			}
			value = string(decoded)
		}
	}
	if value == "" {
		legacy, err := app.Env("IDENTITY_TELEMETRY_AUDIENCE")
		if err != nil {
			return nil, errors.New("IDENTITY_TELEMETRY_AUDIENCES_JSON is required")
		}
		return map[product.ID]string{product.LinkaPlays: legacy}, nil
	}
	var raw map[string]string
	if err := json.Unmarshal([]byte(value), &raw); err != nil || len(raw) == 0 {
		return nil, errors.New("IDENTITY_TELEMETRY_AUDIENCES_JSON must be a non-empty JSON object")
	}
	result := make(map[product.ID]string, len(raw))
	for id, audience := range raw {
		productID := product.ID(id)
		if _, ok := product.Lookup(productID); !ok || strings.TrimSpace(audience) == "" {
			return nil, errors.New("IDENTITY_TELEMETRY_AUDIENCES_JSON contains an invalid product or audience")
		}
		result[productID] = audience
	}
	return result, nil
}

func corsOrigins(environment string) ([]string, error) {
	value := os.Getenv("CORS_ALLOWED_ORIGINS")
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ";")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		parsed, err := url.Parse(origin)
		loopback := parsed != nil && parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost" || parsed.Hostname() == "::1")
		if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" ||
			(parsed.Scheme != "https" && !(environment == "staging" && loopback)) {
			return nil, errors.New("CORS_ALLOWED_ORIGINS must contain exact HTTPS origins")
		}
		if _, duplicate := seen[origin]; duplicate {
			continue
		}
		seen[origin] = struct{}{}
		result = append(result, origin)
	}
	return result, nil
}
