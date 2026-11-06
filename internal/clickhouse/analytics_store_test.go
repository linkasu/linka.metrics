package clickhouse

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/linkasu/linka.plays-metric/internal/analyticsapi"
)

type analyticsTestRow struct {
	driver.Row
	values []any
	err    error
}

func (r analyticsTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("unexpected scan columns")
	}
	for i := range dest {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(r.values[i]))
	}
	return nil
}

type analyticsTestRows struct {
	driver.Rows
	scopes []string
	index  int
}

func (r *analyticsTestRows) Next() bool { r.index++; return r.index <= len(r.scopes) }
func (r *analyticsTestRows) Scan(dest ...any) error {
	*dest[0].(*string) = r.scopes[r.index-1]
	return nil
}
func (r *analyticsTestRows) Close() error { return nil }
func (r *analyticsTestRows) Err() error   { return nil }

type analyticsTestConn struct {
	driver.Conn
	rows       []analyticsTestRow
	scopes     []string
	queries    []string
	queryArgs  [][]any
	writes     [][]any
	execErr    error
	execErrors []error
}

func (c *analyticsTestConn) QueryRow(_ context.Context, query string, args ...any) driver.Row {
	c.queries = append(c.queries, query)
	c.queryArgs = append(c.queryArgs, args)
	if len(c.rows) == 0 {
		return analyticsTestRow{err: errors.New("unexpected query")}
	}
	row := c.rows[0]
	c.rows = c.rows[1:]
	return row
}

func (c *analyticsTestConn) Query(_ context.Context, query string, args ...any) (driver.Rows, error) {
	c.queries = append(c.queries, query)
	c.queryArgs = append(c.queryArgs, args)
	return &analyticsTestRows{scopes: c.scopes}, nil
}

func (c *analyticsTestConn) Exec(_ context.Context, query string, args ...any) error {
	c.queries = append(c.queries, query)
	c.queryArgs = append(c.queryArgs, args)
	c.writes = append(c.writes, args)
	if len(c.execErrors) > 0 {
		err := c.execErrors[0]
		c.execErrors = c.execErrors[1:]
		return err
	}
	return c.execErr
}

func analyticsTestQuery() analyticsapi.Query {
	return analyticsapi.Query{ContractVersion: analyticsapi.Version, Products: []string{"linka-type"},
		Metrics: []string{"active_1d"}, CutoffDate: "2026-11-06", SeriesFrom: "2026-11-01", SeriesTo: "2026-11-06"}
}

func analyticsRunRow(run analyticsapi.SyncRun) analyticsTestRow {
	body, _ := json.Marshal(run.Query)
	return analyticsTestRow{values: []any{uuid.MustParse(run.ID), run.QueryHash, run.State, string(body), run.Reason, run.CreatedAt, run.UpdatedAt, run.Version}}
}

func TestAnalyticsRejectsInvalidInputBeforeStorage(t *testing.T) {
	s := &Store{}
	ctx := context.Background()
	q := analyticsTestQuery()
	for _, id := range []string{"", "invalid", uuid.Nil.String(), strings.Repeat("a", 32)} {
		if _, err := s.AnalyticsEnqueue(ctx, q, id); !errors.Is(err, analyticsapi.ErrInvalidQuery) {
			t.Fatalf("enqueue invalid ID: %v", err)
		}
		if _, err := s.AnalyticsRun(ctx, id); !errors.Is(err, analyticsapi.ErrInvalidQuery) {
			t.Fatalf("run invalid ID: %v", err)
		}
	}
	q.Filters = map[string][]string{"app_version": {strings.Repeat("x", analyticsQueryLimit+1)}}
	if _, err := s.AnalyticsEnqueue(ctx, q, uuid.NewString()); !errors.Is(err, analyticsapi.ErrInvalidQuery) {
		t.Fatalf("oversized query: %v", err)
	}
	if _, err := s.AnalyticsReport(ctx, analyticsapi.Query{}); !errors.Is(err, analyticsapi.ErrInvalidQuery) {
		t.Fatalf("invalid query: %v", err)
	}
	run := analyticsapi.SyncRun{ID: uuid.NewString(), QueryHash: strings.Repeat("a", 64), State: "running"}
	report := analyticsapi.Report{ContractVersion: analyticsapi.Version, QueryHash: run.QueryHash, GenerationID: run.ID}
	for _, revision := range []string{"", strings.Repeat("g", 64), strings.Repeat("a", 63)} {
		if err := s.AnalyticsPublish(ctx, run, report, revision, time.Now().Add(time.Hour)); !errors.Is(err, analyticsapi.ErrInvalidQuery) {
			t.Fatalf("invalid revision: %v", err)
		}
	}
	report.Insights = []analyticsapi.Insight{{Message: strings.Repeat("x", analyticsReportLimit)}}
	if err := s.AnalyticsPublish(ctx, run, report, run.QueryHash, time.Now().Add(time.Hour)); !errors.Is(err, analyticsapi.ErrInvalidQuery) {
		t.Fatalf("oversized report: %v", err)
	}
}

