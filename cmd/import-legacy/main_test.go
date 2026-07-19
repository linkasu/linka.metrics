package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/product"
)

func TestDecodeEventRejectsPIIAndCrossProductKinds(t *testing.T) {
	valid := `{"source":"looks-sqlite","source_record_id":"42","source_subject":"00000000-0000-4000-8000-000000000000","product":"linka-looks","occurred_at":"2026-07-18T12:00:00Z","kind":"start","app_version":"3.2.8","platform":"windows"}`
	if _, err := decodeEvent([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		strings.Replace(valid, `"kind":"start"`, `"kind":"set_import"`, 1),
		strings.Replace(valid, `"kind":"start"`, `"kind":"start","email":"private@example.test"`, 1),
		strings.Replace(valid, `"product":"linka-looks"`, `"product":"linka-pictures"`, 1),
	} {
		if _, err := decodeEvent([]byte(input)); err == nil {
			t.Fatal("unsafe import event was accepted")
		}
	}
}

func TestImporterBuildsDeterministicPseudonymousBatch(t *testing.T) {
	event, err := decodeEvent([]byte(`{"source":"looks-sqlite","source_record_id":"42","source_subject":"legacy-pc","product":"linka-looks","occurred_at":"2026-07-18T12:00:00Z","kind":"start","app_version":"3.2.8","platform":"windows"}`))
	if err != nil {
		t.Fatal(err)
	}
	worker := &importer{secret: []byte(strings.Repeat("s", 32)), dryRun: true}
	first, body1, err := worker.buildBatch([]sourceEvent{event})
	if err != nil {
		t.Fatal(err)
	}
	second, body2, err := worker.buildBatch([]sourceEvent{event})
	if err != nil {
		t.Fatal(err)
	}
	if first.Header.BatchID != second.Header.BatchID || first.Header.Scope.SubjectKey != second.Header.Scope.SubjectKey || string(body1) != string(body2) {
		t.Fatal("legacy import identifiers are not deterministic")
	}
	if first.Header.Scope.SubjectKey == event.SourceSubject || len(first.Header.Scope.SubjectKey) != 64 || first.Header.Scope.Product != product.LinkaLooks {
		t.Fatal("legacy subject was not pseudonymized")
	}
}

func TestImporterRequiresGroupedInputAndFlushesAtBoundary(t *testing.T) {
	input := strings.Join([]string{
		`{"source":"looks-sqlite","source_record_id":"1","source_subject":"a","product":"linka-looks","occurred_at":"2026-07-18T12:00:00Z","kind":"start","app_version":"3.2.8","platform":"windows"}`,
		`{"source":"looks-sqlite","source_record_id":"2","source_subject":"b","product":"linka-looks","occurred_at":"2026-07-18T12:01:00Z","kind":"start","app_version":"3.2.8","platform":"windows"}`,
	}, "\n")
	worker := &importer{secret: []byte(strings.Repeat("s", 32)), dryRun: true, current: make([]sourceEvent, 0, maxImportBatch)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.consume(ctx, strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if worker.events != 2 || worker.batches != 2 {
		t.Fatalf("events=%d batches=%d", worker.events, worker.batches)
	}
}

func TestImporterRejectsUngroupedSubjects(t *testing.T) {
	input := strings.Join([]string{
		`{"source":"looks-sqlite","source_record_id":"1","source_subject":"a","product":"linka-looks","occurred_at":"2026-07-18T12:00:00Z","kind":"start","app_version":"3.2.8","platform":"windows"}`,
		`{"source":"looks-sqlite","source_record_id":"2","source_subject":"b","product":"linka-looks","occurred_at":"2026-07-18T12:01:00Z","kind":"start","app_version":"3.2.8","platform":"windows"}`,
		`{"source":"looks-sqlite","source_record_id":"3","source_subject":"a","product":"linka-looks","occurred_at":"2026-07-18T12:02:00Z","kind":"start","app_version":"3.2.8","platform":"windows"}`,
	}, "\n")
	worker := &importer{secret: []byte(strings.Repeat("s", 32)), dryRun: true, current: make([]sourceEvent, 0, maxImportBatch)}
	if err := worker.consume(context.Background(), strings.NewReader(input)); err == nil {
		t.Fatal("ungrouped import input was accepted")
	}
}
