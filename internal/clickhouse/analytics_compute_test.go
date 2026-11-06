package clickhouse

import (
	"context"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/linkasu/linka.plays-metric/internal/analyticsapi"
)

func TestAnalyticsComputeUnsupportedDoesNotRead(t *testing.T) {
	for _, change := range []func(*analyticsapi.Query){
		func(q *analyticsapi.Query) { q.Methodology = "account-v3" },
		func(q *analyticsapi.Query) { q.Filters = map[string][]string{"country": {"RU"}} },
		func(q *analyticsapi.Query) { q.Filters = map[string][]string{"region": {"Moscow"}} },
		func(q *analyticsapi.Query) { q.Dimensions = []string{"platform"} },
		func(q *analyticsapi.Query) { q.Metrics = []string{"funnel_entered"} },
		func(q *analyticsapi.Query) { q.Products = []string{"linka-tts"} },
	} {
		q := analyticsTestQuery()
		change(&q)
		s := &Store{now: func() time.Time { return time.Unix(0, 0) }}
		r, err := s.ComputeAnalytics(context.Background(), q)
		if err != nil || len(r.Results) != 1 || r.Results[0].Value != nil || r.Results[0].Status != "unavailable" || r.Results[0].Reason == "" {
			t.Fatalf("unsupported query %+v: %+v, %v", q, r, err)
		}
	}
}

func TestAnalyticsComputeGA4AndInvalidQuery(t *testing.T) {
	s := &Store{now: time.Now}
	q := analyticsTestQuery()
	q.Methodology = "ga4"
	r, err := s.ComputeAnalytics(context.Background(), q)
	if err != nil || len(r.Results) != 0 || len(r.Capabilities) != 0 {
		t.Fatalf("GA4 must be composed by caller: %+v %v", r, err)
	}
	q.Products = []string{"linka-site"}
	if _, err := s.ComputeAnalytics(context.Background(), q); !errors.Is(err, analyticsapi.ErrInvalidQuery) {
		t.Fatalf("invalid product accepted: %v", err)
	}
}

func TestAnalyticsComputeSQLIsBoundAndExact(t *testing.T) {
	q := analyticsTestQuery()
	q.Filters = map[string][]string{"platform": {"linux' OR 1=1"}, "locale": {"ru"}, "app_version": {"1"}}
	query, args := analyticsComputeSQL(q, "linka-looks")
	for _, required := range []string{"datalens_product_v2", "datalens_outcomes_v1", "uniqExactIf", "GROUPING SETS", "kind = 'start'", "kind = 'utterance_completed' AND result = 'completed'", "platform IN (?)", "app_version IN (?)", "locale IN (?)", "occurred_at >=", "occurred_at <", "cityHash64(product_filter, toUUID("} {
		if !strings.Contains(query, required) {
			t.Errorf("SQL missing %q", required)
		}
	}
	if strings.Contains(query, "linux' OR") || strings.Contains(query, "sum(") || len(args) != strings.Count(query, "?") {
		t.Fatalf("unbound or inexact SQL: %s args=%+v", query, args)
	}
	for _, product := range analyticsapi.Products() {
		sql, _ := analyticsComputeSQL(analyticsTestQuery(), product.ID)
		if strings.Contains(sql, "technical") || strings.Contains(sql, "raw") {
			t.Fatalf("unapproved source for %s", product.ID)
		}
	}
}

