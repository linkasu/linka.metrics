package analyticsbuild

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/analyticsapi"
)

type computerFunc func(context.Context, analyticsapi.Query) (analyticsapi.Report, error)

func (f computerFunc) ComputeAnalytics(ctx context.Context, q analyticsapi.Query) (analyticsapi.Report, error) {
	return f(ctx, q)
}

func buildQuery() analyticsapi.Query {
	return analyticsapi.Query{ContractVersion: analyticsapi.Version, Products: []string{"linka-type"}, Metrics: []string{"active_1d"}, CutoffDate: "2026-11-01", SeriesFrom: "2026-11-01", SeriesTo: "2026-11-01"}
}

func buildReport(q analyticsapi.Query) analyticsapi.Report {
	hash, _ := q.Hash()
	value, previous := int64(30), int64(20)
	return analyticsapi.Report{ContractVersion: analyticsapi.Version, QueryHash: hash, Status: "ready", Results: []analyticsapi.Result{{Product: q.Products[0], Metric: q.Metrics[0], Source: "test", Methodology: "installation-v2", Unit: "installation", Status: "ready", Value: &value, Previous: &previous}}}
}

func TestCombinedComputer(t *testing.T) {
	q := buildQuery()
	a := buildReport(q)
	a.DataAsOf = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	b := buildReport(q)
	b.Results[0].Source = "other"
	b.DataAsOf = a.DataAsOf.Add(-time.Hour)
	computer := CombinedComputer{Computers: []analyticsapi.Computer{
		computerFunc(func(context.Context, analyticsapi.Query) (analyticsapi.Report, error) { return a, nil }),
		computerFunc(func(context.Context, analyticsapi.Query) (analyticsapi.Report, error) { return b, nil }),
		computerFunc(func(context.Context, analyticsapi.Query) (analyticsapi.Report, error) {
			return analyticsapi.Report{}, errors.New("secret provider URL")
		}),
	}}
	r, err := computer.ComputeAnalytics(context.Background(), q)
	if err != nil || r.Status != "partial" || len(r.Results) != 2 || *r.Results[0].Value != 30 || !r.DataAsOf.Equal(b.DataAsOf) || r.GeneratedAt.IsZero() {
		t.Fatalf("combined report: %+v, %v", r, err)
	}
	if len(r.Insights) != 3 || strings.Contains(r.Insights[0].Message, "secret") {
		t.Fatalf("unexpected insights: %+v", r.Insights)
	}
}

func TestCombinedRejectsMalformed(t *testing.T) {
	q := buildQuery()
	for name, mutate := range map[string]func(*analyticsapi.Report){
		"hash":       func(r *analyticsapi.Report) { r.QueryHash = "wrong" },
		"version":    func(r *analyticsapi.Report) { r.ContractVersion++ },
		"product":    func(r *analyticsapi.Report) { r.Results[0].Product = "linka-tts" },
		"capability": func(r *analyticsapi.Report) { r.Capabilities = []analyticsapi.Capability{{Product: "linka-tts"}} },
		"insight":    func(r *analyticsapi.Report) { r.Insights = []analyticsapi.Insight{{Product: "linka-tts"}} },
		"negative":   func(r *analyticsapi.Report) { v := int64(-1); r.Results[0].Value = &v },
		"fake zero":  func(r *analyticsapi.Report) { v := int64(0); r.Results[0].Status = "no_data"; r.Results[0].Value = &v },
		"unit":       func(r *analyticsapi.Report) { r.Results[0].Unit = "people" },
		"series":     func(r *analyticsapi.Report) { r.Results[0].Series = make([]analyticsapi.Point, 367) },
		"rows": func(r *analyticsapi.Report) {
			row := r.Results[0]
			r.Results = make([]analyticsapi.Result, 251)
			for i := range r.Results {
				r.Results[i] = row
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := buildReport(q)
			mutate(&r)
			_, err := (CombinedComputer{Computers: []analyticsapi.Computer{computerFunc(func(context.Context, analyticsapi.Query) (analyticsapi.Report, error) { return r, nil })}}).ComputeAnalytics(context.Background(), q)
			if err == nil {
				t.Fatal("accepted malformed report")
			}
		})
	}
}

