package analyticsbuild

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/analyticsapi"
	"github.com/linkasu/linka.plays-metric/internal/ga4"
)

const ga4Population = "GA4 property-local source identifiers, not people; non-additive across dates, products and sources."

func DefaultProperties() map[string]string {
	return map[string]string{"linka-type": "151443791", "linka-pictures": "340729214", "linka-paperboard": "251389924", "linka-looks": "408073017"}
}

type GA4Computer struct {
	client     *ga4.Client
	properties map[string]string
}

func NewGA4Computer(client *ga4.Client, properties map[string]string) *GA4Computer {
	allowed := DefaultProperties()
	selected := make(map[string]string)
	for product, property := range properties {
		if expected, ok := allowed[product]; ok && property == expected {
			selected[product] = property
		}
	}
	return &GA4Computer{client: client, properties: selected}
}

func (c *GA4Computer) ComputeAnalytics(ctx context.Context, q analyticsapi.Query) (analyticsapi.Report, error) {
	hash, err := q.Hash()
	if err != nil {
		return analyticsapi.Report{}, err
	}
	report := analyticsapi.Report{ContractVersion: analyticsapi.Version, QueryHash: hash, GeneratedAt: time.Now().UTC(), Status: "ready", Results: []analyticsapi.Result{}, Capabilities: []analyticsapi.Capability{}, Insights: []analyticsapi.Insight{}}
	if q.Methodology != "" && q.Methodology != "ga4" {
		return report, nil
	}
	if len(q.Products)*len(q.Metrics) > 250 {
		return analyticsapi.Report{}, analyticsapi.ErrBudget
	}
	filters := make(map[string][]string)
	names := map[string]string{"platform": "platform", "app_version": "appVersion", "country": "country", "region": "region"}
	unsupported := len(q.Dimensions) != 0
	for name, values := range q.Filters {
		if native, ok := names[name]; ok {
			filters[native] = append([]string(nil), values...)
		} else {
			unsupported = true
		}
	}
	for _, product := range q.Products {
		property, ok := c.properties[product]
		if !ok {
			continue
		}
		capability := analyticsapi.Capability{Product: product, Source: "ga4-" + property, Methodology: "ga4", Unit: "property_identifier", Metrics: []string{"active_1d", "active_7d", "active_30d", "calendar_active", "sessions"}, Dimensions: []string{"date", "yearMonth", "platform", "appVersion", "country", "region"}, Status: "ready"}
		for _, metric := range q.Metrics {
			result := analyticsapi.Result{Product: product, Metric: metric, Source: capability.Source, Methodology: "ga4", Unit: "property_identifier", Status: "unavailable", PopulationNote: ga4Population}
			if metric == "sessions" {
				result.Unit = "session"
			}
			switch {
			case unsupported:
				result.Reason = "GA4 grouping and locale filters are unsupported"
				capability.Status, capability.Reason = "unavailable", result.Reason
			case metric != "active_1d" && metric != "active_7d" && metric != "active_30d" && metric != "calendar_active" && metric != "sessions":
				result.Reason = "GA4 aggregates cannot establish this metric or successful actions/funnels"
			case c.client == nil:
				result.Reason = "GA4 provider unavailable"
				capability.Status, capability.Reason = "unavailable", result.Reason
			default:
				if err := c.computeGA4Metric(ctx, property, q, filters, &result); err != nil {
					// Never surface provider error strings, URLs, payloads or credentials.
					result.Value, result.Contributors, result.Previous, result.Series = nil, nil, nil, nil
					result.Status, result.Reason = "unavailable", "GA4 source report unavailable or invalid"
					capability.Status, capability.Reason = "unavailable", result.Reason
				}
			}
			report.Results = append(report.Results, result)
		}
		report.Capabilities = append(report.Capabilities, capability)
	}
	return report, nil
}

