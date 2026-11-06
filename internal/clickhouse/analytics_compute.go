package clickhouse

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/linkasu/linka.plays-metric/internal/analyticsapi"
)

var _ analyticsapi.Computer = (*Store)(nil)

const analyticsPopulation = "Consented observed installation/source identifiers, including legacy source units; not people or verified product accounts. Missing observations do not prove zero activity."

var errAnalyticsSourceSchema = errors.New("analytics source schema unavailable")

// ComputeAnalytics deliberately does not infer account identity or a funnel from
// aggregate activity: neither historical coverage nor first-ever use is proven.
func (s *Store) ComputeAnalytics(ctx context.Context, q analyticsapi.Query) (analyticsapi.Report, error) {
	hash, err := q.Hash()
	if err != nil {
		return analyticsapi.Report{}, err
	}
	cutoff, _ := time.Parse(time.DateOnly, q.CutoffDate)
	r := analyticsapi.Report{ContractVersion: analyticsapi.Version, QueryHash: hash,
		GeneratedAt: s.now().UTC(), DataAsOf: cutoff.AddDate(0, 0, 1), Status: "no_data",
		Results: []analyticsapi.Result{}, Capabilities: []analyticsapi.Capability{}, Insights: []analyticsapi.Insight{}}
	if q.Methodology == "ga4" {
		return r, nil
	}
	// The executor is serial; also bound direct callers to one source query at a time.
	s.analyticsMu.Lock()
	defer s.analyticsMu.Unlock()
	for _, product := range q.Products {
		metrics := []string{"active_1d", "active_7d", "active_30d", "calendar_active", "useful_actions", "sessions"}
		methodology, unit := "installation-v2", "installation"
		if product == "linka-tts" {
			metrics, methodology, unit = []string{"service_operations", "service_failed"}, "tts", "service_operation"
		}
		capability := analyticsapi.Capability{Product: product, Source: "metrics-v2", Methodology: methodology,
			Unit: unit, Metrics: metrics, Dimensions: []string{}, Status: "ready"}
		reason := ""
		switch {
		case q.Methodology == "account-v3":
			reason = "account_v3_client_and_verified_alias_required"
			capability.Methodology, capability.Unit = "account-v3", "product_account"
		case q.Methodology != "" && q.Methodology != methodology:
			reason = "methodology_not_supported_by_source"
		case len(q.Dimensions) != 0:
			reason = "dimension_breakdown_not_supported"
		case len(q.Filters["country"]) != 0 || len(q.Filters["region"]) != 0:
			reason = "geographic_filter_not_supported_by_source"
		}
		needsRead := false
		for _, metric := range q.Metrics {
			if analyticsMetricSupported(metric, metrics) && reason == "" {
				needsRead = true
			}
		}
		var total analyticsCounts
		days := make(map[string]analyticsCounts)
		if needsRead {
			query, args := analyticsComputeSQL(q, product)
			total, days, err = s.analyticsReadCounts(ctx, query, args)
			if err != nil {
				reason = "source_query_failed"
				if errors.Is(err, analyticsapi.ErrBudget) {
					reason = "source_query_budget_exceeded"
				} else if errors.Is(err, errAnalyticsSourceSchema) {
					reason = "source_view_or_required_column_missing"
				}
			}
		}
		start := len(r.Results)
		for _, metric := range q.Metrics {
			result := analyticsResult(q, product, metric, capability.Methodology, capability.Unit)
			result.Reason = reason
			if result.Reason == "" && !analyticsMetricSupported(metric, metrics) {
				result.Reason = "metric_not_supported_by_source"
				if strings.HasPrefix(metric, "funnel_") {
					result.Reason = "funnel_first_observed_history_and_complete_seven_day_coverage_unproven"
				}
			}
			if metric == "calendar_active" && result.Reason == "" {
				_, calendarTo, seriesMonth := analyticsCalendarWindow(q)
				if seriesMonth.Before(calendarTo.AddDate(0, -13, 0)) {
					result.Reason = "calendar_series_range_exceeds_thirteen_month_budget"
				}
			}
			if result.Reason != "" {
				result.Status = "unavailable"
			} else {
				result.Value, result.Contributors, result.Status = total.observation(metric)
				if result.Status == "unavailable" {
					result.Reason = "aggregate_count_overflow"
				} else if result.Value == nil {
					result.Reason = "source_observation_or_complete_coverage_missing"
				}
				if metric == "active_1d" || metric == "useful_actions" {
					from, _ := time.Parse(time.DateOnly, q.SeriesFrom)
					to, _ := time.Parse(time.DateOnly, q.SeriesTo)
					for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
						key := day.Format(time.DateOnly)
						counts := days[key]
						if metric == "active_1d" {
							counts.active1 = counts.dailyActive
						}
						v, contributors, status := counts.observation(metric)
						result.Series = append(result.Series, analyticsapi.Point{Date: key, Value: v, Contributors: contributors, Status: status})
					}
				}
				if metric == "calendar_active" {
					_, calendarTo, seriesMonth := analyticsCalendarWindow(q)
					for month := seriesMonth; month.Before(calendarTo); month = month.AddDate(0, 1, 0) {
						key := month.Format(time.DateOnly)
						v, contributors, status := days["month:"+key].observation(metric)
						result.Series = append(result.Series, analyticsapi.Point{Date: key, Value: v, Contributors: contributors, Status: status})
					}
				}
			}
			r.Results = append(r.Results, result)
		}
		capability.Status = analyticsResultsStatus(r.Results[start:])
		if reason != "" {
			capability.Reason = reason
		} else if capability.Status == "partial" || capability.Status == "unavailable" {
			capability.Reason = "some_requested_metrics_not_supported"
		}
		r.Capabilities = append(r.Capabilities, capability)
	}
	r.Status = analyticsResultsStatus(r.Results)
	return r, nil
}

