package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/app"
	metricclickhouse "github.com/linkasu/linka.plays-metric/internal/clickhouse"
	"github.com/linkasu/linka.plays-metric/internal/privacy"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("privacy worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	password, err := app.Env("CLICKHOUSE_PASSWORD")
	if err != nil {
		return err
	}
	retentionConfig, err := app.LoadRetention()
	if err != nil {
		return err
	}
	store, err := metricclickhouse.Open(metricclickhouse.Config{
		Addresses: strings.Split(envDefault("CLICKHOUSE_ADDRS", "clickhouse:9000"), ","),
		Database:  envDefault("CLICKHOUSE_DATABASE", "linka_metric"), Username: envDefault("CLICKHOUSE_USER", "metric_privacy"),
		Password: password, Secure: os.Getenv("CLICKHOUSE_SECURE") == "true", Retention: retentionConfig,
	})
	if err != nil {
		return err
	}
	defer store.Close()
	maxAttempts := 10
	maxAttempts, err = strconv.Atoi(envDefault("PRIVACY_WORKER_MAX_ATTEMPTS", "10"))
	if err != nil {
		return fmt.Errorf("PRIVACY_WORKER_MAX_ATTEMPTS must be an integer")
	}
	worker, err := privacy.NewWorkerWithMaxAttempts(store, store, 100, maxAttempts)
	if err != nil {
		return err
	}
	interval, err := time.ParseDuration(envDefault("PRIVACY_WORKER_INTERVAL", "30s"))
	if err != nil || interval < time.Second {
		return fmt.Errorf("PRIVACY_WORKER_INTERVAL must be at least one second")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := worker.RunOnce(ctx); err != nil && ctx.Err() == nil {
			logger.Error("privacy worker iteration failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func envDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
