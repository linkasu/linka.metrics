package analytics

import "time"

// ActiveUnits counts exact distinct subjects (or installations) in [from, to).
// Empty selected keys and technical events are excluded. Times are instants;
// callers supply full UTC reporting-day boundaries for DAU, WAU and MAU.
func ActiveUnits(events []Observation, product string, from, to time.Time, installations bool) int {
	units := make(map[string]struct{})
	for _, event := range events {
		if event.Product != product || (!event.Activity && !event.Useful) || event.At.Before(from) || !event.At.Before(to) {
			continue
		}
		key := event.Subject
		if installations {
			key = event.Installation
		}
		if key != "" {
			units[key] = struct{}{}
		}
	}
	return len(units)
}