func (c *GA4Computer) computeGA4Metric(ctx context.Context, property string, q analyticsapi.Query, filters map[string][]string, result *analyticsapi.Result) error {
	cutoff, _ := time.Parse(time.DateOnly, q.CutoffDate)
	days := map[string]int{"active_1d": 1, "active_7d": 7, "active_30d": 30}[result.Metric]
	request := ga4.Request{From: q.SeriesFrom, To: q.SeriesTo, Metrics: []string{"sessions", "totalUsers"}, Filters: filters}
	if days != 0 {
		request.From, request.To = cutoff.AddDate(0, 0, 1-days).Format(time.DateOnly), q.CutoffDate
		request.Metrics = []string{"activeUsers"}
	}
	if result.Metric == "calendar_active" {
		nextDay := cutoff.AddDate(0, 0, 1)
		calendarTo := time.Date(nextDay.Year(), nextDay.Month(), 1, 0, 0, 0, 0, nextDay.Location())
		request.From = calendarTo.AddDate(0, -1, 0).Format(time.DateOnly)
		request.To = calendarTo.AddDate(0, 0, -1).Format(time.DateOnly)
		request.Metrics = []string{"activeUsers"}
	}
	source, err := c.client.RunReport(ctx, property, request)
	if err != nil {
		return err
	}
	if err := setGA4Window(result, source, request); err != nil {
		return err
	}
	result.Value, result.Contributors, err = ga4Aggregate(source, len(request.Metrics))
	if err != nil {
		return err
	}
	result.Status = "no_data"
	if result.Value != nil {
		result.Status = "ready"
	}
	if days != 0 && result.Value != nil {
		previous := request
		previous.From = cutoff.AddDate(0, 0, 1-2*days).Format(time.DateOnly)
		previous.To = cutoff.AddDate(0, 0, -days).Format(time.DateOnly)
		prior, err := c.client.RunReport(ctx, property, previous)
		if err != nil {
			return err
		}
		if prior.Metadata.TimeZone != source.Metadata.TimeZone {
			return errors.New("GA4 source timezone changed")
		}
		if err := mergeGA4Quality(result, prior.Metadata); err != nil {
			return err
		}
		result.Previous, _, err = ga4Aggregate(prior, 1)
		if err != nil {
			return err
		}
	}
	if result.Metric == "active_1d" {
		daily := ga4.Request{From: q.SeriesFrom, To: q.SeriesTo, Metrics: []string{"activeUsers"}, Dimensions: []string{"date"}, Filters: filters}
		series, err := c.client.RunReport(ctx, property, daily)
		if err != nil {
			return err
		}
		if series.Metadata.TimeZone != source.Metadata.TimeZone {
			return errors.New("GA4 source timezone changed")
		}
		if err := mergeGA4Quality(result, series.Metadata); err != nil {
			return err
		}
		result.Series, err = ga4Series(series, q.SeriesFrom, q.SeriesTo, false)
		return err
	}
	if result.Metric == "calendar_active" {
		from, _ := time.ParseInLocation(time.DateOnly, q.SeriesFrom[:7]+"-01", result.WindowTo.Location())
		if (result.WindowTo.Year()-from.Year())*12+int(result.WindowTo.Month()-from.Month()) > 366 {
			return analyticsapi.ErrBudget
		}
		for from.Before(result.WindowTo) {
			// Whole-month annual chunks keep each native request within 366 days.
			to := from.AddDate(1, 0, 0)
			if to.After(result.WindowTo) {
				to = result.WindowTo
			}
			monthly := ga4.Request{From: from.Format(time.DateOnly), To: to.AddDate(0, 0, -1).Format(time.DateOnly), Metrics: []string{"activeUsers"}, Dimensions: []string{"yearMonth"}, Filters: filters}
			series, err := c.client.RunReport(ctx, property, monthly)
			if err != nil {
				return err
			}
			if series.Metadata.TimeZone != source.Metadata.TimeZone {
				return errors.New("GA4 source timezone changed")
			}
			if err := mergeGA4Quality(result, series.Metadata); err != nil {
				return err
			}
			points, err := ga4Series(series, monthly.From, monthly.To, true)
			if err != nil {
				return err
			}
			if len(result.Series)+len(points) > 366 {
				return analyticsapi.ErrBudget
			}
			result.Series = append(result.Series, points...)
			from = to
		}
	}
	return nil
}