func TestAnalyticsEnqueueReplayConflictAndCoalesce(t *testing.T) {
	q := analyticsTestQuery()
	hash, _ := q.Hash()
	now := time.Now().UTC()
	stored := analyticsapi.SyncRun{ID: uuid.NewString(), QueryHash: hash, Query: q, State: "queued", Version: 1, CreatedAt: now, UpdatedAt: now}
	for _, tc := range []struct {
		name    string
		id      string
		query   analyticsapi.Query
		rows    []analyticsTestRow
		wantErr error
		writes  int
	}{
		{"new", stored.ID, q, []analyticsTestRow{{err: sql.ErrNoRows}, {err: sql.ErrNoRows}, {err: sql.ErrNoRows}}, nil, 2},
		{"recover missing alias", stored.ID, q, []analyticsTestRow{{err: sql.ErrNoRows}, analyticsRunRow(stored)}, nil, 1},
		{"replay alias", stored.ID, q, []analyticsTestRow{{values: []any{hash, uuid.MustParse(stored.ID)}}, analyticsRunRow(stored)}, nil, 0},
		{"coalesce", uuid.NewString(), q, []analyticsTestRow{{err: sql.ErrNoRows}, {err: sql.ErrNoRows}, analyticsRunRow(stored)}, nil, 1},
		{"conflict", stored.ID, func() analyticsapi.Query { v := q; v.Metrics = []string{"sessions"}; return v }(), []analyticsTestRow{{err: sql.ErrNoRows}, analyticsRunRow(stored)}, analyticsapi.ErrConflict, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &analyticsTestConn{rows: tc.rows}
			s := &Store{connection: conn, now: func() time.Time { return now }}
			run, err := s.AnalyticsEnqueue(context.Background(), tc.query, tc.id)
			if !errors.Is(err, tc.wantErr) || len(conn.writes) != tc.writes {
				t.Fatalf("err=%v writes=%d", err, len(conn.writes))
			}
			if err == nil && (run.ID != stored.ID || run.Version != 1 || run.State != "queued") {
				t.Fatalf("unexpected run: %+v", run)
			}
		})
	}
}

func TestAnalyticsCoalescedAliasSurvivesRestart(t *testing.T) {
	q := analyticsTestQuery()
	hash, _ := q.Hash()
	now := time.Now().UTC()
	stored := analyticsapi.SyncRun{ID: uuid.NewString(), QueryHash: hash, Query: q, State: "queued", Version: 1, CreatedAt: now, UpdatedAt: now}
	requestID := uuid.NewString()
	conn := &analyticsTestConn{rows: []analyticsTestRow{{err: sql.ErrNoRows}, {err: sql.ErrNoRows}, analyticsRunRow(stored)}}
	s := &Store{connection: conn, now: func() time.Time { return now }}
	got, err := s.AnalyticsEnqueue(context.Background(), q, requestID)
	if err != nil || got.ID != stored.ID || len(conn.writes) != 1 {
		t.Fatalf("coalesce: %+v %v writes=%d", got, err, len(conn.writes))
	}
	alias := conn.writes[0]
	if alias[0] != uuid.MustParse(requestID) || alias[1] != hash || alias[2] != uuid.MustParse(stored.ID) {
		t.Fatalf("incorrect persisted alias: %+v", alias)
	}
	// A fresh Store knows only the durable alias, even after its run completes.
	stored.State, stored.Version = "completed", 3
	aliasRow := analyticsTestRow{values: []any{alias[1], alias[2]}}
	restartedConn := &analyticsTestConn{rows: []analyticsTestRow{aliasRow, analyticsRunRow(stored), aliasRow}}
	restarted := &Store{connection: restartedConn, now: func() time.Time { return now }}
	got, err = restarted.AnalyticsEnqueue(context.Background(), q, requestID)
	if err != nil || got.ID != stored.ID || got.State != "completed" {
		t.Fatalf("replay after restart: %+v %v", got, err)
	}
	changed := q
	changed.Metrics = []string{"sessions"}
	if _, err := restarted.AnalyticsEnqueue(context.Background(), changed, requestID); !errors.Is(err, analyticsapi.ErrConflict) {
		t.Fatalf("changed coalesced request after restart: %v", err)
	}
	if len(restartedConn.writes) != 0 {
		t.Fatal("alias replay or conflict wrote another job")
	}
}

