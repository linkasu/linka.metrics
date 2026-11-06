package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // Alpine runtime has no host zoneinfo; GA4 windows require source timezones.

	"github.com/google/uuid"
	"github.com/linkasu/linka.plays-metric/internal/analyticsapi"
	"github.com/linkasu/linka.plays-metric/internal/analyticsbuild"
	"github.com/linkasu/linka.plays-metric/internal/app"
	metricclickhouse "github.com/linkasu/linka.plays-metric/internal/clickhouse"
	"github.com/linkasu/linka.plays-metric/internal/ga4"
)

func openMetricStore(retention metricclickhouse.Retention) (*metricclickhouse.Store, error) {
	password, err := app.Env("CLICKHOUSE_PASSWORD")
	if err != nil {
		return nil, err
	}
	get := func(key, fallback string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return fallback
	}
	return metricclickhouse.Open(metricclickhouse.Config{Addresses: strings.Split(get("CLICKHOUSE_ADDRS", "clickhouse:9000"), ","), Database: get("CLICKHOUSE_DATABASE", "linka_metric"), Username: get("CLICKHOUSE_USER", "metric_writer"), Password: password, Secure: os.Getenv("CLICKHOUSE_SECURE") == "true", Retention: retention})
}

func runAnalyticsSync(logger *slog.Logger, args []string) error {
	flags := flag.NewFlagSet("analytics-sync", flag.ContinueOnError)
	once := flags.Bool("once", false, "prepare queued reports and exit")
	backfillFrom := flags.Int("backfill-from-year", 0, "enqueue complete annual reports from this year")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("invalid analytics-sync arguments")
	}
	if *backfillFrom != 0 && (*backfillFrom < 2000 || *backfillFrom > time.Now().UTC().Year()) {
		return errors.New("invalid analytics backfill year")
	}
	lockPath := os.Getenv("ANALYTICS_LOCK_PATH")
	if lockPath == "" {
		lockPath = filepath.Join(os.TempDir(), "linka-metrics-analytics", "executor.lock")
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return errors.New("analytics lock directory unavailable")
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return errors.New("analytics executor lock unavailable")
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another analytics executor owns the lock")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	store, err := openMetricStore(metricclickhouse.Retention{})
	if err != nil {
		return err
	}
	defer store.Close()
	credentials := os.Getenv("GA4_SERVICE_ACCOUNT_JSON_BASE64")
	if credentials == "" || len(credentials) > 90<<10 {
		return errors.New("GA4_SERVICE_ACCOUNT_JSON_BASE64 is required for analytics sync")
	}
	data, err := base64.StdEncoding.DecodeString(credentials)
	if err != nil {
		return errors.New("invalid encoded GA4 service account")
	}
	token, err := ga4.ServiceAccountTokenSource(data, nil)
	if err != nil {
		return err
	}
	computer := analyticsbuild.CombinedComputer{Computers: []analyticsapi.Computer{store, analyticsbuild.NewGA4Computer(ga4.NewClient(token, nil), analyticsbuild.DefaultProperties())}}
	executor := analyticsbuild.Executor{Backend: store, Computer: computer}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *backfillFrom != 0 {
		for year := *backfillFrom; year < time.Now().UTC().Year(); year++ {
			query := analyticsapi.DefaultQuery(time.Now())
			query.SeriesFrom, query.SeriesTo, query.CutoffDate = time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
			if _, err := store.AnalyticsEnqueue(ctx, query, uuid.NewString()); err != nil {
				return err
			}
		}
		current := analyticsapi.DefaultQuery(time.Now())
		current.SeriesFrom = time.Date(time.Now().UTC().Year(), 1, 1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
		if current.SeriesFrom <= current.SeriesTo {
			if _, err := store.AnalyticsEnqueue(ctx, current, uuid.NewString()); err != nil {
				return err
			}
		}
	}
	seed := func() error {
		query := analyticsapi.DefaultQuery(time.Now())
		_, err := store.AnalyticsReport(ctx, query)
		if err == nil {
			return nil
		}
		if !errors.Is(err, analyticsapi.ErrNotFound) && !errors.Is(err, analyticsapi.ErrInvalidated) {
			return err
		}
		_, err = store.AnalyticsEnqueue(ctx, query, uuid.NewString())
		return err
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := seed(); err != nil {
			return err
		}
		for {
			runContext, cancel := context.WithTimeout(ctx, 15*time.Minute)
			worked, err := executor.ProcessOne(runContext)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				logger.Warn("analytics build failed", "reason", "executor_error")
				if *once {
					return errors.New("analytics build failed")
				}
				break
			}
			if !worked {
				break
			}
		}
		if *once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
