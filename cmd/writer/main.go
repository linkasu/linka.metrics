package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/analyticsapi"
	"github.com/linkasu/linka.plays-metric/internal/app"
	"github.com/linkasu/linka.plays-metric/internal/auth"
	"github.com/linkasu/linka.plays-metric/internal/writer"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) > 1 && os.Args[1] == "analytics-sync" {
		if err := runAnalyticsSync(logger, os.Args[2:]); err != nil {
			logger.Error("analytics sync stopped", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(logger); err != nil {
		logger.Error("writer stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	writerSecret, err := app.Secret("WRITER_HMAC_SECRET")
	if err != nil {
		return err
	}
	serviceKey, previousServiceKey, err := app.HMACKeyring("SERVICE_HMAC", writerSecret)
	if err != nil {
		return err
	}
	serviceVerifier, err := auth.NewServiceVerifier(serviceKey, previousServiceKey, "collector", 5*time.Minute)
	if err != nil {
		return err
	}
	retentionConfig, err := app.LoadRetention()
	if err != nil {
		return err
	}
	store, err := openMetricStore(retentionConfig)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		return err
	}
	address := os.Getenv("LISTEN_ADDR")
	if address == "" {
		address = ":8081"
	}
	handler := writer.NewServerWithV2AndFundraising(store, store, store, writerSecret, serviceVerifier, logger, 5*time.Minute)
	if os.Getenv("ANALYTICS_HMAC_ACTIVE_SECRET") != "" || os.Getenv("ANALYTICS_HMAC_ACTIVE_KEY_ID") != "" || os.Getenv("ANALYTICS_HMAC_PREVIOUS_SECRET") != "" || os.Getenv("ANALYTICS_HMAC_PREVIOUS_KEY_ID") != "" {
		if os.Getenv("ANALYTICS_HMAC_ACTIVE_KEY_ID") == "" || os.Getenv("ANALYTICS_HMAC_ACTIVE_SECRET") == "" {
			return errors.New("partial analytics HMAC configuration")
		}
		key, previous, err := app.HMACKeyring("ANALYTICS_HMAC", nil)
		if err != nil {
			return err
		}
		verifier, err := auth.NewServiceVerifier(key, previous, "crm-analytics", 5*time.Minute)
		if err != nil {
			return err
		}
		mux := http.NewServeMux()
		mux.Handle("/internal/analytics/", analyticsapi.NewHandler(store, verifier, logger))
		mux.Handle("/", handler)
		handler = mux
	}
	return app.Serve(address, handler, logger)
}