func TestAnalyticsAliasFailureDoesNotAcknowledgeEnqueue(t *testing.T) {
	q := analyticsTestQuery()
	hash, _ := q.Hash()
	now := time.Now().UTC()
	stored := analyticsapi.SyncRun{ID: uuid.NewString(), QueryHash: hash, Query: q, State: "queued", Version: 1, CreatedAt: now, UpdatedAt: now}
	conn := &analyticsTestConn{rows: []analyticsTestRow{{err: sql.ErrNoRows}, {err: sql.ErrNoRows}, analyticsRunRow(stored)}, execErr: errors.New("alias insert failed")}
	s := &Store{connection: conn, now: func() time.Time { return now }}
	if _, err := s.AnalyticsEnqueue(context.Background(), q, uuid.NewString()); err == nil {
		t.Fatal("enqueue acknowledged without durable alias")
	}
}

func TestAnalyticsRecoversRunInsertedBeforeAliasFailure(t *testing.T) {
	q := analyticsTestQuery()
	hash, _ := q.Hash()
	now := time.Now().UTC()
	id := uuid.NewString()
	conn := &analyticsTestConn{
		rows:       []analyticsTestRow{{err: sql.ErrNoRows}, {err: sql.ErrNoRows}, {err: sql.ErrNoRows}},
		execErrors: []error{nil, errors.New("alias insert failed")},
	}
	s := &Store{connection: conn, now: func() time.Time { return now }}
	if _, err := s.AnalyticsEnqueue(context.Background(), q, id); err == nil {
		t.Fatal("enqueue acknowledged a run without its alias")
	}
	if len(conn.writes) != 2 || conn.writes[0][0] != uuid.MustParse(id) {
		t.Fatalf("new run did not reserve request ID before alias: %+v", conn.writes)
	}
	stored := analyticsapi.SyncRun{ID: id, QueryHash: hash, Query: q, State: "queued", Version: 1, CreatedAt: now, UpdatedAt: now}
	restartedConn := &analyticsTestConn{rows: []analyticsTestRow{{err: sql.ErrNoRows}, analyticsRunRow(stored)}}
	restarted := &Store{connection: restartedConn, now: func() time.Time { return now }}
	got, err := restarted.AnalyticsEnqueue(context.Background(), q, id)
	if err != nil || got.ID != id || len(restartedConn.writes) != 1 || len(restartedConn.writes[0]) != 4 {
		t.Fatalf("recovery did not insert only alias: %+v %v writes=%+v", got, err, restartedConn.writes)
	}
}

