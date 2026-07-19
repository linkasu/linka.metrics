package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/linkasu/linka.plays-metric/internal/app"
	metricclickhouse "github.com/linkasu/linka.plays-metric/internal/clickhouse"
	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
	"github.com/linkasu/linka.plays-metric/internal/product"
)

const maxImportBatch = 500

var (
	importNamespace = uuid.MustParse("8a925f88-1ca2-4e34-a2af-745a3cc2e01f")
	safeValue       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]{0,95}$`)
	safeSourceID    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]{0,127}$`)
	sourceProducts  = map[string]product.ID{
		"looks-sqlite":      product.LinkaLooks,
		"firebase-pictures": product.LinkaPictures,
		"firebase-type":     product.LinkaType,
		"yandex-metrika":    product.LinkaSite,
		"tts-postgres":      product.LinkaTTS,
	}
)

type sourceEvent struct {
	Source         string     `json:"source"`
	SourceRecordID string     `json:"source_record_id"`
	SourceSubject  string     `json:"source_subject"`
	Product        product.ID `json:"product"`
	OccurredAt     string     `json:"occurred_at"`
	Kind           string     `json:"kind"`
	AppVersion     string     `json:"app_version"`
	AppBuild       string     `json:"app_build,omitempty"`
	Platform       string     `json:"platform"`
	OSVersion      string     `json:"os_version,omitempty"`
	Locale         string     `json:"locale,omitempty"`
	occurredAtTime time.Time
}

