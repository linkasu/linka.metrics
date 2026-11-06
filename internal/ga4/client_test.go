package ga4

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

const validPage = `{"dimensionHeaders":[{"name":"date"}],"metricHeaders":[{"name":"activeUsers","type":"TYPE_INTEGER"}],"rows":[{"dimensionValues":[{"value":"20260101"}],"metricValues":[{"value":"42"}]}],"rowCount":1,"metadata":{"timeZone":"Europe/Moscow","subjectToThresholding":true,"dataLossFromOtherRow":true},"futureField":{}}`

func request() Request {
	return Request{From: "2026-01-01", To: "2026-01-02", Metrics: []string{"activeUsers"}, Dimensions: []string{"date"}}
}

func clientFor(f roundTripFunc) *Client {
	return NewClient(func(context.Context) (string, error) { return "secret-token", nil }, &http.Client{Transport: f})
}

func TestRunReportRequestAndMetadata(t *testing.T) {
	calls := 0
	c := clientFor(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.String() != "https://analyticsdata.googleapis.com/v1beta/properties/123:runReport" || r.Header.Get("Authorization") != "Bearer secret-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request: method=%s url=%s", r.Method, r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		want := `{"dateRanges":[{"startDate":"2026-01-01","endDate":"2026-01-02"}],"metrics":[{"name":"activeUsers"}],"dimensions":[{"name":"date"}],"limit":"10000","offset":"0","orderBys":[{"dimension":{"dimensionName":"date"}}]}`
		var gotJSON, wantJSON any
		if json.Unmarshal(body, &gotJSON) != nil || json.Unmarshal([]byte(want), &wantJSON) != nil || !reflect.DeepEqual(gotJSON, wantJSON) {
			t.Fatalf("unexpected request JSON: %s", body)
		}
		return response(200, validPage), nil
	})
	report, err := c.RunReport(context.Background(), "123", request())
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if report.Rows[0].MetricValues[0].Value != "42" || report.Metadata.TimeZone != "Europe/Moscow" || !report.Metadata.SubjectToThresholding || !report.Metadata.DataLossFromOtherRow {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestTrustBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, property string
		mutate         func(*Request)
	}{
		{"empty property", "", nil}, {"path", "1/../../2", nil}, {"long property", strings.Repeat("1", 21), nil}, {"unicode", "١", nil},
		{"bad date", "1", func(r *Request) { r.From = "2026-02-30" }}, {"loose date", "1", func(r *Request) { r.From = "2026-1-01" }},
		{"reverse", "1", func(r *Request) { r.To = "2025-01-01" }}, {"too long", "1", func(r *Request) { r.To = "2027-01-02" }},
		{"no metrics", "1", func(r *Request) { r.Metrics = nil }}, {"unknown metric", "1", func(r *Request) { r.Metrics = []string{"email"} }},
		{"PII dimension", "1", func(r *Request) { r.Dimensions = []string{"city"} }}, {"duplicate", "1", func(r *Request) { r.Metrics = []string{"sessions", "sessions"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request()
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			c := NewClient(func(context.Context) (string, error) { t.Fatal("token fetched for invalid request"); return "", nil }, nil)
			if _, err := c.RunReport(context.Background(), tc.property, r); err == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
}

func TestPaginationAndIncomplete(t *testing.T) {
	for _, drift := range []string{"none", "rowCount", "sampling", "sampling omitted"} {
		t.Run(drift, func(t *testing.T) {
			calls := 0
			c := clientFor(func(r *http.Request) (*http.Response, error) {
				var body struct {
					Offset string `json:"offset"`
				}
				json.NewDecoder(r.Body).Decode(&body)
				if body.Offset != fmt.Sprint(calls*10000) {
					t.Fatalf("offset=%s", body.Offset)
				}
				calls++
				n, count := 10000, 10001
				if calls == 2 {
					n = 1
					if drift == "rowCount" {
						count++
					}
				}
				row := `{"dimensionValues":[{"value":"20260101"}],"metricValues":[{"value":"42"}]}`
				rows := strings.TrimSuffix(strings.Repeat(row+",", n), ",")
				page := strings.Replace(validPage, `"rows":[`+row+`]`, `"rows":[`+rows+`]`, 1)
				page = strings.Replace(page, `"rowCount":1`, fmt.Sprintf(`"rowCount":%d`, count), 1)
				sampling := `"samplingMetadatas":[{"samplesReadCount":"9007199254740993","samplingSpaceSize":"9007199254740999"}],`
				if calls == 2 && drift == "sampling" {
					sampling = strings.Replace(sampling, "9007199254740993", "9007199254740994", 1)
				}
				if calls == 2 && drift == "sampling omitted" {
					sampling = ""
				}
				page = strings.Replace(page, `"metadata":{`, `"metadata":{`+sampling, 1)
				return response(200, page), nil
			})
			report, err := c.RunReport(context.Background(), "1", request())
			if drift != "none" {
				if !errors.Is(err, ErrIncomplete) || len(report.Rows) != 0 {
					t.Fatalf("partial report: %d %v", len(report.Rows), err)
				}
			} else if err != nil || len(report.Rows) != 10001 || calls != 2 {
				t.Fatalf("rows=%d calls=%d err=%v", len(report.Rows), calls, err)
			}
		})
	}
}

func TestInvalidResponses(t *testing.T) {
	row := `{"dimensionValues":[{"value":"20260101"}],"metricValues":[{"value":"42"}]}`
	cases := map[string]string{
		"truncated": validPage[:len(validPage)-3], "oversize": strings.Repeat(" ", 4*1024*1024+1), "trailing": validPage + `{}`, "malformed": `[]`,
		"header":           strings.Replace(validPage, `"name":"date"`, `"name":"country"`, 1),
		"type":             strings.Replace(validPage, "TYPE_INTEGER", "UNKNOWN", 1),
		"missing value":    strings.Replace(validPage, `{"value":"42"}`, `{}`, 1),
		"columns":          strings.Replace(validPage, `"metricValues":[{"value":"42"}]`, `"metricValues":[]`, 1),
		"missing timezone": strings.Replace(validPage, `"timeZone":"Europe/Moscow",`, "", 1),
		"missing rows":     strings.Replace(validPage, `"rows":[`+row+`],`, "", 1),
		"cap":              strings.Replace(validPage, `"rowCount":1`, `"rowCount":100001`, 1),
		"short page":       strings.Replace(validPage, `"rowCount":1`, `"rowCount":2`, 1),
	}
	for _, value := range []string{"NaN", "inf", "-1", "1e309", "1x", "", strings.Repeat("1", 129)} {
		cases["numeric "+value] = strings.Replace(validPage, `"42"`, `"`+value+`"`, 1)
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := clientFor(func(*http.Request) (*http.Response, error) { return response(200, body), nil }).RunReport(context.Background(), "1", request())
			if err == nil || len(r.Rows) != 0 {
				t.Fatalf("accepted incomplete/invalid response: %v", err)
			}
		})
	}
}

func TestHTTPErrorsAndRedirect(t *testing.T) {
	for _, status := range []int{403, 429, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := clientFor(func(*http.Request) (*http.Response, error) {
				calls++
				r := response(status, `secret-token private@example.com`)
				r.Header.Set("Location", "https://evil.example/secret-token")
				return r, nil
			})
			_, err := c.RunReport(context.Background(), "1", request())
			var he *HTTPError
			if !errors.As(err, &he) || he.StatusCode != status || calls != 1 || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "example") {
				t.Fatalf("unsafe error: %v calls=%d", err, calls)
			}
		})
	}
}

