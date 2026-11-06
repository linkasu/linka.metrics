package analyticsapi

import "time"

// DefaultQuery is pinned to the last complete Sunday. UI requests and weekly
// publication therefore share a cache key for the whole reporting week.
func DefaultQuery(now time.Time) Query {
	now = now.UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	days := int(day.Weekday())
	if days == 0 {
		days = 7
	}
	cutoff := day.AddDate(0, 0, -days)
	q := Query{ContractVersion: Version, CutoffDate: cutoff.Format(time.DateOnly), SeriesFrom: cutoff.AddDate(0, 0, -29).Format(time.DateOnly), SeriesTo: cutoff.Format(time.DateOnly), Metrics: []string{"active_1d", "active_7d", "active_30d", "calendar_active", "useful_actions", "sessions", "service_operations", "service_failed", "funnel_entered", "funnel_activated", "funnel_repeated", "funnel_immature"}}
	for _, product := range Products() {
		q.Products = append(q.Products, product.ID)
	}
	return q
}