func TestExecutorPublishesCalendarMonthBeforeRollingSeriesStart(t *testing.T) {
	q := analyticsapi.DefaultQuery(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	q.Products, q.Metrics = []string{"linka-type"}, []string{"calendar_active"}
	hash, _ := q.Hash()
	b := newExecutorBackend()
	b.run.Query, b.run.QueryHash = q, hash
	computer := CombinedComputer{Computers: []analyticsapi.Computer{computerFunc(func(context.Context, analyticsapi.Query) (analyticsapi.Report, error) {
		r := buildReport(q)
		r.Results[0].Series = []analyticsapi.Point{{Date: "2026-10-01", Status: "ready", Value: r.Results[0].Value}}
		return r, nil
	})}}
	if ok, err := (Executor{Backend: b, Computer: computer}).ProcessOne(context.Background()); !ok || err != nil || b.run.State != "completed" || b.published != 1 {
		t.Fatalf("monthly source result must pass combined validation and publication: worked=%v error=%v state=%s published=%d", ok, err, b.run.State, b.published)
	}
}

type executorBackend struct {
	analyticsapi.Backend
	run                             analyticsapi.SyncRun
	states                          []string
	report                          analyticsapi.Report
	nextErr, privacyErr, publishErr error
	failUpdate                      string
	published                       int
	revision                        string
	validUntil                      time.Time
	onUpdate                        func(string)
}

func newExecutorBackend() *executorBackend {
	q := buildQuery()
	hash, _ := q.Hash()
	return &executorBackend{run: analyticsapi.SyncRun{ID: "generation", Query: q, QueryHash: hash, State: "queued", Version: 1}, revision: "revision"}
}

func (b *executorBackend) AnalyticsNext(context.Context) (analyticsapi.SyncRun, error) {
	return b.run, b.nextErr
}
func (b *executorBackend) AnalyticsReport(context.Context, analyticsapi.Query) (analyticsapi.Report, error) {
	if b.report.GenerationID == "" {
		return analyticsapi.Report{}, analyticsapi.ErrNotFound
	}
	return b.report, nil
}
func (b *executorBackend) AnalyticsPrivacy(context.Context) (string, time.Time, error) {
	return b.revision, time.Now().Add(-time.Hour), b.privacyErr
}
func (b *executorBackend) AnalyticsUpdate(_ context.Context, run analyticsapi.SyncRun) error {
	if run.Version != b.run.Version+1 {
		return analyticsapi.ErrConflict
	}
	if run.State == b.failUpdate {
		return errors.New("durable update failed")
	}
	b.run = run
	b.states = append(b.states, run.State)
	if b.onUpdate != nil {
		b.onUpdate(run.State)
	}
	return nil
}
func (b *executorBackend) AnalyticsPublish(_ context.Context, run analyticsapi.SyncRun, report analyticsapi.Report, revision string, until time.Time) error {
	if run.Version != b.run.Version || run.State != "publishing" || revision != b.revision || report.GenerationID != run.ID {
		return analyticsapi.ErrConflict
	}
	if b.publishErr != nil {
		return b.publishErr
	}
	b.report, b.validUntil = report, until
	b.published++
	return nil
}

func TestExecutorStatesAndRestart(t *testing.T) {
	for _, state := range []string{"queued", "running", "validating", "publishing"} {
		t.Run(state, func(t *testing.T) {
			b := newExecutorBackend()
			b.run.State = state
			calls := 0
			e := Executor{Backend: b, Computer: computerFunc(func(_ context.Context, q analyticsapi.Query) (analyticsapi.Report, error) {
				calls++
				return buildReport(q), nil
			})}
			before := time.Now()
			ok, err := e.ProcessOne(context.Background())
			if err != nil || !ok || calls != 1 || b.published != 1 || !reflect.DeepEqual(b.states, []string{"running", "validating", "publishing", "completed"}) || b.run.Version != 5 || b.validUntil.Before(before.Add(8*24*time.Hour)) {
				t.Fatalf("ok=%v err=%v backend=%+v calls=%d", ok, err, b, calls)
			}
		})
	}
}

func TestExecutorPublicationRecovery(t *testing.T) {
	b := newExecutorBackend()
	b.failUpdate = "completed"
	calls := 0
	e := Executor{Backend: b, Computer: computerFunc(func(_ context.Context, q analyticsapi.Query) (analyticsapi.Report, error) {
		calls++
		return buildReport(q), nil
	})}
	if _, err := e.ProcessOne(context.Background()); err == nil || b.run.State != "publishing" {
		t.Fatal("lost durable publication state")
	}
	b.failUpdate = ""
	if ok, err := e.ProcessOne(context.Background()); !ok || err != nil || calls != 1 || b.published != 1 || b.run.State != "completed" {
		t.Fatalf("recovery: %v %v %+v calls=%d", ok, err, b, calls)
	}
}

func TestExecutorFailures(t *testing.T) {
	for _, scenario := range []string{"privacy", "compute", "malformed", "invalidated", "cancel", "running update", "validating update", "publishing update"} {
		t.Run(scenario, func(t *testing.T) {
			b := newExecutorBackend()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "privacy" {
				b.privacyErr = errors.New("secret privacy")
			}
			if scenario == "invalidated" {
				b.publishErr = analyticsapi.ErrInvalidated
			}
			if strings.HasSuffix(scenario, " update") {
				b.failUpdate = strings.TrimSuffix(scenario, " update")
			}
			e := Executor{Backend: b, Computer: computerFunc(func(_ context.Context, q analyticsapi.Query) (analyticsapi.Report, error) {
				if scenario == "compute" {
					return analyticsapi.Report{}, errors.New("secret provider")
				}
				if scenario == "cancel" {
					cancel()
					return buildReport(q), nil
				}
				r := buildReport(q)
				if scenario == "malformed" {
					r.Results[0].Product = "linka-tts"
				}
				return r, nil
			})}
			ok, err := e.ProcessOne(ctx)
			if !ok || err == nil || b.published != 0 || strings.Contains(b.run.Reason, "secret") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("failure: %v %v %+v", ok, err, b)
			}
			if scenario == "cancel" {
				if b.run.State != "running" {
					t.Fatal("cancellation must leave active")
				}
			} else if !strings.HasSuffix(scenario, " update") && b.run.State != "failed" {
				t.Fatal("must fail durably")
			}
			if scenario == "invalidated" && b.run.Reason != "privacy_changed" {
				t.Fatal("wrong privacy reason")
			}
		})
	}
}

