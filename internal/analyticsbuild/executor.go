package analyticsbuild

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/analyticsapi"
)

var errInvalidReport = errors.New("invalid analytics report")

// CombinedComputer retains source-local results, never adding distinct counts.
type CombinedComputer struct {
	Computers []analyticsapi.Computer
}

func (c CombinedComputer) ComputeAnalytics(ctx context.Context, q analyticsapi.Query) (analyticsapi.Report, error) {
	hash, err := q.Hash()
	if err != nil {
		return analyticsapi.Report{}, err
	}
	r := analyticsapi.Report{ContractVersion: q.ContractVersion, QueryHash: hash,
		Results: []analyticsapi.Result{}, Capabilities: []analyticsapi.Capability{}, Insights: []analyticsapi.Insight{}}
	failures := false
	for _, computer := range c.Computers {
		if err := ctx.Err(); err != nil {
			return analyticsapi.Report{}, err
		}
		part, err := computer.ComputeAnalytics(ctx, q)
		if err := interrupted(ctx, err); err != nil {
			return analyticsapi.Report{}, err
		}
		if err != nil {
			failures = true
			continue
		}
		if err := validateReport(q, part); err != nil {
			return analyticsapi.Report{}, err
		}
		r.Results = append(r.Results, part.Results...)
		r.Capabilities = append(r.Capabilities, part.Capabilities...)
		r.Insights = append(r.Insights, part.Insights...)
		if !part.DataAsOf.IsZero() && (r.DataAsOf.IsZero() || part.DataAsOf.Before(r.DataAsOf)) {
			r.DataAsOf = part.DataAsOf
		}
	}
	if failures {
		for _, product := range q.Products {
			r.Insights = append(r.Insights, analyticsapi.Insight{Kind: "quality", Product: product, Message: "Часть источников недоступна; данные могут быть неполными."})
		}
	}
	// Stable source-local order also makes the insight limit deterministic.
	sort.SliceStable(r.Results, func(i, j int) bool {
		a, b := r.Results[i], r.Results[j]
		return resultKey(a) < resultKey(b)
	})
	r.Insights = append(r.Insights, changeInsights(r.Results)...)
	if len(r.Insights) > 100 {
		r.Insights = r.Insights[:100]
	}
	if err := validateReport(q, r); err != nil {
		return analyticsapi.Report{}, err
	}
	r.Status = reportStatus(r.Results, failures)
	r.GeneratedAt = time.Now().UTC()
	return r, nil
}

func resultKey(r analyticsapi.Result) string {
	return r.Product + "\x00" + r.Metric + "\x00" + r.Source + "\x00" + r.Methodology + "\x00" + r.Unit
}

func changeInsights(results []analyticsapi.Result) []analyticsapi.Insight {
	var insights []analyticsapi.Insight
	for _, r := range results {
		if r.Status != "ready" || r.Value == nil || r.Previous == nil || *r.Value < 20 || *r.Previous < 20 {
			continue
		}
		// Values are nonnegative; subtract in int64 before converting, avoiding overflow.
		delta := *r.Value - *r.Previous
		absolute := delta
		if absolute < 0 {
			absolute = -absolute
		}
		if absolute < 5 || float64(absolute)/float64(*r.Previous) < .2 {
			continue
		}
		direction := "вырос"
		if delta < 0 {
			direction = "снизился"
		}
		insights = append(insights, analyticsapi.Insight{Kind: "change", Product: r.Product, Metric: r.Metric,
			Message: fmt.Sprintf("%s: показатель %s (%s, %s) %s с %d до %d.", r.Product, r.Metric, r.Source, r.Methodology, direction, *r.Previous, *r.Value)})
		if len(insights) == 100 {
			break
		}
	}
	return insights
}

func reportStatus(results []analyticsapi.Result, failed bool) string {
	ready, noData, unavailable := 0, 0, 0
	for _, r := range results {
		switch r.Status {
		case "ready":
			ready++
		case "no_data":
			noData++
		case "unavailable":
			unavailable++
		}
	}
	if ready > 0 {
		if failed || noData+unavailable > 0 {
			return "partial"
		}
		return "ready"
	}
	if noData > 0 {
		if failed || unavailable > 0 {
			return "partial"
		}
		return "no_data"
	}
	if failed || unavailable > 0 {
		return "unavailable"
	}
	return "no_data"
}

func validUnit(unit string) bool {
	switch unit {
	case "installation", "product_account", "property_identifier", "service_operation", "event", "session":
		return true
	}
	return false
}

func validValues(status string, value, previous, contributors *int64) bool {
	if status != "ready" && status != "no_data" && status != "unavailable" {
		return false
	}
	for _, v := range []*int64{value, previous, contributors} {
		if v != nil && (*v < 0 || status != "ready") {
			return false
		}
	}
	return status != "ready" || value != nil
}

