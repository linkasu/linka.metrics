package app

import (
	"fmt"
	"os"
	"time"

	metricclickhouse "github.com/linkasu/linka.plays-metric/internal/clickhouse"
)

// LoadRetention reads the RETENTION_V2_* durations shared by writer,
// privacy worker and the legacy importer. An empty value leaves
// expires_at NULL, and ClickHouse TTL never deletes NULL rows.
func LoadRetention() (metricclickhouse.Retention, error) {
	var result metricclickhouse.Retention
	values := []struct {
		name        string
		destination *time.Duration
	}{
		{"RETENTION_V2_INGEST_BATCHES", &result.IngestBatches},
		{"RETENTION_V2_COMMON", &result.Common},
		{"RETENTION_V2_TECHNICAL", &result.Technical},
		{"RETENTION_V2_PLAYS", &result.Plays},
		{"RETENTION_V2_PRODUCT", &result.Product},
		{"RETENTION_V2_PRIVACY", &result.Privacy},
		{"RETENTION_V2_REGISTRY", &result.Registry},
		{"RETENTION_V2_DELETION_PROGRESS", &result.DeletionProgress},
	}
	for _, value := range values {
		duration, err := retentionDuration(value.name)
		if err != nil {
			return metricclickhouse.Retention{}, err
		}
		*value.destination = duration
	}
	return result, nil
}

func retentionDuration(name string) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", name)
	}
	return duration, nil
}
