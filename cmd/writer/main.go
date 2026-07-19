package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/app"
	"github.com/linkasu/linka.plays-metric/internal/auth"
	metricclickhouse "github.com/linkasu/linka.plays-metric/internal/clickhouse"
	"github.com/linkasu/linka.plays-metric/internal/writer"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
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
	password, err := app.Env("CLICKHOUSE_PASSWORD")
	if err != nil {
		return err
	}
	addresses := os.Getenv("CLICKHOUSE_ADDRS")
	if addresses == "" {
		addresses = "clickhouse:9000"
	}
	username := os.Getenv("CLICKHOUSE_USER")
	if username == "" {
		username = "metric_writer"
	}
	database := os.Getenv("CLICKHOUSE_DATABASE")
	if database == "" {
		database = "linka_metric"
	}
	retentionConfig, err := loadRetention()
	if err != nil {
		return err
	}
	store, err := metricclickhouse.Open(metricclickhouse.Config{
		Addresses: strings.Split(addresses, ","),
		Database:  database,
		Username:  username,
		Password:  password,
		Secure:    os.Getenv("CLICKHOUSE_SECURE") == "true",
		Retention: retentionConfig,
	})
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
	return app.Serve(address, writer.NewServerWithV2(store, store, writerSecret, serviceVerifier, logger, 5*time.Minute), logger)
}

func loadRetention() (metricclickhouse.Retention, error) {
	var result metricclickhouse.Retention
	values := []struct {
		name        string
		destination *time.Duration
	}{
		{"RETENTION_V2_INGEST_BATCHES", &result.IngestBatches},
		{"RETENTION_V2_COMMON", &result.Common},
		{"RETENTION_V2_TECHNICAL", &result.Technical},
		{"RETENTION_V2_PLAYS", &result.Plays},
		{"RETENTION_V2_PRODUCT", &result.Product},
		{"RETENTION_V2_PRIVACY", &result.Privacy},
	}
	for _, value := range values {
		duration, err := retention(value.name)
		if err != nil {
			return metricclickhouse.Retention{}, err
		}
		*value.destination = duration
	}
	return result, nil
}

func retention(name string) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", name)
	}
	return duration, nil
}