func TestAnalyticsComputeProductPredicates(t *testing.T) {
	for _, test := range []struct {
		product string
		want    []string
		notWant []string
	}{
		{"linka-type", []string{"kind = 'app_open'", "kind = 'speech_completed' AND result = 'completed'"}, []string{"bank_action_completed", "phrase_composed", "realtime_sync"}},
		{"linka-pictures", []string{"kind = 'app_open'", "kind = 'utterance_completed' AND result = 'completed'"}, []string{"card_select", "output_speak"}},
		{"linka-paperboard", []string{"kind IN ('app_open', 'phrase_spoken')", "kind = 'phrase_spoken' AS useful"}, []string{"datalens_outcomes_v1", "symbol_selected"}},
		{"linka-plays", []string{"datalens_common_v3", "datalens_plays_v3", "kind = 'app_started'", "kind = 'session_finished' AND outcome = 'completed'"}, []string{"session_paused"}},
		{"linka-tts", []string{"false AS eligible", "false AS useful", "kind = 'request_completed' AS operation", "kind = 'request_completed' AND result = 'failed'"}, []string{"datalens_product_v2", "tts_generated"}},
	} {
		sql, _ := analyticsComputeSQL(analyticsTestQuery(), test.product)
		for _, want := range test.want {
			if !strings.Contains(sql, want) {
				t.Errorf("%s missing %s", test.product, want)
			}
		}
		for _, notWant := range test.notWant {
			if strings.Contains(sql, notWant) {
				t.Errorf("%s contains %s", test.product, notWant)
			}
		}
	}
	q := analyticsTestQuery()
	_, args := analyticsComputeSQL(q, "linka-type")
	for i, date := range []string{"2026-11-07", "2026-11-01", "2026-11-07"} {
		if args[i+1].(time.Time).Format(time.DateOnly) != date {
			t.Fatalf("wrong half-open bound: %+v", args)
		}
	}
}

type computeRows struct {
	driver.Rows
	rows  [][]any
	index int
}