func analyticsMetricSupported(metric string, supported []string) bool {
	for _, name := range supported {
		if metric == name {
			return true
		}
	}
	return false
}

func analyticsCalendarWindow(q analyticsapi.Query) (from, to, seriesFrom time.Time) {
	cutoff, _ := time.Parse(time.DateOnly, q.CutoffDate)
	end := cutoff.AddDate(0, 0, 1)
	to = time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC)
	from = to.AddDate(0, -1, 0)
	series, _ := time.Parse(time.DateOnly, q.SeriesFrom)
	seriesFrom = time.Date(series.Year(), series.Month(), 1, 0, 0, 0, 0, time.UTC)
	return
}

func analyticsResult(q analyticsapi.Query, product, metric, methodology, unit string) analyticsapi.Result {
	from, _ := time.Parse(time.DateOnly, q.SeriesFrom)
	to, _ := time.Parse(time.DateOnly, q.SeriesTo)
	to = to.AddDate(0, 0, 1)
	cutoff, _ := time.Parse(time.DateOnly, q.CutoffDate)
	days := map[string]int{"active_1d": 1, "active_7d": 7, "active_30d": 30}[metric]
	if days > 0 {
		to = cutoff.AddDate(0, 0, 1)
		from = to.AddDate(0, 0, -days)
	} else if metric == "calendar_active" {
		from, to, _ = analyticsCalendarWindow(q)
	} else if metric == "useful_actions" {
		unit = "event"
	} else if metric == "sessions" {
		unit = "session"
	}
	note := analyticsPopulation
	if metric == "sessions" {
		note += " Sessions exclude legacy synthetic daily IDs (import-* builds)."
	}
	if product == "linka-tts" {
		note = "Observed typed request_completed service operations (completed, failed or cancelled), not all HTTP requests; the fixed TTS subject is never human reach."
	}
	return analyticsapi.Result{Product: product, Metric: metric, Unit: unit, Methodology: methodology,
		Source: "metrics-v2", Timezone: "UTC", WindowFrom: from, WindowTo: to, PopulationNote: note}
}

func analyticsObservedValue(value uint64) (*int64, string) {
	if value > math.MaxInt64 {
		return nil, "unavailable"
	}
	// shortcut: source views cannot prove complete coverage; do not publish zero until coverage is available.
	if value == 0 {
		return nil, "no_data"
	}
	v := int64(value)
	return &v, "ready"
}

func analyticsResultsStatus(results []analyticsapi.Result) string {
	ready, unavailable := 0, 0
	for _, result := range results {
		if result.Status == "unavailable" {
			unavailable++
		} else if result.Status == "ready" {
			ready++
		}
	}
	if unavailable == len(results) && unavailable > 0 {
		return "unavailable"
	}
	if unavailable > 0 {
		return "partial"
	}
	if ready > 0 {
		return "ready"
	}
	return "no_data"
}