func TestContextAndClientIsolation(t *testing.T) {
	original := &http.Client{Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	c := NewClient(func(ctx context.Context) (string, error) { return "", ctx.Err() }, original)
	if original.Timeout != time.Minute || c.httpClient.Timeout != 20*time.Second {
		t.Fatal("client was mutated or timeout not capped")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.RunReport(ctx, "1", request()); !errors.Is(err, context.Canceled) {
		t.Fatalf("context: %v", err)
	}
	c = NewClient(func(context.Context) (string, error) { return "", errors.New("secret-token private@example.com") }, nil)
	if _, err := c.RunReport(context.Background(), "1", request()); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unsafe token error: %v", err)
	}
}

func TestEmptyReportAndNativeNumericValues(t *testing.T) {
	for _, value := range []string{"", "9007199254740993", "0.000000000001", "1.25e2"} {
		t.Run(value, func(t *testing.T) {
			body := `{"metricHeaders":[{"name":"sessions","type":"TYPE_INTEGER"}],"rowCount":0,"metadata":{"timeZone":"UTC"}}`
			if value != "" {
				body = `{"metricHeaders":[{"name":"sessions","type":"TYPE_FLOAT"}],"rows":[{"metricValues":[{"value":"` + value + `"}]}],"rowCount":1,"metadata":{"timeZone":"UTC"}}`
			}
			c := clientFor(func(r *http.Request) (*http.Response, error) {
				data, _ := io.ReadAll(r.Body)
				if strings.Contains(string(data), "orderBys") || strings.Contains(string(data), "dimensions") {
					t.Fatalf("dimensionless query: %s", data)
				}
				return response(200, body), nil
			})
			r := request()
			r.Dimensions = nil
			r.Metrics = []string{"sessions"}
			report, err := c.RunReport(context.Background(), strings.Repeat("1", 20), r)
			if err != nil {
				t.Fatal(err)
			}
			if value != "" && report.Rows[0].MetricValues[0].Value != value {
				t.Fatal("numeric value changed")
			}
		})
	}
}

func TestCancelledInFlightAndTransportSanitization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := clientFor(func(r *http.Request) (*http.Response, error) {
		cancel()
		return nil, r.Context().Err()
	})
	if _, err := c.RunReport(ctx, "1", request()); !errors.Is(err, context.Canceled) {
		t.Fatalf("context lost: %v", err)
	}
	c = clientFor(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("secret-token private@example.com https://private.example")
	})
	if _, err := c.RunReport(context.Background(), "1", request()); err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "example") {
		t.Fatalf("transport leaked: %v", err)
	}
}