func TestExecutorEmptyQueue(t *testing.T) {
	b := newExecutorBackend()
	b.nextErr = analyticsapi.ErrNotFound
	if ok, err := (Executor{Backend: b}).ProcessOne(context.Background()); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
}

func TestCombinedStatuses(t *testing.T) {
	for _, test := range []struct {
		statuses []string
		failed   bool
		want     string
	}{
		{nil, false, "no_data"}, {nil, true, "unavailable"},
		{[]string{"ready"}, false, "ready"},
		{[]string{"no_data"}, false, "no_data"},
		{[]string{"unavailable"}, false, "unavailable"},
		{[]string{"ready", "no_data"}, false, "partial"},
		{[]string{"ready", "unavailable"}, false, "partial"},
		{[]string{"no_data", "unavailable"}, false, "partial"},
	} {
		t.Run(fmt.Sprint(test.statuses, test.failed), func(t *testing.T) {
			q := buildQuery()
			computers := []analyticsapi.Computer{}
			for i, status := range test.statuses {
				r := buildReport(q)
				r.Status = "ready" // Source summary must not override actual row status.
				r.Results[0].Source = fmt.Sprint(i)
				r.Results[0].Status = status
				if status != "ready" {
					r.Results[0].Value, r.Results[0].Previous = nil, nil
				}
				computers = append(computers, computerFunc(func(context.Context, analyticsapi.Query) (analyticsapi.Report, error) { return r, nil }))
			}
			if test.failed {
				computers = append(computers, computerFunc(func(context.Context, analyticsapi.Query) (analyticsapi.Report, error) {
					return analyticsapi.Report{}, errors.New("secret")
				}))
			}
			r, err := (CombinedComputer{Computers: computers}).ComputeAnalytics(context.Background(), q)
			if err != nil || r.Status != test.want {
				t.Fatalf("status=%s err=%v want=%s", r.Status, err, test.want)
			}
			for _, row := range r.Results {
				if row.Status != "ready" && row.Value != nil {
					t.Fatal("invented numeric row")
				}
			}
		})
	}
}

func TestChangeInsightThresholds(t *testing.T) {
	for _, test := range []struct {
		value, previous int64
		want            bool
	}{
		{30, 20, true}, {25, 20, true}, {20, 25, true}, {24, 20, false},
		{19, 30, false}, {30, 19, false}, {104, 100, false}, {119, 100, false}, {120, 100, true},
	} {
		r := buildReport(buildQuery()).Results[0]
		r.Value, r.Previous = &test.value, &test.previous
		if got := len(changeInsights([]analyticsapi.Result{r})) > 0; got != test.want {
			t.Fatalf("%d/%d: %v", test.value, test.previous, got)
		}
		r.Status = "unavailable"
		if len(changeInsights([]analyticsapi.Result{r})) != 0 {
			t.Fatal("non-ready insight")
		}
	}
}