type analyticsCounts struct {
	active1, active7, active30, useful, sessions, operations, failed, dailyActive uint64
	usefulSubjects, sessionSubjects                                               uint64
	calendarActive                                                                uint64
}

func (c analyticsCounts) observation(metric string) (*int64, *int64, string) {
	value, status := analyticsObservedValue(c.value(metric))
	if metric == "service_failed" && c.operations > 0 && c.failed == 0 {
		return new(int64), nil, "ready"
	}
	if status != "ready" || metric == "service_operations" || metric == "service_failed" {
		return value, nil, status
	}
	contributors := value
	if metric == "useful_actions" {
		contributors, status = analyticsObservedValue(c.usefulSubjects)
	} else if metric == "sessions" {
		contributors, status = analyticsObservedValue(c.sessionSubjects)
	}
	if status == "unavailable" {
		return nil, nil, status
	}
	return value, contributors, "ready"
}

func (c analyticsCounts) value(metric string) uint64 {
	switch metric {
	case "active_1d":
		return c.active1
	case "active_7d":
		return c.active7
	case "active_30d":
		return c.active30
	case "calendar_active":
		return c.calendarActive
	case "useful_actions":
		return c.useful
	case "sessions":
		return c.sessions
	case "service_operations":
		return c.operations
	case "service_failed":
		return c.failed
	}
	return 0
}

