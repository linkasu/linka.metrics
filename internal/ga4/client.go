// Package ga4 reads bounded, source-native GA4 aggregate reports. It does not
// infer individual events or combine distinct-user counts across rows.
package ga4

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	endpoint     = "https://analyticsdata.googleapis.com/v1beta/properties/"
	pageSize     = 10000
	maxRows      = 100000
	maxPageBytes = 4 << 20
)

// ErrIncomplete means the source response cannot establish a complete report.
// RunReport never returns partial data alongside an error.
var ErrIncomplete = errors.New("ga4: incomplete report")

type Request struct {
	From, To            string
	Metrics, Dimensions []string
	// Filters use source-native values, independently of the displayed dimensions.
	Filters map[string][]string
}

type DimensionHeader struct {
	Name string `json:"name"`
}
type MetricHeader struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
type Value struct {
	Value string `json:"value"`
}

// UnmarshalJSON distinguishes a missing value from a valid empty dimension.
func (v *Value) UnmarshalJSON(data []byte) error {
	var wire struct {
		Value *string `json:"value"`
	}
	if err := json.Unmarshal(data, &wire); err != nil || wire.Value == nil {
		return errors.New("ga4: missing or invalid value")
	}
	v.Value = *wire.Value
	return nil
}

type Row struct {
	DimensionValues []Value `json:"dimensionValues"`
	MetricValues    []Value `json:"metricValues"`
}
type Metadata struct {
	CurrencyCode          string             `json:"currencyCode,omitempty"`
	TimeZone              string             `json:"timeZone"`
	SubjectToThresholding bool               `json:"subjectToThresholding"`
	DataLossFromOtherRow  bool               `json:"dataLossFromOtherRow"`
	SamplingMetadatas     []SamplingMetadata `json:"samplingMetadatas,omitempty"`
}

// SamplingMetadata preserves the source's int64 JSON strings without float conversion.
// Entries correspond to requested date ranges in source order.
type SamplingMetadata struct {
	SamplesReadCount  string `json:"samplesReadCount"`
	SamplingSpaceSize string `json:"samplingSpaceSize"`
}

// Report preserves source columns and values. Empty marks a verified headerless
// no-data response, not a measured zero metric; no columns or values are invented.
type Report struct {
	Kind             string            `json:"kind,omitempty"`
	Empty            bool              `json:"empty,omitempty"`
	DimensionHeaders []DimensionHeader `json:"dimensionHeaders,omitempty"`
	MetricHeaders    []MetricHeader    `json:"metricHeaders,omitempty"`
	Rows             []Row             `json:"rows"`
	RowCount         int               `json:"rowCount"`
	Metadata         Metadata          `json:"metadata"`
}

// HTTPError contains no provider body, URL, or credential material.
type HTTPError struct {
	StatusCode int
	Reason     string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("ga4: HTTP %d (%s)", e.StatusCode, e.Reason) }

type Client struct {
	token      func(context.Context) (string, error)
	httpClient *http.Client
}

// NewClient copies the supplied client, preserving its transport but preventing
// redirects (including same-host redirects) and unbounded request durations.
func NewClient(token func(context.Context) (string, error), httpClient *http.Client) *Client {
	c := http.Client{}
	if httpClient != nil {
		c = *httpClient
	}
	if c.Timeout <= 0 || c.Timeout > 20*time.Second {
		c.Timeout = 20 * time.Second
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// Cookies are not part of the Data API authentication contract.
	c.Jar = nil
	return &Client{token: token, httpClient: &c}
}

var propertyPattern = regexp.MustCompile(`^[0-9]{1,20}$`)
var numericPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func validateRequest(property string, r Request) error {
	if !propertyPattern.MatchString(property) {
		return errors.New("ga4: invalid property ID")
	}
	from, err := time.Parse("2006-01-02", r.From)
	if err != nil || from.Year() < 1 || from.Format("2006-01-02") != r.From {
		return errors.New("ga4: invalid From date")
	}
	to, err := time.Parse("2006-01-02", r.To)
	if err != nil || to.Year() < 1 || to.Format("2006-01-02") != r.To {
		return errors.New("ga4: invalid To date")
	}
	if to.Before(from) || to.After(from.AddDate(0, 0, 365)) {
		return errors.New("ga4: date range must contain 1..366 days")
	}
	if len(r.Metrics) == 0 {
		return errors.New("ga4: at least one metric is required")
	}
	if err := validateNames(r.Metrics, "activeUsers,totalUsers,newUsers,eventCount,sessions,engagedSessions,screenPageViews,userEngagementDuration"); err != nil {
		return err
	}
	if err := validateNames(r.Dimensions, "date,yearMonth,country,region,platform,appVersion,eventName"); err != nil {
		return err
	}
	if len(r.Filters) > 4 {
		return errors.New("ga4: too many filters")
	}
	for name, values := range r.Filters {
		switch name {
		case "platform", "appVersion", "country", "region":
		default:
			return errors.New("ga4: unsupported filter")
		}
		if len(values) < 1 || len(values) > 8 {
			return errors.New("ga4: filter must contain 1..8 values")
		}
		for i, value := range values {
			if value == "" || len(value) > 100 || !utf8.ValidString(value) || strings.TrimSpace(value) != value ||
				strings.Contains(value, "@") || strings.ContainsFunc(value, unicode.IsControl) {
				return errors.New("ga4: invalid filter value")
			}
			for _, previous := range values[:i] {
				if strings.EqualFold(value, previous) {
					return errors.New("ga4: duplicate filter value")
				}
			}
		}
	}
	return nil
}

func validateNames(names []string, allowed string) error {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] || name == "" || !strings.Contains(","+allowed+",", ","+name+",") || strings.Contains(name, ",") {
			return errors.New("ga4: unsupported or duplicate column")
		}
		seen[name] = true
	}
	return nil
}