func setGA4Window(result *analyticsapi.Result, source ga4.Report, request ga4.Request) error {
	location, err := time.LoadLocation(source.Metadata.TimeZone)
	if err != nil {
		return errors.New("invalid GA4 timezone")
	}
	result.Timezone = source.Metadata.TimeZone
	result.WindowFrom, err = time.ParseInLocation(time.DateOnly, request.From, location)
	if err != nil {
		return err
	}
	end, err := time.ParseInLocation(time.DateOnly, request.To, location)
	if err != nil {
		return err
	}
	result.WindowTo = end.AddDate(0, 0, 1)
	return mergeGA4Quality(result, source.Metadata)
}

func mergeGA4Quality(result *analyticsapi.Result, metadata ga4.Metadata) error {
	// The frozen result contract has no metadata field; retain quality in Reason.
	for _, sampling := range metadata.SamplingMetadatas {
		for _, count := range []string{sampling.SamplesReadCount, sampling.SamplingSpaceSize} {
			if _, err := strconv.ParseUint(count, 10, 64); err != nil {
				return errors.New("invalid GA4 sampling metadata")
			}
		}
	}
	if metadata.SubjectToThresholding || metadata.DataLossFromOtherRow || len(metadata.SamplingMetadatas) != 0 {
		result.Approximate = true
		quality := struct {
			Thresholding bool                   `json:"subjectToThresholding"`
			OtherRow     bool                   `json:"dataLossFromOtherRow"`
			Sampling     []ga4.SamplingMetadata `json:"samplingMetadatas,omitempty"`
		}{metadata.SubjectToThresholding, metadata.DataLossFromOtherRow, metadata.SamplingMetadatas}
		encoded, _ := json.Marshal(quality)
		if result.Reason != "" {
			result.Reason += "; "
		}
		result.Reason += "ga4_quality=" + string(encoded)
	}
	return nil
}

func ga4Integer(value string) (*int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return nil, errors.New("invalid GA4 integer")
	}
	return &n, nil
}

func ga4Aggregate(source ga4.Report, metrics int) (*int64, *int64, error) {
	if source.Empty || len(source.Rows) == 0 {
		return nil, nil, nil
	}
	if len(source.Rows) != 1 || len(source.Rows[0].MetricValues) != metrics {
		return nil, nil, errors.New("invalid GA4 aggregate")
	}
	value, err := ga4Integer(source.Rows[0].MetricValues[0].Value)
	if err != nil {
		return nil, nil, err
	}
	contributors := value
	if metrics == 2 {
		contributors, err = ga4Integer(source.Rows[0].MetricValues[1].Value)
	}
	return value, contributors, err
}

func ga4Series(source ga4.Report, from, to string, monthly bool) ([]analyticsapi.Point, error) {
	start, _ := time.Parse(time.DateOnly, from)
	end, _ := time.Parse(time.DateOnly, to)
	layout, months, days := "20060102", 0, 1
	if monthly {
		layout, months, days = "200601", 1, 0
	}
	values := make(map[string]*int64, len(source.Rows))
	if len(source.Rows) > 366 {
		return nil, analyticsapi.ErrBudget
	}
	for _, row := range source.Rows {
		if len(row.DimensionValues) != 1 || len(row.MetricValues) != 1 {
			return nil, errors.New("invalid GA4 series row")
		}
		date, err := time.Parse(layout, row.DimensionValues[0].Value)
		if err != nil || date.Before(start) || date.After(end) {
			return nil, errors.New("invalid GA4 series date")
		}
		key := date.Format(time.DateOnly)
		if _, exists := values[key]; exists {
			return nil, errors.New("duplicate GA4 series date")
		}
		value, err := ga4Integer(row.MetricValues[0].Value)
		if err != nil {
			return nil, err
		}
		values[key] = value
	}
	points := make([]analyticsapi.Point, 0, 366)
	for date := start; !date.After(end); date = date.AddDate(0, months, days) {
		if len(points) == 366 {
			return nil, analyticsapi.ErrBudget
		}
		point := analyticsapi.Point{Date: date.Format(time.DateOnly), Status: "no_data"}
		if value, ok := values[point.Date]; ok {
			point.Value, point.Contributors, point.Status = value, value, "ready"
		}
		points = append(points, point)
	}
	return points, nil
}
