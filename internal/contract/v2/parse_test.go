package v2

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

const testOpaqueKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestParseBatchAcceptsOneTypedStream(t *testing.T) {
	body := validCommonBatch()
	batch, err := ParseBatch([]byte(body), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if batch.RecordCount() != 1 || len(batch.CommonRecords) != 1 || len(batch.TechnicalRecords) != 0 || len(batch.PlaysRecords) != 0 || len(batch.ProductRecords) != 0 {
		t.Fatalf("unexpected typed batch: %+v", batch)
	}
}

func TestParseBatchAcceptsRegisteredProductKindsAndPlatforms(t *testing.T) {
	tests := []struct {
		product  string
		kind     string
		platform string
	}{
		{"linka-looks", "start", "windows"},
		{"linka-pictures", "set_import", "android"},
		{"linka-type", "say", "web"},
		{"linka-paperboard", "board_open", "ios"},
		{"linka-site", "page_view", "web"},
		{"linka-tts", "tts_generated", "linux"},
	}
	for _, test := range tests {
		t.Run(test.product, func(t *testing.T) {
			batch, err := ParseBatch([]byte(validProductBatch(test.product, test.kind, test.platform)), testNow)
			if err != nil {
				t.Fatal(err)
			}
			if batch.RecordCount() != 1 || len(batch.ProductRecords) != 1 {
				t.Fatalf("unexpected product batch: %+v", batch)
			}
		})
	}
}

func TestParseBatchAcceptsPlaysInputMethods(t *testing.T) {
	for _, inputMethod := range []string{"mouse", "touch", "gaze", "keyboard", "unknown", "mixed"} {
		t.Run(inputMethod, func(t *testing.T) {
			batch, err := ParseBatch([]byte(validPlaysBatch(inputMethod)), testNow)
			if err != nil {
				t.Fatal(err)
			}
			if got := batch.PlaysRecords[0].InputMethod; got != inputMethod {
				t.Fatalf("input_method = %q, want %q", got, inputMethod)
			}
		})
	}
}

func TestParseBatchAcceptsPlaysGameCategories(t *testing.T) {
	for _, category := range []string{"gaze-basics", "visual-search", "sequencing", "language-aac", "numeracy", "strategy", "continuous-control", "unknown"} {
		t.Run(category, func(t *testing.T) {
			body := strings.Replace(validPlaysBatch("gaze"), `"game_category":"gaze-basics"`, fmt.Sprintf(`"game_category":%q`, category), 1)
			batch, err := ParseBatch([]byte(body), testNow)
			if err != nil {
				t.Fatal(err)
			}
			if got := batch.PlaysRecords[0].GameCategory; got != category {
				t.Fatalf("game_category = %q, want %q", got, category)
			}
		})
	}
}

func TestParseBatchAcceptsSessionFinishedOutcomes(t *testing.T) {
	for _, outcome := range []string{"completed", "interrupted", "cancelled", "error", "incomplete", "lost", "draw"} {
		t.Run(outcome, func(t *testing.T) {
			finished := fmt.Sprintf(`"kind":"session_finished","outcome":%q,"duration_ms":1000`, outcome)
			body := strings.Replace(validPlaysBatch("gaze"), `"kind":"session_started"`, finished, 1)
			batch, err := ParseBatch([]byte(body), testNow)
			if err != nil {
				t.Fatal(err)
			}
			if got := *batch.PlaysRecords[0].Outcome; got != outcome {
				t.Fatalf("outcome = %q, want %q", got, outcome)
			}
		})
	}
}

func TestParseBatchAcceptsAppLocales(t *testing.T) {
	for _, locale := range []string{"ru", "ru-RU", "en", "en-US", "other"} {
		t.Run(locale, func(t *testing.T) {
			body := strings.Replace(validCommonBatch(), `"locale":"ru-RU"`, fmt.Sprintf(`"locale":%q`, locale), 1)
			batch, err := ParseBatch([]byte(body), testNow)
			if err != nil {
				t.Fatal(err)
			}
			if got := batch.CommonRecords[0].App.Locale; got != locale {
				t.Fatalf("app.locale = %q, want %q", got, locale)
			}
		})
	}
}

func TestParseBatchRejectsRawUnsupportedAppLocale(t *testing.T) {
	body := strings.Replace(validCommonBatch(), `"locale":"ru-RU"`, `"locale":"de-DE"`, 1)
	if _, err := ParseBatch([]byte(body), testNow); err == nil {
		t.Fatal("raw unsupported app.locale was accepted instead of privacy-safe other")
	}
}

func TestParseBatchRejectsCrossProductKindAndPayload(t *testing.T) {
	tests := []string{
		validProductBatch("linka-looks", "set_import", "windows"),
		strings.Replace(validProductBatch("linka-type", "say", "web"), `"kind":"say"`, `"kind":"say","text":"private"`, 1),
		strings.Replace(validProductBatch("linka-type", "say", "web"), `"kind":"say"`, `"kind":"say","attributes":{"word":"private"}`, 1),
	}
	for _, body := range tests {
		if _, err := ParseBatch([]byte(body), testNow); err == nil {
			t.Fatal("unsafe product batch was accepted")
		}
	}
}

func TestParseBatchRejectsUnsafeJSONAndEnums(t *testing.T) {
	tests := map[string]string{
		"duplicate": strings.Replace(validCommonBatch(), `"kind":"app_started"`, `"kind":"app_started","kind":"app_closed"`, 1),
		"unknown":   strings.Replace(validCommonBatch(), `"kind":"app_started"`, `"kind":"arbitrary"`, 1),
		"field":     strings.Replace(validCommonBatch(), `"kind":"app_started"`, `"kind":"app_started","text":"private"`, 1),
		"trailing":  validCommonBatch() + `{}`,
		"stream":    strings.Replace(validCommonBatch(), `"stream":"common"`, `"stream":"raw"`, 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBatch([]byte(body), testNow); err == nil {
				t.Fatal("unsafe batch was accepted")
			}
		})
	}
}

