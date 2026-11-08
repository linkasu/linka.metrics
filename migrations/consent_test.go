package migrations

import (
	"strings"
	"testing"
)

func TestConsentMigrationPreservesSafetyAndOldWriterDefaults(t *testing.T) {
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	consent := loaded[len(loaded)-1]
	if consent.Version != 19 {
		t.Fatal("consent migration missing")
	}
	var ledger, state string
	viewCount, columnCount := 0, 0
	for _, statement := range consent.Statements {
		if strings.Contains(statement, "ADD COLUMN IF NOT EXISTS consent_epoch UInt64 DEFAULT 0") {
			columnCount++
		}
		if strings.Contains(statement, "CREATE TABLE IF NOT EXISTS linka_metric.consent_ledger_v2") {
			ledger = statement
		}
		if strings.Contains(statement, "CREATE OR REPLACE VIEW linka_metric.consent_subject_state_v2") {
			state = statement
		}
		if !strings.Contains(statement, "CREATE OR REPLACE VIEW linka_metric.datalens_") {
			continue
		}
		viewCount++
		for _, required := range []string{"events.consent_epoch = 0", "consent_subject_state_v2", "WHERE preference = 'allowed'", "privacy_suppressions_v2 FINAL WHERE active = true", "person_key IS NOT NULL", "org_key IS NOT NULL", "events.expires_at > now()"} {
			if !strings.Contains(statement, required) {
				t.Fatalf("safe view missing %q: %s", required, statement)
			}
		}
	}
	if viewCount != 9 || columnCount != 7 {
		t.Fatalf("views=%d epoch columns=%d", viewCount, columnCount)
	}
	if ledger == "" || strings.Contains(ledger, "TTL ") || !strings.Contains(ledger, "body_sha256 FixedString(64)") || !strings.Contains(ledger, "applied Bool") {
		t.Fatal("ledger must retain all safety receipts without a TTL")
	}
	if !strings.Contains(state, "WHERE applied = true") || !strings.Contains(state, "argMax(consent_epoch, version)") {
		t.Fatal("stale receipts may influence current consent")
	}
}
