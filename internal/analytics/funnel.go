package analytics

import "time"

// FunnelResult uses only mature subjects as the Entered denominator for both
// conversions. Immature subjects are separate, not failed conversions.
type FunnelResult struct {
	Entered   int
	Activated int
	Repeated  int
	Immature  int
}

// Funnel finds each subject's first eligible observation across the input, then
// selects entries in [cohortFrom, cohortTo). A subject matures at entry + 7*24h.
// Activation is the first useful outcome in [entry, entry+7*24h], inclusive.
// Repeat must be strictly later than activation, on a different UTC day, and
// no later than that same inclusive endpoint. Events strictly after asOf are
// ignored; events exactly at asOf are allowed. Input order is irrelevant.
func Funnel(events []Observation, product string, cohortFrom, cohortTo, asOf time.Time) FunnelResult {
	type subjectWindow struct {
		entry      time.Time
		activation time.Time
		latest     time.Time
		useful     bool
	}
	subjects := make(map[string]*subjectWindow)
	for _, event := range events {
		if event.Product != product || event.Subject == "" || (!event.Activity && !event.Useful) || event.At.After(asOf) {
			continue
		}
		window, ok := subjects[event.Subject]
		if !ok {
			subjects[event.Subject] = &subjectWindow{entry: event.At}
		} else if event.At.Before(window.entry) {
			window.entry = event.At
		}
	}
	for _, event := range events {
		if event.Product != product || !event.Useful || event.At.After(asOf) {
			continue
		}
		window, ok := subjects[event.Subject]
		if !ok || event.At.After(window.entry.Add(7*24*time.Hour)) {
			continue
		}
		if !window.useful || event.At.Before(window.activation) {
			window.activation = event.At
		}
		if !window.useful || event.At.After(window.latest) {
			window.latest = event.At
		}
		window.useful = true
	}
	var result FunnelResult
	for _, window := range subjects {
		if window.entry.Before(cohortFrom) || !window.entry.Before(cohortTo) {
			continue
		}
		if asOf.Before(window.entry.Add(7 * 24 * time.Hour)) {
			result.Immature++
			continue
		}
		result.Entered++
		if window.useful {
			result.Activated++
			if window.latest.After(window.activation) && window.latest.UTC().Format(time.DateOnly) != window.activation.UTC().Format(time.DateOnly) {
				result.Repeated++
			}
		}
	}
	return result
}