func validateReport(q analyticsapi.Query, r analyticsapi.Report) error {
	hash, err := q.Hash()
	if err != nil {
		return err
	}
	if r.ContractVersion != q.ContractVersion || r.QueryHash != hash {
		return errInvalidReport
	}
	if len(r.Results) > 250 {
		return analyticsapi.ErrBudget
	}
	products, metrics := map[string]bool{}, map[string]bool{}
	for _, product := range q.Products {
		products[product] = true
	}
	for _, metric := range q.Metrics {
		metrics[metric] = true
	}
	seen := map[string]bool{}
	for _, row := range r.Results {
		key := resultKey(row)
		if !products[row.Product] || !metrics[row.Metric] || !validUnit(row.Unit) || row.Source == "" || row.Methodology == "" || seen[key] || !validValues(row.Status, row.Value, row.Previous, row.Contributors) {
			return errInvalidReport
		}
		seen[key] = true
		if len(row.Series) > 366 {
			return analyticsapi.ErrBudget
		}
		dates := map[string]bool{}
		seriesFrom, seriesTo := q.SeriesFrom, q.SeriesTo
		if row.Metric == "calendar_active" {
			from, _ := time.Parse(time.DateOnly, q.SeriesFrom)
			seriesFrom = time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
			cutoff, _ := time.Parse(time.DateOnly, q.CutoffDate)
			end := cutoff.AddDate(0, 0, 1)
			seriesTo = time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1).Format(time.DateOnly)
		}
		for _, point := range row.Series {
			date, err := time.Parse(time.DateOnly, point.Date)
			if err != nil || point.Date < seriesFrom || point.Date > seriesTo || row.Metric == "calendar_active" && date.Day() != 1 || dates[point.Date] || !validValues(point.Status, point.Value, nil, point.Contributors) {
				return errInvalidReport
			}
			dates[point.Date] = true
		}
	}
	for _, capability := range r.Capabilities {
		if !products[capability.Product] || !validUnit(capability.Unit) || capability.Source == "" || capability.Methodology == "" {
			return errInvalidReport
		}
		switch capability.Status {
		case "ready", "partial", "no_data", "unavailable":
		default:
			return errInvalidReport
		}
	}
	for _, insight := range r.Insights {
		if !products[insight.Product] || (insight.Metric != "" && !metrics[insight.Metric]) {
			return errInvalidReport
		}
	}
	return nil
}

// Executor requires a single-host external flock for its entire lifetime.
// Queue transitions use durable optimistic versions; they are not a process lock.
type Executor struct {
	Backend  analyticsapi.Backend
	Computer analyticsapi.Computer
}

func (e Executor) ProcessOne(ctx context.Context) (bool, error) {
	run, err := e.Backend.AnalyticsNext(ctx)
	if errors.Is(err, analyticsapi.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if run.State == "publishing" {
		report, err := e.Backend.AnalyticsReport(ctx, run.Query)
		if err == nil && report.GenerationID == run.ID {
			return true, e.transition(ctx, &run, "completed", "")
		}
		if err != nil && !errors.Is(err, analyticsapi.ErrNotFound) && !errors.Is(err, analyticsapi.ErrInvalidated) {
			return true, err
		}
	}
	switch run.State {
	case "queued", "running", "validating", "publishing":
	default:
		return true, analyticsapi.ErrConflict
	}
	hash, err := run.Query.Hash()
	if err != nil || hash != run.QueryHash {
		return true, e.fail(ctx, &run, "invalid_query", analyticsapi.ErrInvalidQuery)
	}
	if err := e.transition(ctx, &run, "running", ""); err != nil {
		return true, err
	}
	revision, _, err := e.Backend.AnalyticsPrivacy(ctx)
	if err != nil {
		return true, e.fail(ctx, &run, "privacy_unavailable", err)
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	report, err := e.Computer.ComputeAnalytics(ctx, run.Query)
	if err != nil {
		return true, e.fail(ctx, &run, "compute_failed", err)
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if err := validateReport(run.Query, report); err != nil {
		return true, e.fail(ctx, &run, "invalid_report", err)
	}
	report.GenerationID, report.GeneratedAt = run.ID, time.Now().UTC()
	// CombinedComputer accounts for failed sources in its already-derived status.
	status := reportStatus(report.Results, false)
	if report.Status == "partial" && (status == "ready" || status == "no_data") {
		status = "partial"
	}
	if len(report.Results) == 0 && report.Status == "unavailable" {
		status = "unavailable"
	}
	report.Status = status
	if len(report.Insights) > 100 {
		report.Insights = report.Insights[:100]
	}
	if err := e.transition(ctx, &run, "validating", ""); err != nil {
		return true, err
	}
	if err := e.transition(ctx, &run, "publishing", ""); err != nil {
		return true, err
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if err := e.Backend.AnalyticsPublish(ctx, run, report, revision, time.Now().UTC().Add(8*24*time.Hour)); err != nil {
		if errors.Is(err, analyticsapi.ErrInvalidated) {
			return true, e.fail(ctx, &run, "privacy_changed", err)
		}
		if err := interrupted(ctx, err); err != nil {
			return true, err
		}
		// A lost acknowledgement may follow a committed publish; recover by generation.
		return true, errors.New("analytics build: publish_failed")
	}
	return true, e.transition(ctx, &run, "completed", "")
}

func (e Executor) transition(ctx context.Context, run *analyticsapi.SyncRun, state, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	next := *run
	next.State, next.Reason, next.UpdatedAt = state, reason, time.Now().UTC()
	next.Version++
	if err := e.Backend.AnalyticsUpdate(ctx, next); err != nil {
		return err
	}
	*run = next
	return nil
}

func interrupted(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func (e Executor) fail(ctx context.Context, run *analyticsapi.SyncRun, reason string, cause error) error {
	if err := interrupted(ctx, cause); err != nil {
		return err
	}
	if err := e.transition(ctx, run, "failed", reason); err != nil {
		return err
	}
	// Persist and return only closed reasons, never provider text or payloads.
	if errors.Is(cause, analyticsapi.ErrInvalidated) {
		return analyticsapi.ErrInvalidated
	}
	return errors.New("analytics build: " + reason)
}