func (r *computeRows) Next() bool { r.index++; return r.index <= len(r.rows) }
func (r *computeRows) Scan(dest ...any) error {
	for i, value := range r.rows[r.index-1] {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}
func (r *computeRows) Err() error   { return nil }
func (r *computeRows) Close() error { return nil }

type computeConn struct {
	driver.Conn
	responses []*computeRows
	calls     int
	failFirst bool
	queryErr  error
}

func (c *computeConn) Query(ctx context.Context, _ string, _ ...any) (driver.Rows, error) {
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 8*time.Second {
		panic("unbounded analytics query")
	}
	c.calls++
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	if c.failFirst && c.calls == 1 {
		return nil, errors.New("raw secret server error")
	}
	r := c.responses[0]
	c.responses = c.responses[1:]
	return r, nil
}

func computeFixtureRow(total uint64, day string, counts ...uint64) []any {
	d, _ := time.Parse(time.DateOnly, day)
	row := []any{total, d}
	for _, count := range counts {
		row = append(row, count)
	}
	if len(counts) == 8 {
		row = append(row, uint64(0), uint64(0))
	}
	if len(counts) <= 10 {
		row = append(row, uint64(0))
	}
	return row
}

func TestAnalyticsComputeCalendarWindows(t *testing.T) {
	for _, test := range []struct {
		cutoff, from, to string
		days             int
	}{
		{"2026-11-01", "2026-10-01", "2026-11-01", 31},
		{"2026-10-31", "2026-10-01", "2026-11-01", 31},
		{"2024-03-15", "2024-02-01", "2024-03-01", 29},
		{"2025-03-01", "2025-02-01", "2025-03-01", 28},
		{"2026-01-01", "2025-12-01", "2026-01-01", 31},
	} {
		q := analyticsTestQuery()
		q.CutoffDate, q.SeriesFrom, q.SeriesTo = test.cutoff, test.from, test.cutoff
		result := analyticsResult(q, "linka-type", "calendar_active", "installation-v2", "installation")
		if result.WindowFrom.Format(time.DateOnly) != test.from || result.WindowTo.Format(time.DateOnly) != test.to || result.WindowTo.Sub(result.WindowFrom) != time.Duration(test.days)*24*time.Hour {
			t.Fatalf("calendar window: %+v", result)
		}
	}
}

func TestAnalyticsComputeCalendarSeriesDistinct(t *testing.T) {
	q := analyticsTestQuery()
	q.CutoffDate, q.SeriesFrom, q.SeriesTo = "2026-11-01", "2026-09-15", "2026-11-01"
	q.Metrics = []string{"calendar_active", "active_30d"}
	c := &computeConn{responses: []*computeRows{{rows: [][]any{
		computeFixtureRow(1, "1970-01-01", 1, 1, 7, 0, 0, 0, 0, 8, 0, 0, 1),
		computeFixtureRow(2, "2026-10-01", 0, 0, 7, 0, 0, 0, 0, 1, 0, 0, 1),
		computeFixtureRow(0, "2026-10-01", 0, 0, 0, 0, 0, 0, 0, 1),
		computeFixtureRow(0, "2026-10-31", 0, 0, 1, 0, 0, 0, 0, 1),
	}}}}
	s := &Store{connection: c, now: time.Now}
	r, err := s.ComputeAnalytics(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	calendar := r.Results[0]
	if calendar.Value == nil || *calendar.Value != 1 || calendar.Contributors == nil || *calendar.Contributors != 1 || calendar.Unit != "installation" || len(calendar.Series) != 2 {
		t.Fatalf("calendar distinct: %+v", calendar)
	}
	if calendar.Series[0].Date != "2026-09-01" || calendar.Series[0].Value != nil || calendar.Series[0].Status != "no_data" || calendar.Series[1].Date != "2026-10-01" || *calendar.Series[1].Value != 1 || *calendar.Series[1].Contributors != 1 {
		t.Fatalf("monthly buckets: %+v", calendar.Series)
	}
	if *r.Results[1].Value != 7 {
		t.Fatal("calendar replaced rolling MAU")
	}
	sql, args := analyticsComputeSQL(q, "linka-type")
	for _, expected := range []string{"GROUPING SETS ((day), (month), ())", "uniqExactIf(subject_key, eligible AND occurred_at >= calendar_from AND occurred_at < calendar_to)", "occurred_at >= calendar_scan_from AND occurred_at < calendar_to"} {
		if !strings.Contains(sql, expected) {
			t.Errorf("calendar SQL missing %s", expected)
		}
	}
	if len(args) != strings.Count(sql, "?") || strings.Contains(sql, "sum(") {
		t.Fatal("calendar must use bound exact distinct")
	}
	q.Products = []string{"linka-tts"}
	q.Metrics = []string{"calendar_active"}
	r, err = s.ComputeAnalytics(context.Background(), q)
	if err != nil || r.Results[0].Status != "unavailable" || r.Results[0].Value != nil {
		t.Fatalf("TTS calendar reach: %+v %v", r, err)
	}
}

func TestAnalyticsComputeCalendarSeriesBudgetAndUnknown(t *testing.T) {
	q := analyticsTestQuery()
	q.CutoffDate, q.SeriesFrom, q.SeriesTo = "2026-11-01", "2025-10-30", "2026-10-29"
	q.Metrics = []string{"calendar_active"}
	c := &computeConn{responses: []*computeRows{{}}}
	s := &Store{connection: c, now: time.Now}
	r, err := s.ComputeAnalytics(context.Background(), q)
	if err != nil || len(r.Results) != 1 {
		t.Fatalf("calendar report: %+v %v", r, err)
	}
	result := r.Results[0]
	if result.Value != nil || result.Status != "no_data" || len(result.Series) != 13 || result.Series[0].Date != "2025-10-01" || result.Series[12].Date != "2026-10-01" {
		t.Fatalf("calendar series budget: %+v", result)
	}
	for _, point := range result.Series {
		if point.Value != nil || point.Contributors != nil || point.Status != "no_data" {
			t.Fatalf("unknown month became zero: %+v", point)
		}
	}
	q.SeriesFrom, q.SeriesTo = "2025-09-01", "2025-09-02"
	c.responses = []*computeRows{{}}
	r, err = s.ComputeAnalytics(context.Background(), q)
	if err != nil || r.Results[0].Status != "unavailable" || len(r.Results[0].Series) != 0 || r.Results[0].Reason != "calendar_series_range_exceeds_thirteen_month_budget" {
		t.Fatalf("oversized monthly series silently truncated: %+v %v", r, err)
	}
}

func TestAnalyticsComputeCalendarSourceExistingIntegration(t *testing.T) {
	address := os.Getenv("CLICKHOUSE_INTEGRATION_ADDR")
	if address != "localhost:19009" && address != "127.0.0.1:19009" {
		t.Skip("requires explicitly configured isolated localhost:19009 fixture; no DDL")
	}
	s, err := Open(Config{Addresses: []string{address}, Database: "linka_metric", Username: os.Getenv("CLICKHOUSE_INTEGRATION_USER"), Password: os.Getenv("CLICKHOUSE_INTEGRATION_PASSWORD")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q := analyticsTestQuery()
	q.CutoffDate, q.SeriesFrom, q.SeriesTo = "2026-11-01", "2026-09-15", "2026-11-01"
	q.Metrics, q.Products = []string{"calendar_active", "active_30d"}, []string{"linka-paperboard"}
	query, args := analyticsComputeSQL(q, "linka-paperboard")
	// Inline SQL facts verify the actual engine's month distinct without writing fixtures or DDL.
	fixture := `(SELECT tupleElement(fact, 1) AS subject_key, toUInt64(1) AS app_session_key,
		'fixture' AS app_build, toDateTime(tupleElement(fact, 2), 'UTC') AS occurred_at,
		'app_open' AS kind, 'linka-paperboard' AS product FROM (SELECT arrayJoin([
		(toUInt64(9), '2026-09-30 12:00:00'), (toUInt64(1), '2026-10-01 12:00:00'),
		(toUInt64(1), '2026-10-02 12:00:00'), (toUInt64(1), '2026-10-31 12:00:00'),
		(toUInt64(3), '2026-11-01 12:00:00')]) AS fact))`
	total, buckets, err := s.analyticsReadCounts(context.Background(), strings.ReplaceAll(query, "datalens_product_v2", fixture), args)
	if err != nil {
		t.Fatalf("bounded calendar fixture query: %v", err)
	}
	if total.calendarActive != 1 || total.active30 != 2 || buckets["month:2026-10-01"].calendarActive != 1 || buckets["month:2026-09-01"].calendarActive != 1 {
		t.Fatalf("engine must deduplicate one subject across days, not sum daily uniques: total=%+v buckets=%+v", total, buckets)
	}
	for _, product := range analyticsapi.Products() {
		q.Products = []string{product.ID}
		r, err := s.ComputeAnalytics(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range r.Results {
			if result.Reason == "source_query_failed" || result.Reason == "source_query_budget_exceeded" || result.Reason == "source_view_or_required_column_missing" {
				t.Fatalf("existing source query failed for %s: %+v", product.ID, result)
			}
		}
	}
}

func TestAnalyticsComputeReviewRegressions(t *testing.T) {
	q := analyticsTestQuery()
	q.Metrics = []string{"active_1d", "useful_actions", "sessions"}
	c := &computeConn{responses: []*computeRows{{rows: [][]any{
		computeFixtureRow(1, "1970-01-01", 1, 1, 1, 200, 2, 0, 0, 1, 1, 1),
		computeFixtureRow(0, "2026-11-06", 1, 1, 1, 200, 2, 0, 0, 1, 1, 1),
	}}}}
	s := &Store{connection: c, now: time.Now}
	r, err := s.ComputeAnalytics(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	for i, unit := range []string{"installation", "event", "session"} {
		result := r.Results[i]
		if result.Unit != unit || result.Contributors == nil || *result.Contributors != 1 {
			t.Fatalf("unit/contributors: %+v", result)
		}
	}
	useful := r.Results[1]
	if *useful.Value != 200 || *useful.Contributors != 1 || *useful.Series[5].Contributors != 1 {
		t.Fatalf("200 events must not imply 200 contributors: %+v", useful)
	}
	if *r.Results[0].Series[5].Contributors != 1 || r.Results[0].Series[0].Contributors != nil {
		t.Fatal("DAU contributor series incorrect")
	}
	q.Methodology = "account-v3"
	q.Metrics = []string{"active_1d"}
	r, err = s.ComputeAnalytics(context.Background(), q)
	if err != nil || r.Results[0].Unit != "product_account" || r.Capabilities[0].Unit != "product_account" {
		t.Fatalf("account wire unit: %+v %v", r, err)
	}
	sql, _ := analyticsComputeSQL(analyticsTestQuery(), "linka-type")
	for _, expected := range []string{"app_session_key, app_build, occurred_at", "NOT startsWith(app_build, 'import-')", "uniqExactIf(subject_key, useful AND occurred_at >= series_from AND occurred_at < series_to)", "uniqExactIf(subject_key, eligible AND occurred_at >= series_from AND occurred_at < series_to"} {
		if !strings.Contains(sql, expected) {
			t.Errorf("SQL missing %s", expected)
		}
	}
}

func TestAnalyticsComputeObservedTTSZeroAndOverflow(t *testing.T) {
	q := analyticsTestQuery()
	q.Products, q.Metrics = []string{"linka-tts"}, []string{"service_operations", "service_failed"}
	c := &computeConn{responses: []*computeRows{{rows: [][]any{computeFixtureRow(1, "1970-01-01", 0, 0, 0, 0, 0, 200, 0, 0)}}}}
	s := &Store{connection: c, now: time.Now}
	r, err := s.ComputeAnalytics(context.Background(), q)
	if err != nil || r.Results[1].Value == nil || *r.Results[1].Value != 0 || r.Results[1].Status != "ready" {
		t.Fatalf("observed zero failure: %+v %v", r, err)
	}
	for _, result := range r.Results {
		if result.Contributors != nil {
			t.Fatal("TTS must not report human contributors")
		}
	}
	for _, counts := range [][]uint64{
		{math.MaxUint64, 0, 0, 1, 0, 0, 0, math.MaxUint64, 1, 0},
		{1, 0, 0, math.MaxUint64, 0, 0, 0, 1, 1, 0},
		{1, 0, 0, 1, 0, 0, 0, 1, math.MaxUint64, 0},
	} {
		q.Products, q.Metrics = []string{"linka-type"}, []string{"active_1d", "useful_actions"}
		c.responses = []*computeRows{{rows: [][]any{computeFixtureRow(1, "1970-01-01", counts...), computeFixtureRow(0, "2026-11-06", counts...)}}}
		r, err = s.ComputeAnalytics(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, result := range r.Results {
			if result.Status == "unavailable" {
				found = true
				if result.Value != nil || result.Contributors != nil || result.Reason != "aggregate_count_overflow" {
					t.Fatalf("overflow: %+v", result)
				}
			}
			for _, point := range result.Series {
				if point.Value != nil && *point.Value < 0 {
					t.Fatalf("negative overflow point: %+v", point)
				}
			}
		}
		if !found {
			t.Fatal("overflow silently accepted")
		}
	}
}

func TestAnalyticsComputeWindowsAndMissingDays(t *testing.T) {
	q := analyticsTestQuery()
	q.Metrics = []string{"active_1d", "active_7d", "active_30d", "useful_actions", "sessions", "funnel_repeated", "service_operations"}
	c := &computeConn{responses: []*computeRows{{rows: [][]any{
		computeFixtureRow(1, "1970-01-01", 2, 3, 4, 5, 6, 0, 0, 4),
		computeFixtureRow(0, "2026-11-06", 2, 2, 2, 5, 6, 0, 0, 2),
	}}}}
	s := &Store{connection: c, now: func() time.Time { return time.Unix(1, 0) }}
	r, err := s.ComputeAnalytics(context.Background(), q)
	if err != nil || r.Status != "partial" || c.calls != 1 || len(r.Results) != len(q.Metrics) {
		t.Fatalf("report: %+v %v", r, err)
	}
	for i, days := range []int{1, 7, 30} {
		result := r.Results[i]
		if result.WindowTo.Format(time.RFC3339) != "2026-11-07T00:00:00Z" || result.WindowTo.Sub(result.WindowFrom) != time.Duration(days)*24*time.Hour || result.Value == nil || *result.Value != int64(i+2) {
			t.Fatalf("wrong exact window: %+v", result)
		}
	}
	points := r.Results[0].Series
	if len(points) != 6 || points[0].Value != nil || points[0].Status != "no_data" || points[5].Value == nil || *points[5].Value != 2 {
		t.Fatalf("missing day became zero: %+v", points)
	}
	if len(r.Results[1].Series) != 0 {
		t.Fatal("daily DAU must not be labelled WAU")
	}
	if r.DataAsOf.Format(time.RFC3339) != "2026-11-07T00:00:00Z" || r.QueryHash == "" || r.ContractVersion != 1 {
		t.Fatal("missing provenance")
	}
}

func TestAnalyticsComputeProductFailureAndEmptyAreNotZero(t *testing.T) {
	q := analyticsTestQuery()
	q.Products = []string{"linka-looks", "linka-type"}
	c := &computeConn{failFirst: true, responses: []*computeRows{{rows: [][]any{computeFixtureRow(1, "1970-01-01", 1, 1, 1, 0, 1, 0, 0, 1)}}}}
	s := &Store{connection: c, now: time.Now}
	r, err := s.ComputeAnalytics(context.Background(), q)
	if err != nil || r.Status != "partial" || r.Results[0].Value != nil || r.Results[1].Value == nil || strings.Contains(r.Results[0].Reason, "secret") {
		t.Fatalf("product error leaked or hid healthy product: %+v %v", r, err)
	}
	q.Products = []string{"linka-type"}
	c.responses = []*computeRows{{}}
	r, err = s.ComputeAnalytics(context.Background(), q)
	if err != nil || r.Status != "no_data" || r.Results[0].Value != nil {
		t.Fatalf("empty source: %+v %v", r, err)
	}
}

func TestAnalyticsComputeTTSOperationsOnly(t *testing.T) {
	q := analyticsTestQuery()
	q.Products = []string{"linka-tts"}
	q.Metrics = []string{"service_operations", "service_failed", "active_30d", "useful_actions", "sessions", "funnel_entered"}
	c := &computeConn{responses: []*computeRows{{rows: [][]any{computeFixtureRow(1, "1970-01-01", 0, 0, 0, 0, 0, 12, 3, 0)}}}}
	s := &Store{connection: c, now: time.Now}
	r, err := s.ComputeAnalytics(context.Background(), q)
	if err != nil || r.Status != "partial" || *r.Results[0].Value != 12 || *r.Results[1].Value != 3 {
		t.Fatalf("TTS operations: %+v %v", r, err)
	}
	for _, result := range r.Results[2:] {
		if result.Value != nil || result.Status != "unavailable" {
			t.Fatalf("TTS became reach: %+v", result)
		}
	}
}

func TestAnalyticsComputeSchemaGapAndBudget(t *testing.T) {
	for _, test := range []struct {
		code   int32
		reason string
	}{
		{47, "source_view_or_required_column_missing"},
		{60, "source_view_or_required_column_missing"},
		{241, "source_query_budget_exceeded"},
		{159, "source_query_budget_exceeded"},
	} {
		c := &computeConn{queryErr: &ch.Exception{Code: test.code, Message: "secret raw server details"}}
		s := &Store{connection: c, now: time.Now}
		r, err := s.ComputeAnalytics(context.Background(), analyticsTestQuery())
		if err != nil || r.Results[0].Reason != test.reason || r.Results[0].Status != "unavailable" || r.Results[0].Value != nil {
			t.Fatalf("code %d: %+v %v", test.code, r, err)
		}
	}
}

func TestAnalyticsComputeEmptySourcesIntegration(t *testing.T) {
	// Reuses the existing opt-in, isolated database helper; never the production database.
	s, ctx := retentionIntegrationStore(t)
	q := analyticsTestQuery()
	q.Metrics = []string{"active_1d", "active_7d", "active_30d", "useful_actions", "sessions", "service_operations", "service_failed"}
	for _, product := range analyticsapi.Products() {
		q.Products = []string{product.ID}
		r, err := s.ComputeAnalytics(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range r.Results {
			if result.Reason == "source_query_failed" || result.Reason == "source_query_budget_exceeded" || result.Reason == "source_view_or_required_column_missing" || result.Value != nil {
				t.Fatalf("empty source SQL failed or produced a fake zero for %s: %+v", product.ID, result)
			}
		}
	}
}
