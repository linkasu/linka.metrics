package ga4

import (
	"context"
	"encoding/base64"
	"os"
	"testing"
	"time"
)

func TestLiveServiceAccountReadOnly(t *testing.T) {
	encoded := os.Getenv("GA4_LIVE_SERVICE_ACCOUNT_BASE64")
	if encoded == "" {
		t.Skip("explicit read-only GA4 live credential not configured")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal("invalid encoded live credential")
	}
	token, err := ServiceAccountTokenSource(data, nil)
	if err != nil {
		t.Fatal("live credential rejected")
	}
	client := NewClient(token, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	now := time.Now().UTC()
	for _, property := range []string{"151443791", "340729214", "251389924", "408073017"} {
		t.Run(property, func(t *testing.T) {
			report, err := client.RunReport(ctx, property, Request{From: now.AddDate(0, 0, -7).Format(time.DateOnly), To: now.AddDate(0, 0, -1).Format(time.DateOnly), Metrics: []string{"activeUsers"}})
			if err != nil {
				t.Fatalf("read-only live report failed: %T", err)
			}
			if report.Metadata.TimeZone == "" || report.Empty && len(report.Rows) != 0 {
				t.Fatal("invalid native report metadata")
			}
		})
	}
}