func TestCombinedBoundsAndDuplicateIdentity(t *testing.T) {
	q := buildQuery()
	for _, count := range []int{100, 250, 251} {
		a := buildReport(q)
		a.Results = nil
		for i := 0; i < count; i++ {
			row := buildReport(q).Results[0]
			row.Source = fmt.Sprint(i)
			a.Results = append(a.Results, row)
		}
		// Split across valid sources: the merged budget must still be checked.
		b := a
		mid := len(a.Results) / 2
		b.Results, a.Results = a.Results[mid:], a.Results[:mid]
		r, err := (CombinedComputer{Computers: []analyticsapi.Computer{
			computerFunc(func(context.Context, analyticsapi.Query) (analyticsapi.Report, error) { return a, nil }),
			computerFunc(func(context.Context, analyticsapi.Query) (analyticsapi.Report, error) { return b, nil }),
		}}).ComputeAnalytics(context.Background(), q)
		if count > 250 {
			if !errors.Is(err, analyticsapi.ErrBudget) {
				t.Fatalf("missing merged budget: %v", err)
			}
			continue
		}
		if err != nil || len(r.Results) != count || len(r.Insights) != 100 {
			t.Fatalf("count=%d: %+v %v", count, r, err)
		}
	}
	computer := computerFunc(func(context.Context, analyticsapi.Query) (analyticsapi.Report, error) { return buildReport(q), nil })
	if _, err := (CombinedComputer{Computers: []analyticsapi.Computer{computer, computer}}).ComputeAnalytics(context.Background(), q); err == nil {
		t.Fatal("duplicate source row inflates counts")
	}
}

func TestExecutorOlderGenerationCannotRecoverInvalidation(t *testing.T) {
	b := newExecutorBackend()
	b.run.State = "publishing"
	b.report = buildReport(b.run.Query)
	b.report.GenerationID = "older"
	b.publishErr = analyticsapi.ErrInvalidated
	calls := 0
	e := Executor{Backend: b, Computer: computerFunc(func(_ context.Context, q analyticsapi.Query) (analyticsapi.Report, error) {
		calls++
		return buildReport(q), nil
	})}
	if _, err := e.ProcessOne(context.Background()); !errors.Is(err, analyticsapi.ErrInvalidated) || calls != 1 || b.run.State != "failed" || b.run.Reason != "privacy_changed" || b.published != 0 {
		t.Fatalf("unsafe recovery: %v %+v", err, b)
	}
}

func TestExecutorPublicationAcknowledgementLost(t *testing.T) {
	b := newExecutorBackend()
	b.publishErr = errors.New("secret uncertain publication")
	calls := 0
	e := Executor{Backend: b, Computer: computerFunc(func(_ context.Context, q analyticsapi.Query) (analyticsapi.Report, error) {
		calls++
		return buildReport(q), nil
	})}
	if _, err := e.ProcessOne(context.Background()); err == nil || strings.Contains(err.Error(), "secret") || b.run.State != "publishing" {
		t.Fatalf("lost recovery state: %v %+v", err, b)
	}
	// Simulate the successful commit whose acknowledgement was lost.
	b.report = buildReport(b.run.Query)
	b.report.GenerationID = b.run.ID
	if _, err := e.ProcessOne(context.Background()); err != nil || calls != 1 || b.run.State != "completed" {
		t.Fatalf("recomputed committed generation: %v %+v", err, b)
	}
}

func TestExecutorCancellationBeforePublication(t *testing.T) {
	for _, state := range []string{"validating", "publishing"} {
		t.Run(state, func(t *testing.T) {
			b := newExecutorBackend()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b.onUpdate = func(next string) {
				if next == state {
					cancel()
				}
			}
			e := Executor{Backend: b, Computer: computerFunc(func(_ context.Context, q analyticsapi.Query) (analyticsapi.Report, error) { return buildReport(q), nil })}
			if _, err := e.ProcessOne(ctx); !errors.Is(err, context.Canceled) || b.published != 0 || b.run.State != state {
				t.Fatalf("published after cancellation: %v %+v", err, b)
			}
		})
	}
}

func TestExecutorPrivacyCancellationAndFailedUpdate(t *testing.T) {
	b := newExecutorBackend()
	b.privacyErr = fmt.Errorf("secret provider: %w", context.Canceled)
	if _, err := (Executor{Backend: b}).ProcessOne(context.Background()); err != context.Canceled || b.run.State != "running" {
		t.Fatalf("privacy cancellation: %v %+v", err, b)
	}
	b = newExecutorBackend()
	b.privacyErr, b.failUpdate = errors.New("secret privacy"), "failed"
	if _, err := (Executor{Backend: b}).ProcessOne(context.Background()); err == nil || err.Error() != "durable update failed" || b.run.State != "running" {
		t.Fatalf("invented durable failure: %v %+v", err, b)
	}
}