func TestParseBatchRejectsTimestampAndRangeViolations(t *testing.T) {
	tests := []string{
		strings.Replace(validCommonBatch(), "2026-07-18T12:00:00.000Z", "2026-07-18T12:00:00.0001Z", 1),
		strings.Replace(validCommonBatch(), "2026-07-18T12:00:00.000Z", "2019-07-18T12:00:00.000Z", 1),
		strings.Replace(validCommonBatch(), "2026-07-18T11:59:00.000Z", "2026-07-18T12:06:00.000Z", 1),
	}
	for _, body := range tests {
		if _, err := ParseBatch([]byte(body), testNow); err == nil {
			t.Fatal("invalid timestamp was accepted")
		}
	}
}

func TestParseBatchRejectsDuplicateRecordIDs(t *testing.T) {
	duplicate := `,{
    "record_id":"20000000-0000-4000-8000-000000000002",
    "occurred_at":"2026-07-18T11:59:00.000Z",
    "kind":"app_started",
    "app_session_id":"40000000-0000-4000-8000-000000000004",
    "app":{"version":"1.2.3","build":"42","platform":"linux","os_version":"6.8","locale":"ru-RU"}
  }`
	body := strings.Replace(validCommonBatch(), "}]", "}"+duplicate+"]", 1)
	if _, err := ParseBatch([]byte(body), testNow); err == nil {
		t.Fatal("duplicate record_id was accepted")
	}
}