type namedColumn struct {
	Name string `json:"name"`
}
type orderBy struct {
	Dimension struct {
		DimensionName string `json:"dimensionName"`
	} `json:"dimension"`
}
type reportQuery struct {
	DateRanges      []dateRange      `json:"dateRanges"`
	Metrics         []namedColumn    `json:"metrics"`
	Dimensions      []namedColumn    `json:"dimensions,omitempty"`
	Limit           string           `json:"limit"`
	Offset          string           `json:"offset"`
	OrderBys        []orderBy        `json:"orderBys,omitempty"`
	DimensionFilter *dimensionFilter `json:"dimensionFilter,omitempty"`
}
type dimensionFilter struct {
	AndGroup struct {
		Expressions []filterExpression `json:"expressions"`
	} `json:"andGroup"`
}
type filterExpression struct {
	Filter struct {
		FieldName    string `json:"fieldName"`
		InListFilter struct {
			Values        []string `json:"values"`
			CaseSensitive bool     `json:"caseSensitive"`
		} `json:"inListFilter"`
	} `json:"filter"`
}
type dateRange struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

func (c *Client) RunReport(ctx context.Context, property string, request Request) (Report, error) {
	if err := validateRequest(property, request); err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	q := reportQuery{DateRanges: []dateRange{{request.From, request.To}}, Limit: strconv.Itoa(pageSize)}
	for _, name := range request.Metrics {
		q.Metrics = append(q.Metrics, namedColumn{name})
	}
	for _, name := range request.Dimensions {
		q.Dimensions = append(q.Dimensions, namedColumn{name})
		var order orderBy
		order.Dimension.DimensionName = name
		q.OrderBys = append(q.OrderBys, order)
	}
	if len(request.Filters) != 0 {
		keys := make([]string, 0, len(request.Filters))
		for name := range request.Filters {
			keys = append(keys, name)
		}
		slices.Sort(keys)
		q.DimensionFilter = &dimensionFilter{}
		for _, name := range keys {
			var expression filterExpression
			expression.Filter.FieldName = name
			expression.Filter.InListFilter.Values = request.Filters[name]
			q.DimensionFilter.AndGroup.Expressions = append(q.DimensionFilter.AndGroup.Expressions, expression)
		}
	}
	var report Report
	for offset := 0; ; offset += pageSize {
		q.Offset = strconv.Itoa(offset)
		page, err := c.readPage(ctx, property, q)
		if err != nil {
			return Report{}, err
		}
		if err := validatePage(page, request); err != nil {
			return Report{}, err
		}
		if offset == 0 {
			report = page
			report.Rows = nil
		} else {
			if page.RowCount != report.RowCount || !sameMetadata(page.Metadata, report.Metadata) || !sameMetricHeaders(page.MetricHeaders, report.MetricHeaders) {
				return Report{}, fmt.Errorf("%w: source changed between pages", ErrIncomplete)
			}
		}
		remaining := report.RowCount - offset
		want := min(pageSize, remaining)
		if remaining < 0 || len(page.Rows) != want {
			return Report{}, fmt.Errorf("%w: page row count mismatch", ErrIncomplete)
		}
		report.Rows = append(report.Rows, page.Rows...)
		if len(report.Rows) == report.RowCount {
			return report, nil
		}
	}
}

func sameMetadata(a, b Metadata) bool {
	return a.CurrencyCode == b.CurrencyCode && a.TimeZone == b.TimeZone &&
		a.SubjectToThresholding == b.SubjectToThresholding &&
		a.DataLossFromOtherRow == b.DataLossFromOtherRow &&
		slices.Equal(a.SamplingMetadatas, b.SamplingMetadatas)
}

