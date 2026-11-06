package analytics

// Status is caller-supplied availability, separate from numeric counts. A ready
// metric may be zero; an empty status is not ready. Helpers do not infer status.
type Status string

const (
	StatusReady             Status = "ready"
	StatusUnknown           Status = "unknown"
	StatusSuppressed        Status = "suppressed"
	StatusImmature          Status = "immature"
	StatusUnsupportedFilter Status = "unsupported_filter"
	StatusSourceFailure     Status = "source_failure"
)