func (s *Store) analyticsReadCounts(ctx context.Context, query string, args []any) (analyticsCounts, map[string]analyticsCounts, error) {
	ctx, cancel := analyticsContext(ctx)
	defer cancel()
	ctx = ch.Context(ctx, ch.WithSettings(ch.Settings{
		"max_execution_time": 8, "max_threads": 1, "max_memory_usage": 256 << 20,
		"max_rows_to_read": 3_000_000, "read_overflow_mode": "throw",
		"group_by_use_nulls": 0,
	}))
	total, days := analyticsCounts{}, make(map[string]analyticsCounts)
	rows, err := s.connection.Query(ctx, query, args...)
	if err != nil {
		var exception *ch.Exception
		if errors.As(err, &exception) && (exception.Code == 47 || exception.Code == 60) {
			return total, days, errAnalyticsSourceSchema
		}
		return total, days, analyticsStorageError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var all uint64
		var day time.Time
		var counts analyticsCounts
		if err := rows.Scan(&all, &day, &counts.active1, &counts.active7, &counts.active30, &counts.useful,
			&counts.sessions, &counts.operations, &counts.failed, &counts.dailyActive,
			&counts.usefulSubjects, &counts.sessionSubjects, &counts.calendarActive); err != nil {
			return analyticsCounts{}, nil, analyticsStorageError(ctx, err)
		}
		if all == 1 {
			total = counts
		} else if all == 2 {
			counts.calendarActive = counts.dailyActive
			days["month:"+day.UTC().Format(time.DateOnly)] = counts
		} else {
			days[day.UTC().Format(time.DateOnly)] = counts
		}
	}
	if err := rows.Err(); err != nil {
		return analyticsCounts{}, nil, analyticsStorageError(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return analyticsCounts{}, nil, analyticsStorageError(ctx, err)
	}
	return total, days, nil
}

// All identifiers and predicates below are server-owned constants. Filter values
// are bound separately, including the product scope shared by every source.
func analyticsComputeSQL(q analyticsapi.Query, product string) (string, []any) {
	cutoff, _ := time.Parse(time.DateOnly, q.CutoffDate)
	from, _ := time.Parse(time.DateOnly, q.SeriesFrom)
	to, _ := time.Parse(time.DateOnly, q.SeriesTo)
	args := []any{product, cutoff.AddDate(0, 0, 1), from, to.AddDate(0, 0, 1)}
	calendarFrom, calendarTo, calendarScanFrom := analyticsCalendarWindow(q)
	if calendarScanFrom.After(calendarFrom) {
		calendarScanFrom = calendarFrom
	}
	if calendarScanFrom.Before(calendarTo.AddDate(0, -13, 0)) {
		calendarScanFrom = calendarTo.AddDate(0, -13, 0)
	}
	args = append(args, calendarFrom, calendarTo, calendarScanFrom)
	filters := ""
	for _, dimension := range []string{"platform", "app_version", "locale"} {
		if values := q.Filters[dimension]; len(values) > 0 {
			filters += " AND " + dimension + " IN (?)"
			args = append(args, values)
		}
	}
	scope := ` WHERE product = product_filter
		AND ((occurred_at >= cutoff_to - INTERVAL 30 DAY AND occurred_at < cutoff_to)
		OR (occurred_at >= series_from AND occurred_at < series_to)`
	if analyticsMetricSupported("calendar_active", q.Metrics) && product != "linka-tts" {
		scope += ` OR (occurred_at >= calendar_scan_from AND occurred_at < calendar_to)`
	}
	scope += ")" + filters
	selectFacts := func(view, eligible, useful, operation, failed string) string {
		return "SELECT subject_key, app_session_key, app_build, occurred_at, " + eligible + " AS eligible, " + useful +
			" AS useful, " + operation + " AS operation, " + failed + " AS failed FROM " + view + scope
	}
	var facts []string
	switch product {
	case "linka-looks", "linka-pictures", "linka-type":
		open, outcome := "app_open", "utterance_completed"
		if product == "linka-looks" {
			open = "start"
		} else if product == "linka-type" {
			outcome = "speech_completed"
		}
		facts = append(facts, selectFacts("datalens_product_v2", "kind = '"+open+"'", "false", "false", "false"))
		success := "kind = '" + outcome + "' AND result = 'completed'"
		facts = append(facts, selectFacts("datalens_outcomes_v1", success, success, "false", "false"))
	case "linka-paperboard":
		facts = append(facts, selectFacts("datalens_product_v2", "kind IN ('app_open', 'phrase_spoken')", "kind = 'phrase_spoken'", "false", "false"))
	case "linka-plays":
		facts = append(facts, selectFacts("datalens_common_v3", "kind = 'app_started'", "false", "false", "false"))
		success := "kind = 'session_finished' AND outcome = 'completed'"
		facts = append(facts, selectFacts("datalens_plays_v3", success, success, "false", "false"))
	case "linka-tts":
		facts = append(facts, selectFacts("datalens_outcomes_v1", "false", "false",
			"kind = 'request_completed'", "kind = 'request_completed' AND result = 'failed'"))
	}
	// Filters occur in each union branch, so repeat their bound values in order.
	filterArgs := append([]any(nil), args[7:]...)
	for i := 1; i < len(facts); i++ {
		args = append(args, filterArgs...)
	}
	validSession := `eligible AND occurred_at >= series_from AND occurred_at < series_to
			AND NOT startsWith(app_build, 'import-') AND app_session_key != 0
			AND app_session_key != cityHash64(product_filter, toUUID('00000000-0000-0000-0000-000000000000'))`
	query := `WITH ? AS product_filter, ? AS cutoff_to, ? AS series_from, ? AS series_to,
		? AS calendar_from, ? AS calendar_to, ? AS calendar_scan_from,
		toDate(occurred_at, 'UTC') AS day, toStartOfMonth(day) AS month
	SELECT toUInt64(multiIf(grouping(day) = 0, 0, grouping(month) = 0, 2, 1)) AS bucket_kind,
		if(grouping(day) = 0, day, month) AS bucket_date,
		uniqExactIf(subject_key, eligible AND occurred_at >= cutoff_to - INTERVAL 1 DAY AND occurred_at < cutoff_to),
		uniqExactIf(subject_key, eligible AND occurred_at >= cutoff_to - INTERVAL 7 DAY AND occurred_at < cutoff_to),
		uniqExactIf(subject_key, eligible AND occurred_at >= cutoff_to - INTERVAL 30 DAY AND occurred_at < cutoff_to),
		countIf(useful AND occurred_at >= series_from AND occurred_at < series_to),
		uniqExactIf(app_session_key, ` + validSession + `),
		countIf(operation AND occurred_at >= series_from AND occurred_at < series_to),
		countIf(failed AND occurred_at >= series_from AND occurred_at < series_to),
		uniqExactIf(subject_key, eligible),
		uniqExactIf(subject_key, useful AND occurred_at >= series_from AND occurred_at < series_to),
		uniqExactIf(subject_key, ` + validSession + `),
		uniqExactIf(subject_key, eligible AND occurred_at >= calendar_from AND occurred_at < calendar_to)
	FROM (` + strings.Join(facts, " UNION ALL ") + `)
	WHERE eligible OR useful OR operation OR failed
	GROUP BY GROUPING SETS ((day), (month), ())
	LIMIT 850`
	return query, args
}