func TestAnalyticsReportPrivacyAndExpiry(t *testing.T) {
	q := analyticsTestQuery()
	hash, _ := q.Hash()
	id := uuid.New()
	now := time.Now().UTC()
	digest := sha256.Sum256(nil)
	revision := hex.EncodeToString(digest[:])
	body, _ := json.Marshal(analyticsapi.Report{ContractVersion: analyticsapi.Version, QueryHash: hash, GenerationID: id.String()})
	for _, tc := range []struct {
		name, body, revision string
		expiry               time.Time
		invalidated          bool
		valid                bool
	}{
		{"valid", string(body), revision, now.Add(time.Hour), false, true},
		{"expired", string(body), revision, now, true, false},
		{"privacy changed", string(body), strings.Repeat("a", 64), now.Add(time.Hour), true, false},
		{"malformed", "{", revision, now.Add(time.Hour), false, false},
		{"too big", strings.Repeat("x", analyticsReportLimit+1), revision, now.Add(time.Hour), false, false},
		{"wrong hash", strings.Replace(string(body), hash, strings.Repeat("b", 64), 1), revision, now.Add(time.Hour), false, false},
		{"wrong contract", strings.Replace(string(body), `"contract_version":1`, `"contract_version":2`, 1), revision, now.Add(time.Hour), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &analyticsTestConn{rows: []analyticsTestRow{
				{values: []any{tc.body, tc.revision, tc.expiry, id}},
			}}
			s := &Store{connection: conn, now: func() time.Time { return now }}
			_, err := s.AnalyticsReport(context.Background(), q)
			if (err == nil) != tc.valid || (tc.invalidated && !errors.Is(err, analyticsapi.ErrInvalidated)) {
				t.Fatalf("report error=%v", err)
			}
			if len(conn.queries) != 2 || !strings.Contains(conn.queries[1], "privacy_suppressions_v2 FINAL") {
				t.Fatalf("GET must read only snapshot and privacy revision: %v", conn.queries)
			}
			for _, query := range conn.queries {
				if strings.Contains(query, "expires_at") || strings.Contains(query, "events_v2") || strings.Contains(query, "product_outcomes_v2") {
					t.Fatalf("GET queried raw sources/expiry: %s", query)
				}
			}
		})
	}
}