func TestInclusiveDateBound(t *testing.T) {
	r := request()
	r.From = "2024-01-01"
	r.To = "2024-12-31"
	if err := validateRequest("1", r); err != nil {
		t.Fatalf("366 inclusive dates: %v", err)
	}
	r.To = "2025-01-01"
	if err := validateRequest("1", r); err == nil {
		t.Fatal("367 dates accepted")
	}
	for _, name := range []string{"userId", "email", "city", "country,eventName"} {
		r = request()
		r.Dimensions = []string{name}
		if err := validateRequest("1", r); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}

func TestProtobufEmptyReport(t *testing.T) {
	row := `{"dimensionValues":[{"value":"20260101"}],"metricValues":[{"value":"42"}]}`
	empty := strings.Replace(validPage, `"rows":[`+row+`],"rowCount":1,`, "", 1)
	for name, body := range map[string]string{
		"valid":                     empty,
		"rows without count":        strings.Replace(validPage, `"rowCount":1,`, "", 1),
		"null count":                strings.Replace(empty, `"metadata":`, `"rowCount":null,"metadata":`, 1),
		"malformed count":           strings.Replace(empty, `"metadata":`, `"rowCount":"zero","metadata":`, 1),
		"fractional count":          strings.Replace(empty, `"metadata":`, `"rowCount":0.5,"metadata":`, 1),
		"empty object":              `{}`,
		"missing dimension headers": strings.Replace(empty, `"dimensionHeaders":[{"name":"date"}],`, "", 1),
		"missing metric headers":    strings.Replace(empty, `"metricHeaders":[{"name":"activeUsers","type":"TYPE_INTEGER"}],`, "", 1),
		"missing metadata":          strings.Replace(empty, `"metadata":{"timeZone":"Europe/Moscow","subjectToThresholding":true,"dataLossFromOtherRow":true},`, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			r, err := clientFor(func(*http.Request) (*http.Response, error) { return response(200, body), nil }).RunReport(context.Background(), "1", request())
			if name == "valid" {
				if err != nil || r.RowCount != 0 || len(r.Rows) != 0 || r.Metadata.TimeZone != "Europe/Moscow" {
					t.Fatalf("empty protobuf report: %+v %v", r, err)
				}
			} else if !errors.Is(err, ErrIncomplete) || len(r.Rows) != 0 {
				t.Fatalf("accepted invalid empty report: %v", err)
			}
		})
	}
}

func TestSamplingMetadataPreserved(t *testing.T) {
	sampling := `[{"samplesReadCount":"9007199254740993","samplingSpaceSize":"9223372036854775807"}]`
	body := strings.Replace(validPage, `"metadata":{`, `"metadata":{"samplingMetadatas":`+sampling+`,`, 1)
	r, err := clientFor(func(*http.Request) (*http.Response, error) { return response(200, body), nil }).RunReport(context.Background(), "1", request())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(r.Metadata)
	if err != nil || !strings.Contains(string(data), `"samplingMetadatas":`+sampling) || !r.Metadata.SubjectToThresholding || !r.Metadata.DataLossFromOtherRow {
		t.Fatalf("sampling/threshold metadata lost: %s %v", data, err)
	}
}