func TestParsePrivacyRequestAndIdempotency(t *testing.T) {
	body := fmt.Sprintf(`{"schema_version":2,"request_id":"10000000-0000-4000-8000-000000000001","scope":{"product":"linka-plays","subject_key":%q},"action":"delete","requested_at":"2026-07-18T12:00:00Z"}`, testOpaqueKey)
	request, err := ParsePrivacyRequest([]byte(body), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if request.Action != PrivacyDelete {
		t.Fatalf("action = %q", request.Action)
	}
	if err := ValidateIdempotencyKey(request.RequestID, request.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := ValidateIdempotencyKey("20000000-0000-4000-8000-000000000002", request.RequestID); err == nil {
		t.Fatal("mismatched idempotency key was accepted")
	}
}

func TestParsePrivacyRequestNormalizesPersistedNanoseconds(t *testing.T) {
	body := fmt.Sprintf(`{"schema_version":2,"request_id":"10000000-0000-4000-8000-000000000001","scope":{"product":"linka-plays","subject_key":%q},"action":"delete","requested_at":"2026-07-18T12:00:00.123456789Z"}`, testOpaqueKey)
	request, err := ParsePrivacyRequest([]byte(body), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := request.RequestedAtTime.Nanosecond(), 123*int(time.Millisecond); got != want {
		t.Fatalf("requested_at nanoseconds = %d, want %d", got, want)
	}
}

func TestParseBatchRejectsSubMillisecondPrecision(t *testing.T) {
	body := strings.Replace(validCommonBatch(), "2026-07-18T12:00:00.000Z", "2026-07-18T12:00:00.000001Z", 1)
	if _, err := ParseBatch([]byte(body), testNow); err == nil {
		t.Fatal("sub-millisecond batch timestamp was accepted")
	}
}

func validCommonBatch() string {
	return fmt.Sprintf(`{
  "schema_version":2,
  "batch_id":"10000000-0000-4000-8000-000000000001",
  "scope":{"product":"linka-plays","subject_key":%q},
  "stream":"common",
  "sent_at":"2026-07-18T12:00:00.000Z",
  "records":[{
    "record_id":"20000000-0000-4000-8000-000000000002",
    "occurred_at":"2026-07-18T11:59:00.000Z",
    "kind":"app_started",
    "app_session_id":"30000000-0000-4000-8000-000000000003",
    "app":{"version":"1.2.3","build":"42","platform":"linux","os_version":"6.8","locale":"ru-RU"}
  }]
}`, testOpaqueKey)
}

func validProductBatch(productID, kind, platform string) string {
	return fmt.Sprintf(`{
  "schema_version":2,
  "batch_id":"10000000-0000-4000-8000-000000000001",
  "scope":{"product":%q,"subject_key":%q},
  "stream":"product",
  "sent_at":"2026-07-18T12:00:00.000Z",
  "records":[{
    "record_id":"20000000-0000-4000-8000-000000000002",
    "occurred_at":"2026-07-18T11:59:00.000Z",
    "kind":%q,
    "app_session_id":"30000000-0000-4000-8000-000000000003",
    "app":{"version":"1.2.3","build":"42","platform":%q,"os_version":"1","locale":"ru-RU"}
  }]
}`, productID, testOpaqueKey, kind, platform)
}

func validPlaysBatch(inputMethod string) string {
	return fmt.Sprintf(`{
  "schema_version":2,
  "batch_id":"10000000-0000-4000-8000-000000000001",
  "scope":{"product":"linka-plays","subject_key":%q},
  "stream":"plays",
  "sent_at":"2026-07-18T12:00:00.000Z",
  "records":[{
    "record_id":"20000000-0000-4000-8000-000000000002",
    "occurred_at":"2026-07-18T11:59:00.000Z",
    "kind":"session_started",
    "app_session_id":"30000000-0000-4000-8000-000000000003",
    "game_session_id":"40000000-0000-4000-8000-000000000004",
    "app":{"version":"1.2.3","build":"42","platform":"linux","os_version":"6.8","locale":"ru-RU"},
    "game_id":"aquarium",
    "game_category":"gaze-basics",
    "input_method":%q
  }]
}`, testOpaqueKey, inputMethod)
}
