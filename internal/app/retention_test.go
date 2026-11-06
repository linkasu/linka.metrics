package app

import (
	"strings"
	"testing"
	"time"

	metricclickhouse "github.com/linkasu/linka.plays-metric/internal/clickhouse"
)

func TestLoadRetention(t *testing.T) {
	names := []string{
		"RETENTION_V2_INGEST_BATCHES", "RETENTION_V2_COMMON", "RETENTION_V2_TECHNICAL",
		"RETENTION_V2_PLAYS", "RETENTION_V2_PRODUCT", "RETENTION_V2_OUTCOME",
		"RETENTION_V2_PRIVACY", "RETENTION_V2_REGISTRY", "RETENTION_V2_DELETION_PROGRESS",
	}
	for _, environment := range []string{"", "development", "production"} {
		t.Run("positive/"+environment, func(t *testing.T) {
			t.Setenv("DEPLOYMENT_ENVIRONMENT", environment)
			for i, name := range names {
				t.Setenv(name, (time.Duration(i+1) * time.Hour).String())
			}
			got, err := LoadRetention()
			if err != nil {
				t.Fatal(err)
			}
			want := metricclickhouse.Retention{
				IngestBatches: time.Hour, Common: 2 * time.Hour, Technical: 3 * time.Hour,
				Plays: 4 * time.Hour, Product: 5 * time.Hour, Outcome: 6 * time.Hour,
				Privacy: 7 * time.Hour, Registry: 8 * time.Hour, DeletionProgress: 9 * time.Hour,
			}
			if got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
		for _, name := range names {
			for _, value := range []string{"", "0", "-1h", "malformed"} {
				t.Run(environment+"/"+name+"/"+value, func(t *testing.T) {
					t.Setenv("DEPLOYMENT_ENVIRONMENT", environment)
					for _, key := range names {
						t.Setenv(key, "1h")
					}
					t.Setenv(name, value)
					got, err := LoadRetention()
					if value == "" && environment != "production" {
						if err != nil {
							t.Fatal(err)
						}
						return
					}
					if err == nil || !strings.Contains(err.Error(), name) {
						t.Fatalf("expected error naming %s, got %v", name, err)
					}
					if got != (metricclickhouse.Retention{}) {
						t.Fatalf("invalid config returned partial retention: %+v", got)
					}
				})
			}
		}
	}
	t.Run("development-empty", func(t *testing.T) {
		t.Setenv("DEPLOYMENT_ENVIRONMENT", "development")
		for _, name := range names {
			t.Setenv(name, "")
		}
		got, err := LoadRetention()
		if err != nil || got != (metricclickhouse.Retention{}) {
			t.Fatalf("expected zero retention, got %+v, error %v", got, err)
		}
	})
}
