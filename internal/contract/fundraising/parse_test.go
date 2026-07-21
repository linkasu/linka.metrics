package fundraising

import (
	"strings"
	"testing"
	"time"
)

func TestParseBatchAcceptsClosedPrivacySafePayment(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	batch, err := ParseBatch(validBatch(now), now)
	if err != nil || len(batch.Records) != 1 || *batch.Records[0].Amount != "1200.00" {
		t.Fatalf("batch=%#v err=%v", batch, err)
	}
}

func TestParseBatchRejectsSensitiveAndUnsafeFields(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	for _, replacement := range []string{
		`"email":"donor@example.test"`,
		`"payment_id":"provider-id"`,
		`"attribution_campaign":"free text"`,
		`"failure_code":"provider message"`,
	} {
		body := strings.Replace(string(validBatch(now)), `"attribution_campaign":"summer_2026"`, replacement, 1)
		if _, err := ParseBatch([]byte(body), now); err == nil {
			t.Fatalf("accepted forbidden input %s", replacement)
		}
	}
}

func TestParseBatchRejectsInvalidAmountAndFailureCodePlacement(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	for _, replacement := range []string{`"amount":"12.345"`, `"amount":"000.00"`, `"failure_code":"declined"`} {
		body := strings.Replace(string(validBatch(now)), `"amount":"1200.00"`, replacement, 1)
		if _, err := ParseBatch([]byte(body), now); err == nil {
			t.Fatalf("accepted invalid input %s", replacement)
		}
	}
}

func TestParseBatchRequiresNullableCampaignField(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	body := strings.Replace(string(validBatch(now)), ",\n    \"attribution_campaign\":\"summer_2026\"", "", 1)
	if _, err := ParseBatch([]byte(body), now); err == nil {
		t.Fatal("accepted a batch without attribution_campaign")
	}
}

func validBatch(now time.Time) []byte {
	return []byte(`{
  "schema_version":1,
  "batch_id":"10000000-0000-4000-8000-000000000001",
  "sent_at":"` + now.Format(time.RFC3339) + `",
  "records":[{
    "event_id":"20000000-0000-4000-8000-000000000002",
    "occurred_at":"` + now.Add(-time.Minute).Format(time.RFC3339) + `",
    "kind":"payment_succeeded",
    "amount":"1200.00",
    "currency":"RUB",
    "frequency":"one_time",
    "attribution_source":"utm",
    "attribution_campaign":"summer_2026"
  }]
}`)
}
