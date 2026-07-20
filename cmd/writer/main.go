package main

import (
	"context"
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
	retentionConfig, err := app.LoadRetention()
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
