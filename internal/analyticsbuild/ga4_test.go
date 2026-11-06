package analyticsbuild

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/analyticsapi"
	"github.com/linkasu/linka.plays-metric/internal/ga4"
)

type ga4Transport func(*http.Request) (*http.Response, error)

func (f ga4Transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type ga4WireQuery struct {
	DateRanges      []struct{ StartDate, EndDate string }
	Metrics         []struct{ Name string }
	Dimensions      []struct{ Name string }
	DimensionFilter struct {
		AndGroup struct {
			Expressions []struct {
				Filter struct {
					FieldName    string
					InListFilter struct {
						Values        []string
						CaseSensitive bool
					}
				}
			}
		}
	}
}

func ga4TestQuery(metrics ...string) analyticsapi.Query {
	return analyticsapi.Query{ContractVersion: 1, Products: []string{"linka-type"}, Metrics: metrics, CutoffDate: "2026-03-09", SeriesFrom: "2026-03-07", SeriesTo: "2026-03-09"}
}

func ga4FixtureClient(t *testing.T, fixture func(*http.Request, ga4WireQuery) (int, string)) *ga4.Client {
	t.Helper()
	return ga4.NewClient(func(context.Context) (string, error) { return "secret-token", nil }, &http.Client{Transport: ga4Transport(func(r *http.Request) (*http.Response, error) {
		var q ga4WireQuery
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Fatal(err)
		}
		code, body := fixture(r, q)
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
}

func ga4FixtureReport(q ga4WireQuery, values [][]string, dates []string, metadata ga4.Metadata) string {
	r := ga4.Report{Kind: "analyticsData#runReport", Metadata: metadata, RowCount: len(values)}
	for _, metric := range q.Metrics {
		r.MetricHeaders = append(r.MetricHeaders, ga4.MetricHeader{Name: metric.Name, Type: "TYPE_INTEGER"})
	}
	for _, dimension := range q.Dimensions {
		r.DimensionHeaders = append(r.DimensionHeaders, ga4.DimensionHeader{Name: dimension.Name})
	}
	for i, counts := range values {
		row := ga4.Row{}
		for _, count := range counts {
			row.MetricValues = append(row.MetricValues, ga4.Value{Value: count})
		}
		if len(dates) != 0 {
			row.DimensionValues = []ga4.Value{{Value: dates[i]}}
		}
		r.Rows = append(r.Rows, row)
	}
	b, _ := json.Marshal(r)
	return string(b)
}

func TestGA4WindowsDSTExactIntegersAndPrevious(t *testing.T) {
	var ranges [][2]string
	client := ga4FixtureClient(t, func(_ *http.Request, q ga4WireQuery) (int, string) {
		ranges = append(ranges, [2]string{q.DateRanges[0].StartDate, q.DateRanges[0].EndDate})
		return 200, ga4FixtureReport(q, [][]string{{"9007199254740993"}}, nil, ga4.Metadata{TimeZone: "America/New_York"})
	})
	report, err := NewGA4Computer(client, DefaultProperties()).ComputeAnalytics(context.Background(), ga4TestQuery("active_7d", "active_30d"))
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"2026-03-03", "2026-03-09"}, {"2026-02-24", "2026-03-02"}, {"2026-02-08", "2026-03-09"}, {"2026-01-09", "2026-02-07"}}
	if !reflect.DeepEqual(ranges, want) {
		t.Fatalf("ranges = %v", ranges)
	}
	for _, result := range report.Results {
		if result.Status != "ready" || result.Value == nil || *result.Value != 9007199254740993 || result.Previous == nil || *result.Previous != *result.Value || result.Contributors == nil || *result.Contributors != *result.Value {
			t.Fatalf("result = %+v", result)
		}
		if result.Source != "ga4-151443791" || result.Unit != "property_identifier" || result.Timezone != "America/New_York" || result.Methodology != "ga4" || !strings.Contains(result.PopulationNote, "not people") {
			t.Fatalf("identity = %+v", result)
		}
		if len(result.Series) != 0 {
			t.Fatal("WAU/MAU must not be daily sums")
		}
	}
	if got := report.Results[0].WindowTo.Sub(report.Results[0].WindowFrom); got != 167*time.Hour {
		t.Fatalf("DST week = %v", got)
	}
	if got := report.Results[1].WindowTo.Sub(report.Results[1].WindowFrom); got != 719*time.Hour {
		t.Fatalf("DST month = %v", got)
	}
}

func TestGA4SeriesSessionsFiltersAndQuality(t *testing.T) {
	var calls int
	metadata := ga4.Metadata{TimeZone: "America/New_York", SubjectToThresholding: true, DataLossFromOtherRow: true, SamplingMetadatas: []ga4.SamplingMetadata{{SamplesReadCount: "9007199254740993", SamplingSpaceSize: "9999999999999999"}}}
	client := ga4FixtureClient(t, func(_ *http.Request, q ga4WireQuery) (int, string) {
		calls++
		gotFilters := map[string][]string{}
		for _, expression := range q.DimensionFilter.AndGroup.Expressions {
			if expression.Filter.InListFilter.CaseSensitive {
				t.Fatal("filters must use native case-insensitive values")
			}
			gotFilters[expression.Filter.FieldName] = expression.Filter.InListFilter.Values
		}
		wantFilters := map[string][]string{"platform": {"ANDROID"}, "appVersion": {"1.2"}, "country": {"Germany"}, "region": {"Berlin"}}
		if !reflect.DeepEqual(gotFilters, wantFilters) {
			t.Fatalf("filters = %v", gotFilters)
		}
		if len(q.Dimensions) != 0 {
			if q.Dimensions[0].Name != "date" || q.Metrics[0].Name != "activeUsers" {
				t.Fatalf("daily = %+v", q)
			}
			return 200, ga4FixtureReport(q, [][]string{{"3"}, {"0"}}, []string{"20260307", "20260309"}, metadata)
		}
		if q.Metrics[0].Name == "sessions" {
			if len(q.Metrics) != 2 || q.Metrics[1].Name != "totalUsers" || q.DateRanges[0].StartDate != "2026-03-07" || q.DateRanges[0].EndDate != "2026-03-09" {
				t.Fatalf("sessions query = %+v", q)
			}
			return 200, ga4FixtureReport(q, [][]string{{"42", "7"}}, nil, metadata)
		}
		if q.DateRanges[0].StartDate != q.DateRanges[0].EndDate || (q.DateRanges[0].StartDate != "2026-03-09" && q.DateRanges[0].StartDate != "2026-03-08") {
			t.Fatalf("1d/previous window = %+v", q.DateRanges)
		}
		return 200, ga4FixtureReport(q, [][]string{{"0"}}, nil, metadata)
	})
	q := ga4TestQuery("active_1d", "sessions", "useful_actions", "funnel_activated")
	q.Filters = map[string][]string{"platform": {"ANDROID"}, "app_version": {"1.2"}, "country": {"Germany"}, "region": {"Berlin"}}
	report, err := NewGA4Computer(client, DefaultProperties()).ComputeAnalytics(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("calls = %d", calls)
	}
	active, sessions := report.Results[0], report.Results[1]
	if active.Value == nil || *active.Value != 0 || active.Status != "ready" || active.Previous == nil || *active.Previous != 0 {
		t.Fatalf("zero = %+v", active)
	}
	if len(active.Series) != 3 || active.Series[1].Value != nil || active.Series[1].Status != "no_data" || active.Series[2].Value == nil || *active.Series[2].Value != 0 || active.Series[2].Status != "ready" || active.Series[0].Contributors == nil || *active.Series[0].Contributors != 3 {
		t.Fatalf("series = %+v", active.Series)
	}
	if sessions.Unit != "session" || sessions.Value == nil || *sessions.Value != 42 || sessions.Contributors == nil || *sessions.Contributors != 7 || sessions.Previous != nil || sessions.WindowTo.Sub(sessions.WindowFrom) != 71*time.Hour {
		t.Fatalf("sessions = %+v", sessions)
	}
	for _, result := range report.Results[:2] {
		if !result.Approximate || !strings.Contains(result.Reason, `"subjectToThresholding":true`) || !strings.Contains(result.Reason, `"dataLossFromOtherRow":true`) || !strings.Contains(result.Reason, `"samplesReadCount":"9007199254740993"`) {
			t.Fatalf("quality = %+v", result)
		}
	}
	for _, result := range report.Results[2:] {
		if result.Value != nil || result.Contributors != nil || result.Previous != nil || result.Status != "unavailable" || result.Reason == "" {
			t.Fatalf("unsupported = %+v", result)
		}
	}
}

func TestGA4MetadataOnlyIsNotZero(t *testing.T) {
	client := ga4FixtureClient(t, func(_ *http.Request, _ ga4WireQuery) (int, string) {
		return 200, `{"kind":"analyticsData#runReport","metadata":{"timeZone":"Europe/Berlin","subjectToThresholding":true}}`
	})
	report, err := NewGA4Computer(client, DefaultProperties()).ComputeAnalytics(context.Background(), ga4TestQuery("active_1d", "active_7d", "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range report.Results {
		if result.Status != "no_data" || result.Value != nil || result.Contributors != nil || result.Previous != nil || !result.Approximate || result.Timezone != "Europe/Berlin" {
			t.Fatalf("empty = %+v", result)
		}
		for _, point := range result.Series {
			if point.Status != "no_data" || point.Value != nil || point.Contributors != nil {
				t.Fatalf("empty day = %+v", point)
			}
		}
	}
}

func TestGA4UnsupportedDimensionsAndOtherMethodology(t *testing.T) {
	client := ga4FixtureClient(t, func(_ *http.Request, _ ga4WireQuery) (int, string) {
		t.Fatal("unsupported queries must never call GA4")
		return 0, ""
	})
	computer := NewGA4Computer(client, DefaultProperties())
	for _, mode := range []string{"locale", "dimensions", "methodology"} {
		t.Run(mode, func(t *testing.T) {
			q := ga4TestQuery("active_7d", "sessions")
			switch mode {
			case "locale":
				q.Filters = map[string][]string{"locale": {"ru"}}
			case "dimensions":
				q.Dimensions = []string{"country"}
			case "methodology":
				q.Methodology = "account-v3"
			}
			report, err := computer.ComputeAnalytics(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "methodology" {
				if len(report.Results) != 0 || len(report.Capabilities) != 0 {
					t.Fatalf("other source = %+v", report)
				}
				return
			}
			if report.Capabilities[0].Status != "unavailable" {
				t.Fatalf("capabilities = %+v", report.Capabilities)
			}
			for _, result := range report.Results {
				if result.Status != "unavailable" || result.Value != nil || result.Reason == "" {
					t.Fatalf("unsupported = %+v", result)
				}
			}
		})
	}
}

func TestGA4FailureIsolationAndNoPII(t *testing.T) {
	client := ga4FixtureClient(t, func(r *http.Request, q ga4WireQuery) (int, string) {
		if strings.Contains(r.URL.Path, "151443791") {
			return 403, `{"error":"secret-token alice@example.org rawUserId=abc"}`
		}
		return 200, ga4FixtureReport(q, [][]string{{"9"}}, nil, ga4.Metadata{TimeZone: "UTC"})
	})
	q := ga4TestQuery("active_7d")
	q.Products = []string{"linka-type", "linka-pictures", "linka-plays", "linka-tts"}
	report, err := NewGA4Computer(client, DefaultProperties()).ComputeAnalytics(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 2 || report.Results[0].Status != "unavailable" || report.Results[0].Value != nil || report.Results[1].Status != "ready" || report.Results[1].Value == nil || *report.Results[1].Value != 9 || report.Capabilities[0].Status != "unavailable" {
		t.Fatalf("isolation = %+v", report)
	}
	body, _ := json.Marshal(report)
	for _, secret := range []string{"secret-token", "alice@", "rawUserId", "googleapis"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
}

func TestGA4InvalidIntegerAndSourceData(t *testing.T) {
	for _, value := range []string{"-1", "1.0", "1e3", "9223372036854775808"} {
		t.Run(value, func(t *testing.T) {
			client := ga4FixtureClient(t, func(_ *http.Request, q ga4WireQuery) (int, string) {
				return 200, ga4FixtureReport(q, [][]string{{value}}, nil, ga4.Metadata{TimeZone: "UTC"})
			})
			report, err := NewGA4Computer(client, DefaultProperties()).ComputeAnalytics(context.Background(), ga4TestQuery("active_7d"))
			if err != nil {
				t.Fatal(err)
			}
			if report.Results[0].Status != "unavailable" || report.Results[0].Value != nil || report.Results[0].Previous != nil {
				t.Fatalf("invalid = %+v", report.Results[0])
			}
		})
	}
}

func TestGA4DefaultsAndLimits(t *testing.T) {
	defaults := DefaultProperties()
	defaults["linka-type"] = "999"
	if DefaultProperties()["linka-type"] != "151443791" {
		t.Fatal("mutable defaults")
	}
	computer := NewGA4Computer(nil, map[string]string{"linka-type": "999", "linka-plays": "151443791"})
	report, err := computer.ComputeAnalytics(context.Background(), ga4TestQuery("active_7d"))
	if err != nil || len(report.Results) != 0 {
		t.Fatalf("unapproved property: %+v %v", report, err)
	}
	q := ga4TestQuery("active_7d")
	q.SeriesFrom, q.SeriesTo, q.CutoffDate = "2025-01-01", "2026-01-02", "2026-01-02"
	if _, err := computer.ComputeAnalytics(context.Background(), q); err == nil {
		t.Fatal("unbounded series accepted")
	}
}

func TestGA4UntrustedSourceMetadataAndDates(t *testing.T) {
	for _, mode := range []string{"timezone", "sampling", "duplicate_date", "outside_date", "previous_negative"} {
		t.Run(mode, func(t *testing.T) {
			client := ga4FixtureClient(t, func(_ *http.Request, q ga4WireQuery) (int, string) {
				metadata := ga4.Metadata{TimeZone: "UTC"}
				if mode == "sampling" {
					metadata.SamplingMetadatas = []ga4.SamplingMetadata{{SamplesReadCount: "secret alice@example.org", SamplingSpaceSize: "100"}}
				}
				if len(q.Dimensions) != 0 {
					switch mode {
					case "duplicate_date":
						return 200, ga4FixtureReport(q, [][]string{{"4"}, {"5"}}, []string{"20260307", "20260307"}, metadata)
					case "outside_date":
						return 200, ga4FixtureReport(q, [][]string{{"4"}}, []string{"20260306"}, metadata)
					}
				}
				if q.DateRanges[0].StartDate == "2026-03-08" {
					if mode == "timezone" {
						metadata.TimeZone = "Europe/Berlin"
					}
					if mode == "previous_negative" {
						return 200, ga4FixtureReport(q, [][]string{{"-1"}}, nil, metadata)
					}
				}
				return 200, ga4FixtureReport(q, [][]string{{"4"}}, nil, metadata)
			})
			report, err := NewGA4Computer(client, DefaultProperties()).ComputeAnalytics(context.Background(), ga4TestQuery("active_1d"))
			if err != nil {
				t.Fatal(err)
			}
			if report.Results[0].Value != nil || report.Results[0].Previous != nil || report.Results[0].Status != "unavailable" {
				t.Fatalf("untrusted source accepted: %+v", report.Results[0])
			}
			body, _ := json.Marshal(report)
			if strings.Contains(string(body), "alice") || strings.Contains(string(body), "secret") {
				t.Fatal("source metadata leaked")
			}
		})
	}
}

func TestGA4CalendarActiveWindows(t *testing.T) {
	for _, tc := range []struct {
		cutoff, from, to string
		hours            time.Duration
	}{
		{"2026-03-31", "2026-03-01", "2026-03-31", 743 * time.Hour},
		{"2026-04-15", "2026-03-01", "2026-03-31", 743 * time.Hour},
		{"2024-02-29", "2024-02-01", "2024-02-29", 696 * time.Hour},
		{"2025-02-28", "2025-02-01", "2025-02-28", 672 * time.Hour},
		{"2026-12-31", "2026-12-01", "2026-12-31", 744 * time.Hour},
		{"2027-01-01", "2026-12-01", "2026-12-31", 744 * time.Hour},
	} {
		t.Run(tc.cutoff, func(t *testing.T) {
			var requests []ga4WireQuery
			q := ga4TestQuery("calendar_active")
			q.CutoffDate, q.SeriesFrom, q.SeriesTo = tc.cutoff, tc.cutoff[:7]+"-01", tc.cutoff
			// The series starts after the last complete month except at month-end.
			if tc.cutoff == tc.to {
				q.SeriesFrom = tc.from
			}
			client := ga4FixtureClient(t, func(_ *http.Request, query ga4WireQuery) (int, string) {
				requests = append(requests, query)
				if query.DateRanges[0].StartDate != tc.from || query.DateRanges[0].EndDate != tc.to || len(query.Metrics) != 1 || query.Metrics[0].Name != "activeUsers" {
					t.Fatalf("calendar query = %+v", query)
				}
				if len(query.Dimensions) != 0 {
					if query.Dimensions[0].Name != "yearMonth" {
						t.Fatalf("dimension = %+v", query.Dimensions)
					}
					return 200, ga4FixtureReport(query, [][]string{{"9007199254740993"}}, []string{strings.ReplaceAll(tc.from[:7], "-", "")}, ga4.Metadata{TimeZone: "America/New_York"})
				}
				return 200, ga4FixtureReport(query, [][]string{{"9007199254740993"}}, nil, ga4.Metadata{TimeZone: "America/New_York"})
			})
			report, err := NewGA4Computer(client, DefaultProperties()).ComputeAnalytics(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			r := report.Results[0]
			if r.Status != "ready" || r.Value == nil || *r.Value != 9007199254740993 || r.Contributors == nil || *r.Contributors != *r.Value || r.Previous != nil || r.Unit != "property_identifier" || r.Timezone != "America/New_York" {
				t.Fatalf("calendar card = %+v", r)
			}
			if r.WindowFrom.Format(time.DateOnly) != tc.from || r.WindowTo.AddDate(0, 0, -1).Format(time.DateOnly) != tc.to || r.WindowTo.Sub(r.WindowFrom) != tc.hours {
				t.Fatalf("calendar window = %v .. %v", r.WindowFrom, r.WindowTo)
			}
			if tc.cutoff != tc.to && (len(r.Series) != 0 || len(requests) != 1) {
				t.Fatalf("partial month series = %+v, requests = %d", r.Series, len(requests))
			}
		})
	}
}

func TestGA4CalendarSeriesNativeDistinctAndNoData(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(strconv.FormatBool(empty), func(t *testing.T) {
			var calls int
			client := ga4FixtureClient(t, func(_ *http.Request, query ga4WireQuery) (int, string) {
				calls++
				if len(query.DimensionFilter.AndGroup.Expressions) != 1 || query.DimensionFilter.AndGroup.Expressions[0].Filter.FieldName != "platform" {
					t.Fatalf("calendar filters = %+v", query)
				}
				if empty {
					return 200, `{"kind":"analyticsData#runReport","metadata":{"timeZone":"UTC","subjectToThresholding":true}}`
				}
				metadata := ga4.Metadata{TimeZone: "UTC", SubjectToThresholding: true, SamplingMetadatas: []ga4.SamplingMetadata{{SamplesReadCount: "5", SamplingSpaceSize: "10"}}}
				if len(query.Dimensions) == 0 {
					if query.DateRanges[0].StartDate != "2026-03-01" || query.DateRanges[0].EndDate != "2026-03-31" {
						t.Fatalf("card window = %+v", query)
					}
					return 200, ga4FixtureReport(query, [][]string{{"2"}}, nil, metadata)
				}
				if query.Dimensions[0].Name != "yearMonth" || query.DateRanges[0].StartDate != "2025-12-01" || query.DateRanges[0].EndDate != "2026-03-31" {
					t.Fatalf("monthly query = %+v", query)
				}
				return 200, ga4FixtureReport(query, [][]string{{"2"}, {"2"}, {"0"}}, []string{"202512", "202601", "202603"}, metadata)
			})
			q := ga4TestQuery("calendar_active")
			q.CutoffDate, q.SeriesFrom, q.SeriesTo = "2026-04-15", "2025-12-17", "2026-04-15"
			q.Filters = map[string][]string{"platform": {"ANDROID"}}
			report, err := NewGA4Computer(client, DefaultProperties()).ComputeAnalytics(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			r := report.Results[0]
			if calls != 2 || !r.Approximate || r.Previous != nil || len(r.Series) != 4 || r.Series[0].Date != "2025-12-01" || r.Series[3].Date != "2026-03-01" {
				t.Fatalf("calendar series = %+v, calls = %d", r, calls)
			}
			if empty {
				if r.Status != "no_data" || r.Value != nil || r.Contributors != nil {
					t.Fatalf("empty card = %+v", r)
				}
				for _, point := range r.Series {
					if point.Value != nil || point.Contributors != nil || point.Status != "no_data" {
						t.Fatalf("empty month = %+v", point)
					}
				}
				return
			}
			if r.Value == nil || *r.Value != 2 || r.Series[0].Value == nil || *r.Series[0].Value != 2 || r.Series[0].Contributors == nil || *r.Series[0].Contributors != 2 || r.Series[1].Value == nil || *r.Series[1].Value != 2 || r.Series[2].Value != nil || r.Series[2].Status != "no_data" || r.Series[3].Value == nil || *r.Series[3].Value != 0 || r.Series[3].Status != "ready" {
				t.Fatalf("distinct monthly values = %+v", r)
			}
		})
	}
}

func TestGA4CalendarLeapYearSeriesBoundedRequests(t *testing.T) {
	var ranges [][2]string
	client := ga4FixtureClient(t, func(_ *http.Request, q ga4WireQuery) (int, string) {
		ranges = append(ranges, [2]string{q.DateRanges[0].StartDate, q.DateRanges[0].EndDate})
		if len(q.Dimensions) == 0 {
			return 200, ga4FixtureReport(q, [][]string{{"2"}}, nil, ga4.Metadata{TimeZone: "UTC"})
		}
		start, _ := time.Parse(time.DateOnly, q.DateRanges[0].StartDate)
		end, _ := time.Parse(time.DateOnly, q.DateRanges[0].EndDate)
		var values [][]string
		var months []string
		for date := start; !date.After(end); date = date.AddDate(0, 1, 0) {
			values = append(values, []string{"2"})
			months = append(months, date.Format("200601"))
		}
		return 200, ga4FixtureReport(q, values, months, ga4.Metadata{TimeZone: "UTC"})
	})
	q := ga4TestQuery("calendar_active")
	q.SeriesFrom, q.SeriesTo, q.CutoffDate = "2024-02-29", "2025-02-28", "2025-02-28"
	report, err := NewGA4Computer(client, DefaultProperties()).ComputeAnalytics(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"2025-02-01", "2025-02-28"}, {"2024-02-01", "2025-01-31"}, {"2025-02-01", "2025-02-28"}}
	if !reflect.DeepEqual(ranges, want) {
		t.Fatalf("bounded monthly requests = %v", ranges)
	}
	r := report.Results[0]
	if r.Status != "ready" || r.Value == nil || *r.Value != 2 || r.Previous != nil || len(r.Series) != 13 {
		t.Fatalf("leap series = %+v", r)
	}
	for _, point := range r.Series {
		if point.Value == nil || *point.Value != 2 || point.Contributors == nil || *point.Contributors != 2 {
			t.Fatalf("native monthly distinct = %+v", point)
		}
	}
}

func TestGA4CalendarRejectsInvalidMonths(t *testing.T) {
	for _, mode := range []string{"202600", "202613", "2026-01", "20260201", "202512", "202604", "duplicate", "timezone"} {
		t.Run(mode, func(t *testing.T) {
			client := ga4FixtureClient(t, func(_ *http.Request, query ga4WireQuery) (int, string) {
				metadata := ga4.Metadata{TimeZone: "UTC"}
				if len(query.Dimensions) == 0 {
					return 200, ga4FixtureReport(query, [][]string{{"2"}}, nil, metadata)
				}
				if mode == "timezone" {
					metadata.TimeZone = "Europe/Berlin"
				}
				dates, values := []string{mode}, [][]string{{"2"}}
				if mode == "duplicate" {
					dates, values = []string{"202601", "202601"}, [][]string{{"2"}, {"2"}}
				}
				if mode == "timezone" {
					dates = []string{"202601"}
				}
				return 200, ga4FixtureReport(query, values, dates, metadata)
			})
			q := ga4TestQuery("calendar_active")
			q.SeriesFrom, q.SeriesTo, q.CutoffDate = "2026-01-15", "2026-04-15", "2026-04-15"
			report, err := NewGA4Computer(client, DefaultProperties()).ComputeAnalytics(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			if r := report.Results[0]; r.Status != "unavailable" || r.Value != nil || r.Previous != nil || len(r.Series) != 0 {
				t.Fatalf("invalid month accepted: %+v", r)
			}
		})
	}
}