type importer struct {
	store      *metricclickhouse.Store
	secret     []byte
	dryRun     bool
	events     uint64
	batches    uint64
	replayed   uint64
	currentKey string
	current    []sourceEvent
	closedKeys map[string]struct{}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(); err != nil {
		logger.Error("legacy import failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	inputPath := flag.String("input", "-", "strict NDJSON input path, or - for stdin")
	dryRun := flag.Bool("dry-run", false, "validate and hash input without writing ClickHouse")
	flag.Parse()
	secret, err := app.Secret("IMPORT_SUBJECT_HMAC_SECRET")
	if err != nil {
		return err
	}
	input, closeInput, err := openInput(*inputPath)
	if err != nil {
		return err
	}
	defer closeInput()

	var store *metricclickhouse.Store
	if !*dryRun {
		store, err = openStore()
		if err != nil {
			return err
		}
		defer store.Close()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	worker := &importer{store: store, secret: secret, dryRun: *dryRun, current: make([]sourceEvent, 0, maxImportBatch), closedKeys: make(map[string]struct{})}
	if err := worker.consume(ctx, input); err != nil {
		return err
	}
	fmt.Printf("validated_events=%d batches=%d replayed_batches=%d dry_run=%t\n", worker.events, worker.batches, worker.replayed, worker.dryRun)
	return nil
}

func openInput(path string) (io.Reader, func(), error) {
	if path == "-" {
		return os.Stdin, func() {}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open input: %w", err)
	}
	return file, func() { _ = file.Close() }, nil
}

func openStore() (*metricclickhouse.Store, error) {
	password, err := app.Env("CLICKHOUSE_PASSWORD")
	if err != nil {
		return nil, err
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
	store, err := metricclickhouse.Open(metricclickhouse.Config{
		Addresses: strings.Split(addresses, ","), Database: database, Username: username, Password: password,
		Secure: os.Getenv("CLICKHOUSE_SECURE") == "true",
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func (i *importer) consume(ctx context.Context, input io.Reader) error {
	if i.closedKeys == nil {
		i.closedKeys = make(map[string]struct{})
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if err := ctx.Err(); err != nil {
			return err
		}
		event, err := decodeEvent(scanner.Bytes())
		if err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		key := event.Source + "\x00" + string(event.Product) + "\x00" + i.subjectKey(event.Product, event.Source, event.SourceSubject)
		if i.currentKey != "" && (key != i.currentKey || len(i.current) == maxImportBatch) {
			previousKey := i.currentKey
			if err := i.flush(ctx); err != nil {
				return fmt.Errorf("flush before line %d: %w", line, err)
			}
			if key != previousKey {
				i.closedKeys[previousKey] = struct{}{}
			}
		}
		if _, closed := i.closedKeys[key]; closed {
			return fmt.Errorf("line %d: input must be grouped by source_subject", line)
		}
		i.currentKey = key
		i.current = append(i.current, event)
		i.events++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan input: %w", err)
	}
	return i.flush(ctx)
}

func decodeEvent(data []byte) (sourceEvent, error) {
	var event sourceEvent
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return sourceEvent{}, fmt.Errorf("decode JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return sourceEvent{}, errors.New("unexpected data after JSON object")
	}
	expectedProduct, ok := sourceProducts[event.Source]
	if !ok || expectedProduct != event.Product {
		return sourceEvent{}, errors.New("source is not registered for product")
	}
	if !safeSourceID.MatchString(event.SourceRecordID) || len(event.SourceSubject) < 1 || len(event.SourceSubject) > 256 {
		return sourceEvent{}, errors.New("invalid source identifiers")
	}
	spec, ok := product.Lookup(event.Product)
	if !ok || !spec.AllowsStream(product.StreamProduct) || !spec.AllowsProductKind(event.Kind) {
		return sourceEvent{}, errors.New("event kind is not registered for product")
	}
	for name, value := range map[string]string{"app_version": event.AppVersion, "platform": event.Platform} {
		if !safeValue.MatchString(value) {
			return sourceEvent{}, fmt.Errorf("%s is unsafe or empty", name)
		}
	}
	for name, value := range map[string]*string{"app_build": &event.AppBuild, "os_version": &event.OSVersion, "locale": &event.Locale} {
		if *value == "" {
			*value = "unknown"
			if name == "app_build" {
				*value = "import-" + event.Source
			}
		}
		if !safeValue.MatchString(*value) {
			return sourceEvent{}, fmt.Errorf("%s is unsafe", name)
		}
	}
	parsed, err := time.Parse(time.RFC3339Nano, event.OccurredAt)
	if err != nil || parsed.Before(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) || !parsed.Before(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)) {
		return sourceEvent{}, errors.New("occurred_at is outside the supported RFC3339 range")
	}
	event.occurredAtTime = parsed.UTC().Truncate(time.Millisecond)
	event.OccurredAt = event.occurredAtTime.Format("2006-01-02T15:04:05.000Z07:00")
	return event, nil
}

func (i *importer) flush(ctx context.Context) error {
	if len(i.current) == 0 {
		return nil
	}
	validated, body, err := i.buildBatch(i.current)
	if err != nil {
		return err
	}
	i.batches++
	if !i.dryRun {
		result, err := i.store.InsertV2(ctx, validated, v2.BodySHA256(body))
		if err != nil {
			return err
		}
		if result.Replayed {
			i.replayed++
		}
	}
	clear(i.current)
	i.current = i.current[:0]
	i.currentKey = ""
	return nil
}

func (i *importer) buildBatch(events []sourceEvent) (v2.ValidatedBatch, []byte, error) {
	if len(events) == 0 {
		return v2.ValidatedBatch{}, nil, errors.New("cannot build an empty import batch")
	}
	first := events[0]
	spec, _ := product.Lookup(first.Product)
	subjectKey := i.subjectKey(first.Product, first.Source, first.SourceSubject)
	records := make([]v2.ProductRecord, 0, len(events))
	validatedRecords := make([]v2.ValidatedProductRecord, 0, len(events))
	sentAt := first.occurredAtTime
	for _, event := range events {
		if event.Source != first.Source || event.Product != first.Product || event.SourceSubject != first.SourceSubject {
			return v2.ValidatedBatch{}, nil, errors.New("batch contains multiple legacy subjects")
		}
		if event.occurredAtTime.After(sentAt) {
			sentAt = event.occurredAtTime
		}
		record := v2.ProductRecord{
			RecordID:     deterministicID("record", event.Source, string(event.Product), event.SourceRecordID),
			OccurredAt:   event.OccurredAt,
			Kind:         event.Kind,
			AppSessionID: deterministicID("session", event.Source, string(event.Product), event.SourceSubject, event.occurredAtTime.Format("2006-01-02")),
			App:          v2.AppMetadata{Version: event.AppVersion, Build: event.AppBuild, Platform: event.Platform, OSVersion: event.OSVersion, Locale: event.Locale},
		}
		records = append(records, record)
		validatedRecords = append(validatedRecords, v2.ValidatedProductRecord{ProductRecord: record, OccurredAtTime: event.occurredAtTime})
	}
	batchID := deterministicID("batch", first.Source, string(first.Product), subjectKey, records[0].RecordID, records[len(records)-1].RecordID)
	header := v2.BatchHeader{
		SchemaVersion: v2.SchemaVersion,
		BatchID:       batchID,
		Scope:         v2.Scope{Product: first.Product, SubjectKey: subjectKey},
		Stream:        product.StreamProduct,
		SentAt:        sentAt.Format("2006-01-02T15:04:05.000Z07:00"),
	}
	body, err := json.Marshal(v2.ProductBatch{BatchHeader: header, Records: records})
	if err != nil {
		return v2.ValidatedBatch{}, nil, err
	}
	return v2.ValidatedBatch{Header: header, SentAtTime: sentAt, ProductKey: spec.OpaqueKey, ProductRecords: validatedRecords}, body, nil
}

func (i *importer) subjectKey(productID product.ID, source, subject string) string {
	digest := hmac.New(sha256.New, i.secret)
	_, _ = io.WriteString(digest, string(productID))
	_, _ = digest.Write([]byte{0})
	_, _ = io.WriteString(digest, source)
	_, _ = digest.Write([]byte{0})
	_, _ = io.WriteString(digest, subject)
	return hex.EncodeToString(digest.Sum(nil))
}

func deterministicID(parts ...string) string {
	return uuid.NewSHA1(importNamespace, []byte(strings.Join(parts, "\x00"))).String()
}
