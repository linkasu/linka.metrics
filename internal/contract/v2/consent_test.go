package v2

import (
	"fmt"
	"strings"
	"testing"

	"github.com/linkasu/linka.plays-metric/internal/jsonstrict"
)

func TestConsentRequestStrictContract(t *testing.T) {
	body := fmt.Sprintf(`{"schema_version":2,"request_id":"10000000-0000-4000-8000-000000000001","scope":{"product":"linka-pictures","subject_key":%q},"consent_epoch":1,"version":2,"preference":"denied","recorded_at":"2026-11-08T12:00:00.123Z"}`, strings.Repeat("a", 64))
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", body, true},
		{"uint64 max", strings.Replace(body, `"version":2`, `"version":18446744073709551615`, 1), true},
		{"missing epoch", strings.Replace(body, `"consent_epoch":1,`, "", 1), false},
		{"zero epoch", strings.Replace(body, `"consent_epoch":1`, `"consent_epoch":0`, 1), false},
		{"negative epoch", strings.Replace(body, `"consent_epoch":1`, `"consent_epoch":-1`, 1), false},
		{"overflow", strings.Replace(body, `"version":2`, `"version":18446744073709551616`, 1), false},
		{"null version", strings.Replace(body, `"version":2`, `"version":null`, 1), false},
		{"zero version", strings.Replace(body, `"version":2`, `"version":0`, 1), false},
		{"unknown preference", strings.Replace(body, `"denied"`, `"yes"`, 1), false},
		{"missing milliseconds", strings.Replace(body, ".123Z", "Z", 1), false},
		{"excess precision", strings.Replace(body, ".123Z", ".1234Z", 1), false},
		{"invalid date", strings.Replace(body, "2026-11-08", "2026-02-31", 1), false},
		{"invalid timezone", strings.Replace(body, ".123Z", ".123+24:00", 1), false},
		{"duplicate", strings.Replace(body, `"version":2`, `"version":2,"version":3`, 1), false},
		{"unknown", strings.Replace(body, `"version":2`, `"version":2,"extra":1`, 1), false},
		{"scope epoch forbidden", strings.Replace(body, `"product":`, `"consent_epoch":1,"product":`, 1), false},
		{"person forbidden", strings.Replace(body, `"product":`, `"person_key":null,"product":`, 1), false},
		{"org forbidden", strings.Replace(body, `"product":`, `"org_key":null,"product":`, 1), false},
		{"bad uuid", strings.Replace(body, "10000000-0000-4000-8000-000000000001", "not-uuid", 1), false},
		{"trailing JSON", body + `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConsentRequest([]byte(tc.body))
			if (err == nil) != tc.valid {
				t.Fatalf("parse error = %v", err)
			}
		})
	}
}

func TestBatchScopeOptionalConsentEpoch(t *testing.T) {
	for _, tc := range []struct {
		epoch string
		valid bool
	}{
		{"", true}, {"1", true}, {"18446744073709551615", true}, {"0", false}, {"null", false}, {"-1", false}, {"1.5", false}, {"18446744073709551616", false},
	} {
		t.Run(tc.epoch, func(t *testing.T) {
			body := fmt.Sprintf(`{"product":"linka-pictures","subject_key":%q`, strings.Repeat("a", 64))
			if tc.epoch != "" {
				body += `,"consent_epoch":` + tc.epoch
			}
			body += `}`
			var scope Scope
			err := jsonstrict.DecodeObject([]byte(body), &scope, MaxJSONDepth)
			if err == nil {
				_, err = validateScope(scope)
			}
			if (err == nil) != tc.valid {
				t.Fatalf("epoch=%s error=%v", tc.epoch, err)
			}
		})
	}
}