func sameMetricHeaders(a, b []MetricHeader) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (c *Client) readPage(ctx context.Context, property string, q reportQuery) (Report, error) {
	if c.token == nil {
		return Report{}, errors.New("ga4: token provider is required")
	}
	token, err := c.token(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		return Report{}, errors.New("ga4: token provider failed")
	}
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return Report{}, errors.New("ga4: invalid access token")
	}
	body, err := json.Marshal(q)
	if err != nil {
		return Report{}, errors.New("ga4: cannot encode request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+property+":runReport", bytes.NewReader(body))
	if err != nil {
		return Report{}, errors.New("ga4: cannot create request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		return Report{}, errors.New("ga4: HTTP transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		reason := http.StatusText(resp.StatusCode)
		if reason == "" {
			reason = "unexpected status"
		}
		return Report{}, &HTTPError{StatusCode: resp.StatusCode, Reason: reason}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil || len(data) > maxPageBytes {
		return Report{}, fmt.Errorf("%w: unreadable or oversized page", ErrIncomplete)
	}
	// Protobuf JSON may omit a default zero count; explicit null is not a count.
	var wire struct {
		Report
		RowCount         json.RawMessage `json:"rowCount"`
		DimensionHeaders json.RawMessage `json:"dimensionHeaders"`
		MetricHeaders    json.RawMessage `json:"metricHeaders"`
	}
	if json.Unmarshal(data, &wire) != nil {
		return Report{}, fmt.Errorf("%w: invalid response schema", ErrIncomplete)
	}
	if (len(wire.DimensionHeaders) != 0 && json.Unmarshal(wire.DimensionHeaders, &wire.Report.DimensionHeaders) != nil) ||
		(len(wire.MetricHeaders) != 0 && json.Unmarshal(wire.MetricHeaders, &wire.Report.MetricHeaders) != nil) {
		return Report{}, fmt.Errorf("%w: invalid response schema", ErrIncomplete)
	}
	if len(wire.RowCount) == 0 {
		// Source-empty reports and column schemas are checked by validatePage.
		if len(wire.Rows) != 0 {
			return Report{}, fmt.Errorf("%w: missing row count for nonempty page", ErrIncomplete)
		}
	} else if bytes.Equal(bytes.TrimSpace(wire.RowCount), []byte("null")) || json.Unmarshal(wire.RowCount, &wire.Report.RowCount) != nil {
		return Report{}, fmt.Errorf("%w: invalid row count", ErrIncomplete)
	}
	// Do not trust a provider-supplied "empty" field or null/empty headers.
	wire.Report.Empty = wire.Kind == "analyticsData#runReport" && len(wire.Rows) == 0 && wire.Report.RowCount == 0 &&
		len(wire.DimensionHeaders) == 0 && len(wire.MetricHeaders) == 0
	return wire.Report, nil
}

func validatePage(p Report, request Request) error {
	invalid := func() error { return fmt.Errorf("%w: inconsistent response schema", ErrIncomplete) }
	if p.RowCount < 0 || p.RowCount > maxRows || len(p.Rows) > pageSize || p.Metadata.TimeZone == "" || len(p.Metadata.TimeZone) > 128 {
		return invalid()
	}
	if p.Empty {
		if _, err := time.LoadLocation(p.Metadata.TimeZone); err != nil {
			return invalid()
		}
		return nil
	}
	if len(p.DimensionHeaders) != len(request.Dimensions) || len(p.MetricHeaders) != len(request.Metrics) {
		return invalid()
	}
	for i, h := range p.DimensionHeaders {
		if h.Name != request.Dimensions[i] {
			return invalid()
		}
	}
	for i, h := range p.MetricHeaders {
		if h.Name != request.Metrics[i] {
			return invalid()
		}
		switch h.Type {
		case "TYPE_INTEGER", "TYPE_FLOAT", "TYPE_SECONDS", "TYPE_MILLISECONDS", "TYPE_MINUTES", "TYPE_HOURS", "TYPE_STANDARD", "TYPE_CURRENCY", "TYPE_FEET", "TYPE_MILES", "TYPE_METERS", "TYPE_KILOMETERS":
		default:
			return invalid()
		}
	}
	for _, row := range p.Rows {
		if len(row.DimensionValues) != len(request.Dimensions) || len(row.MetricValues) != len(request.Metrics) {
			return invalid()
		}
		for _, v := range row.MetricValues {
			if len(v.Value) > 128 || !numericPattern.MatchString(v.Value) {
				return invalid()
			}
			n, err := strconv.ParseFloat(v.Value, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
				return invalid()
			}
		}
	}
	return nil
}
