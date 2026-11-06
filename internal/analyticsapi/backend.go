package analyticsapi

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound    = errors.New("analytics snapshot not found")
	ErrConflict    = errors.New("analytics idempotency conflict")
	ErrInvalidated = errors.New("analytics snapshot invalidated")
	ErrBudget      = errors.New("analytics query budget exceeded")
)

// Backend persists aggregate reports and their serial, single-host build queue.
// Reader calls must verify the current privacy revision and ValidUntil before
// returning a published snapshot. The executor owns all non-queued transitions.
type Backend interface {
	AnalyticsReport(context.Context, Query) (Report, error)
	AnalyticsEnqueue(context.Context, Query, string) (SyncRun, error)
	AnalyticsRun(context.Context, string) (SyncRun, error)
	AnalyticsNext(context.Context) (SyncRun, error)
	AnalyticsUpdate(context.Context, SyncRun) error
	AnalyticsPublish(context.Context, SyncRun, Report, string, time.Time) error
	AnalyticsPrivacy(context.Context) (string, time.Time, error)
}

// Computer reads only committed, privacy-filtered source facts. It is called by
// the serial executor, never by dashboard GET requests.
type Computer interface {
	ComputeAnalytics(context.Context, Query) (Report, error)
}