func TestAnalyticsCalendarExpiryCoversFactsBeforeRollingWindow(t *testing.T) {
	q := analyticsapi.DefaultQuery(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	conn := &analyticsTestConn{rows: []analyticsTestRow{{values: []any{(*time.Time)(nil)}}}}
	s := &Store{connection: conn, now: time.Now}
	if _, err := s.analyticsQueryExpiry(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	monthFrom := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	covered := false
	for _, arg := range conn.queryArgs[0][2:] {
		if date, ok := arg.(time.Time); ok && !date.After(monthFrom) {
			covered = true
		}
	}
	if !covered {
		t.Fatal("calendar-month facts can expire while the snapshot remains readable")
	}
}

func TestAnalyticsUpdatesAreMonotonicAndNeverResurrectTerminal(t *testing.T) {
	q := analyticsTestQuery()
	hash, _ := q.Hash()
	now := time.Now().UTC()
	stored := analyticsapi.SyncRun{ID: uuid.NewString(), Query: q, QueryHash: hash, State: "running", Version: 2, CreatedAt: now}
	for _, tc := range []struct {
		name, storedState, state string
		version                  uint64
		want                     error
	}{
		{"advance", "running", "publishing", 3, nil},
		{"restart", "publishing", "running", 4, nil},
		{"same version", "running", "publishing", 2, analyticsapi.ErrConflict},
		{"terminal", "completed", "running", 3, analyticsapi.ErrConflict},
		{"failed", "failed", "running", 3, analyticsapi.ErrConflict},
		{"cancelled", "cancelled", "running", 3, analyticsapi.ErrConflict},
		{"queued forbidden", "running", "queued", 3, analyticsapi.ErrInvalidQuery},
		{"unknown forbidden", "running", "unknown", 3, analyticsapi.ErrInvalidQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored.State = tc.storedState
			conn := &analyticsTestConn{rows: []analyticsTestRow{analyticsRunRow(stored)}}
			s := &Store{connection: conn, now: func() time.Time { return now }}
			run := stored
			run.State, run.Version = tc.state, tc.version
			if err := s.AnalyticsUpdate(context.Background(), run); !errors.Is(err, tc.want) {
				t.Fatalf("update: %v", err)
			}
			if tc.want != nil && len(conn.writes) != 0 {
				t.Fatal("invalid update wrote state")
			}
		})
	}
}

func TestAnalyticsReportProjectionDoesNotReadRawSources(t *testing.T) {
	q := analyticsTestQuery()
	hash, _ := q.Hash()
	all := q
	all.Products = nil
	for _, product := range analyticsapi.Products() {
		all.Products = append(all.Products, product.ID)
	}
	allHash, _ := all.Hash()
	now := time.Now().UTC()
	id := uuid.New()
	digest := sha256.Sum256(nil)
	revision := hex.EncodeToString(digest[:])
	body, _ := json.Marshal(analyticsapi.Report{ContractVersion: analyticsapi.Version, QueryHash: allHash, GenerationID: id.String(),
		Results: []analyticsapi.Result{{Product: "linka-type", Status: "ready"}, {Product: "linka-looks", Status: "ready"}}})
	conn := &analyticsTestConn{rows: []analyticsTestRow{{err: sql.ErrNoRows}, {values: []any{string(body), revision, now.Add(time.Hour), id}}}}
	s := &Store{connection: conn, now: func() time.Time { return now }}
	report, err := s.AnalyticsReport(context.Background(), q)
	if err != nil || report.QueryHash != hash || len(report.Results) != 1 || report.Results[0].Product != "linka-type" {
		t.Fatalf("projected report: %+v %v", report, err)
	}
	if len(conn.queries) != 3 || !reflect.DeepEqual(conn.queryArgs[1], []any{allHash}) || !strings.Contains(conn.queries[2], "privacy_suppressions_v2 FINAL") {
		t.Fatalf("projection must read only exact snapshots and privacy revision: %v %v", conn.queries, conn.queryArgs)
	}
	for _, query := range conn.queries {
		if strings.Contains(query, "expires_at") || strings.Contains(query, "events_v2") || strings.Contains(query, "product_outcomes_v2") {
			t.Fatalf("projected GET queried raw sources/expiry: %s", query)
		}
	}
}

func TestAnalyticsPrivacyAndPublicationCapsExpiry(t *testing.T) {
	q := analyticsTestQuery()
	hash, _ := q.Hash()
	now := time.Now().UTC()
	expiry := now.Add(time.Minute)
	run := analyticsapi.SyncRun{ID: uuid.NewString(), Query: q, QueryHash: hash, State: "running", Version: 2, CreatedAt: now}
	digest := sha256.Sum256([]byte("scope\n"))
	revision := hex.EncodeToString(digest[:])
	conn := &analyticsTestConn{scopes: []string{"scope"}, rows: []analyticsTestRow{analyticsRunRow(run), {values: []any{&expiry}}, {values: []any{uint64(7)}}}}
	s := &Store{connection: conn, now: func() time.Time { return now }}
	report := analyticsapi.Report{ContractVersion: analyticsapi.Version, QueryHash: hash, GenerationID: run.ID,
		Results: []analyticsapi.Result{{Product: "linka-type", Source: "metrics-v2", Status: "ready"}}}
	if err := s.AnalyticsPublish(context.Background(), run, report, revision, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(conn.writes) != 1 || conn.writes[0][5] != sqlTime(expiry) {
		t.Fatalf("expiry not capped: %+v", conn.writes)
	}
	if !strings.Contains(conn.queries[1], "FINAL ORDER BY scope_state") || !strings.Contains(conn.queries[2], "minOrNull(expires_at)") {
		t.Fatalf("privacy query lost FINAL/sort/expiry: %v", conn.queries)
	}
	if got := analyticsStorageError(context.Background(), errors.New("private identifier")); strings.Contains(got.Error(), "private identifier") {
		t.Fatal("storage error exposed source data")
	}
}

func TestAnalyticsNextIncludesInterruptedRuns(t *testing.T) {
	q := analyticsTestQuery()
	hash, _ := q.Hash()
	for _, state := range []string{"queued", "running", "validating", "publishing"} {
		t.Run(state, func(t *testing.T) {
			run := analyticsapi.SyncRun{ID: uuid.NewString(), Query: q, QueryHash: hash, State: state, Version: 2}
			conn := &analyticsTestConn{rows: []analyticsTestRow{analyticsRunRow(run)}}
			s := &Store{connection: conn}
			got, err := s.AnalyticsNext(context.Background())
			if err != nil || got.ID != run.ID || got.State != state {
				t.Fatalf("next: %+v %v", got, err)
			}
			query := conn.queries[0]
			if !strings.Contains(query, "FINAL") || !strings.Contains(query, "ORDER BY created_at, run_id LIMIT 1") || strings.Contains(query, "'completed'") || strings.Contains(query, "'failed'") {
				t.Fatalf("unsafe resume query: %s", query)
			}
		})
	}
}

func TestAnalyticsPublishUsesMonotonicSnapshotRevision(t *testing.T) {
	q := analyticsTestQuery()
	hash, _ := q.Hash()
	now := time.Now().UTC()
	digest := sha256.Sum256(nil)
	privacyRevision := hex.EncodeToString(digest[:])
	for _, tc := range []struct {
		name     string
		max      uint64
		queryErr error
		wantErr  bool
	}{
		{"first", 0, nil, false},
		{"existing", 42, nil, false},
		{"last valid", ^uint64(0) - 1, nil, false},
		{"overflow", ^uint64(0), nil, true},
		{"max query fails", 0, errors.New("private query parameter"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := analyticsapi.SyncRun{ID: uuid.NewString(), Query: q, QueryHash: hash, State: "running", Version: 2}
			conn := &analyticsTestConn{rows: []analyticsTestRow{analyticsRunRow(run), {values: []any{tc.max}, err: tc.queryErr}}}
			s := &Store{connection: conn, now: func() time.Time { return now }}
			report := analyticsapi.Report{ContractVersion: analyticsapi.Version, QueryHash: hash, GenerationID: run.ID}
			err := s.AnalyticsPublish(context.Background(), run, report, privacyRevision, now.Add(time.Hour))
			if (err != nil) != tc.wantErr {
				t.Fatalf("publish error: %v", err)
			}
			if conn.queries[2] != "SELECT max(revision) FROM analytics_snapshots_v1 WHERE query_hash = ?" || !reflect.DeepEqual(conn.queryArgs[2], []any{hash}) {
				t.Fatalf("revision query is not exact-key parameterized: %v %v", conn.queries, conn.queryArgs)
			}
			if tc.wantErr {
				if len(conn.writes) != 0 {
					t.Fatal("revision failure wrote snapshot")
				}
				if strings.Contains(err.Error(), "private query parameter") {
					t.Fatal("revision query error leaked data")
				}
				return
			}
			if len(conn.queries) != 4 || !strings.Contains(conn.queries[3], "valid_until, revision) VALUES") || len(conn.writes) != 1 || conn.writes[0][6] != tc.max+1 {
				t.Fatalf("revision read/insert order or value incorrect: %v %+v", conn.queries, conn.writes)
			}
		})
	}
}

func TestAnalyticsPublishRejectsInvalidatedAndTerminal(t *testing.T) {
	q := analyticsTestQuery()
	hash, _ := q.Hash()
	now := time.Now().UTC()
	digest := sha256.Sum256(nil)
	revision := hex.EncodeToString(digest[:])
	for _, tc := range []struct {
		name, state, revision string
		until                 time.Time
		want                  error
	}{
		{"privacy changed", "running", strings.Repeat("a", 64), now.Add(time.Hour), analyticsapi.ErrInvalidated},
		{"expired", "running", revision, now, analyticsapi.ErrInvalidated},
		{"terminal", "completed", revision, now.Add(time.Hour), analyticsapi.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := analyticsapi.SyncRun{ID: uuid.NewString(), Query: q, QueryHash: hash, State: tc.state, Version: 2}
			conn := &analyticsTestConn{rows: []analyticsTestRow{analyticsRunRow(run), {values: []any{(*time.Time)(nil)}}}}
			s := &Store{connection: conn, now: func() time.Time { return now }}
			run.State = "running"
			report := analyticsapi.Report{ContractVersion: analyticsapi.Version, QueryHash: hash, GenerationID: run.ID}
			if err := s.AnalyticsPublish(context.Background(), run, report, tc.revision, tc.until); !errors.Is(err, tc.want) {
				t.Fatalf("publish: %v", err)
			}
			if len(conn.writes) != 0 {
				t.Fatal("rejected report was published")
			}
		})
	}
}

func TestAnalyticsBackendIntegration(t *testing.T) {
	address := os.Getenv("CLICKHOUSE_INTEGRATION_ADDR")
	if address == "" {
		t.Skip("CLICKHOUSE_INTEGRATION_ADDR is not configured; durable analytics backend not exercised")
	}
	config := Config{Addresses: []string{address}, Database: "linka_metric", Username: os.Getenv("CLICKHOUSE_INTEGRATION_USER"), Password: os.Getenv("CLICKHOUSE_INTEGRATION_PASSWORD")}
	s, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	q := analyticsTestQuery()
	q.Filters = map[string][]string{"app_version": {uuid.NewString()}}
	id := uuid.NewString()
	run, err := s.AnalyticsEnqueue(ctx, q, id)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.AnalyticsEnqueue(ctx, q, id)
	if err != nil || replay.ID != run.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	coalescedRequestID := uuid.NewString()
	coalesced, err := s.AnalyticsEnqueue(ctx, q, coalescedRequestID)
	if err != nil || coalesced.ID != run.ID {
		t.Fatalf("coalesced: %+v %v", coalesced, err)
	}
	changed := q
	changed.Metrics = []string{"sessions"}
	if _, err := s.AnalyticsEnqueue(ctx, changed, id); !errors.Is(err, analyticsapi.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	restarted, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, err := restarted.AnalyticsEnqueue(ctx, changed, coalescedRequestID); !errors.Is(err, analyticsapi.ErrConflict) {
		t.Fatalf("coalesced request conflict after restart: %v", err)
	}
	replayedAlias, err := restarted.AnalyticsEnqueue(ctx, q, coalescedRequestID)
	if err != nil || replayedAlias.ID != run.ID {
		t.Fatalf("coalesced alias replay after restart: %+v %v", replayedAlias, err)
	}
	run.State, run.Version = "running", 2
	if err := s.AnalyticsUpdate(ctx, run); err != nil {
		t.Fatal(err)
	}
	revision, expiry, err := s.AnalyticsPrivacy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().UTC().Add(time.Minute)
	if !expiry.IsZero() && expiry.Before(until) {
		until = expiry
	}
	report := analyticsapi.Report{ContractVersion: analyticsapi.Version, QueryHash: run.QueryHash, GenerationID: run.ID, Status: "ready"}
	publicationTime := time.Now().UTC().Truncate(time.Millisecond)
	s.now = func() time.Time { return publicationTime }
	if err := s.AnalyticsPublish(ctx, run, report, revision, until); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnalyticsReport(ctx, q); err != nil {
		t.Fatal(err)
	}
	run.State, run.Version = "completed", 3
	if err := s.AnalyticsUpdate(ctx, run); err != nil {
		t.Fatal(err)
	}
	replayedAlias, err = restarted.AnalyticsEnqueue(ctx, q, coalescedRequestID)
	if err != nil || replayedAlias.ID != run.ID {
		t.Fatalf("coalesced alias replay retained original run: %+v %v", replayedAlias, err)
	}
	run.State, run.Version = "running", 4
	if err := s.AnalyticsUpdate(ctx, run); !errors.Is(err, analyticsapi.ErrConflict) {
		t.Fatalf("resurrection: %v", err)
	}
	next, err := s.AnalyticsEnqueue(ctx, q, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == run.ID {
		t.Fatal("new generation reused terminal run")
	}
	next.State, next.Version = "running", 2
	if err := s.AnalyticsUpdate(ctx, next); err != nil {
		t.Fatal(err)
	}
	report.GenerationID = next.ID
	if err := s.AnalyticsPublish(ctx, next, report, revision, until); err != nil {
		t.Fatal(err)
	}
	latest, err := s.AnalyticsReport(ctx, q)
	if err != nil || latest.GenerationID != next.ID {
		t.Fatalf("FINAL did not return newer same-clock generation: %+v %v", latest, err)
	}
	var publicationTimes, maxRevision uint64
	if err := s.connection.QueryRow(ctx, `SELECT uniqExact(published_at), max(revision) FROM analytics_snapshots_v1 WHERE query_hash = ?`, run.QueryHash).Scan(&publicationTimes, &maxRevision); err != nil {
		t.Fatal(err)
	}
	if publicationTimes != 1 || maxRevision != 2 {
		t.Fatalf("same-clock publications: timestamps=%d max revision=%d", publicationTimes, maxRevision)
	}
	next.State, next.Version = "completed", 3
	if err := s.AnalyticsUpdate(ctx, next); err != nil {
		t.Fatal(err)
	}
}
