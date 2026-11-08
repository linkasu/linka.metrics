package clickhouse

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"
	"github.com/linkasu/linka.plays-metric/internal/analyticsapi"
)

const analyticsQueryLimit = 16 << 10
const analyticsReportLimit = 2 << 20

var _ analyticsapi.Backend = (*Store)(nil)

func analyticsContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	return ch.Context(ctx, ch.WithSettings(ch.Settings{
		"max_execution_time": 8, "max_threads": 1, "max_memory_usage": 256 << 20,
	})), cancel
}

func analyticsStorageError(ctx context.Context, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return analyticsapi.ErrNotFound
	}
	if ctx.Err() != nil {
		return analyticsapi.ErrBudget
	}
	var exception *ch.Exception
	if errors.As(err, &exception) && (exception.Code == 159 || exception.Code == 241) {
		return analyticsapi.ErrBudget
	}
	// Driver errors may contain parameter values, including privacy identifiers.
	return errors.New("analytics storage unavailable")
}

func analyticsUUID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil || len(value) != 36 || id == uuid.Nil {
		return uuid.Nil, analyticsapi.ErrInvalidQuery
	}
	return id, nil
}

func analyticsDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func analyticsQuery(q analyticsapi.Query) (string, string, error) {
	hash, err := q.Hash()
	if err != nil {
		return "", "", analyticsapi.ErrInvalidQuery
	}
	body, err := json.Marshal(q)
	if err != nil || len(body) > analyticsQueryLimit {
		return "", "", analyticsapi.ErrInvalidQuery
	}
	return hash, string(body), nil
}

func analyticsActive(state string) bool {
	return state == "queued" || state == "running" || state == "validating" || state == "publishing"
}

func (s *Store) AnalyticsEnqueue(ctx context.Context, q analyticsapi.Query, requestID string) (analyticsapi.SyncRun, error) {
	id, err := analyticsUUID(requestID)
	if err != nil {
		return analyticsapi.SyncRun{}, err
	}
	hash, _, err := analyticsQuery(q)
	if err != nil {
		return analyticsapi.SyncRun{}, err
	}
	s.analyticsMu.Lock()
	defer s.analyticsMu.Unlock()
	ctx, cancel := analyticsContext(ctx)
	defer cancel()
	var aliasHash string
	var aliasRunID uuid.UUID
	err = s.connection.QueryRow(ctx, `SELECT query_hash, run_id FROM analytics_request_aliases_v1 FINAL
		WHERE request_id = ? LIMIT 1`, id).Scan(&aliasHash, &aliasRunID)
	if err == nil {
		if aliasHash != hash {
			return analyticsapi.SyncRun{}, analyticsapi.ErrConflict
		}
		run, err := s.AnalyticsRun(ctx, aliasRunID.String())
		if err != nil {
			return analyticsapi.SyncRun{}, err
		}
		if run.QueryHash != hash {
			return analyticsapi.SyncRun{}, analyticsapi.ErrConflict
		}
		return run, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return analyticsapi.SyncRun{}, analyticsStorageError(ctx, err)
	}
	run, err := s.AnalyticsRun(ctx, id.String())
	if err == nil {
		if run.QueryHash != hash {
			return analyticsapi.SyncRun{}, analyticsapi.ErrConflict
		}
		return run, s.analyticsInsertAlias(ctx, id, run)
	}
	if !errors.Is(err, analyticsapi.ErrNotFound) {
		return analyticsapi.SyncRun{}, err
	}
	run, err = s.analyticsReadRun(ctx, `WHERE query_hash = ? AND state IN ('queued', 'running', 'validating', 'publishing') ORDER BY created_at, run_id LIMIT 1`, hash)
	if err == nil {
		return run, s.analyticsInsertAlias(ctx, id, run)
	}
	if !errors.Is(err, analyticsapi.ErrNotFound) {
		return analyticsapi.SyncRun{}, err
	}
	now := s.now().UTC()
	run = analyticsapi.SyncRun{ID: id.String(), QueryHash: hash, State: "queued", Query: q, CreatedAt: now, UpdatedAt: now, Version: 1}
	if err := s.analyticsInsertRun(ctx, run); err != nil {
		return analyticsapi.SyncRun{}, err
	}
	return run, s.analyticsInsertAlias(ctx, id, run)
}

func (s *Store) analyticsInsertAlias(ctx context.Context, requestID uuid.UUID, run analyticsapi.SyncRun) error {
	runID, err := analyticsUUID(run.ID)
	if err != nil {
		return err
	}
	if err := s.connection.Exec(ctx, `INSERT INTO analytics_request_aliases_v1
		(request_id, query_hash, run_id, created_at) VALUES (?, ?, ?, ?)`,
		requestID, run.QueryHash, runID, sqlTime(s.now().UTC())); err != nil {
		return analyticsStorageError(ctx, err)
	}
	return nil
}

func (s *Store) AnalyticsRun(ctx context.Context, requestID string) (analyticsapi.SyncRun, error) {
	id, err := analyticsUUID(requestID)
	if err != nil {
		return analyticsapi.SyncRun{}, err
	}
	ctx, cancel := analyticsContext(ctx)
	defer cancel()
	return s.analyticsReadRun(ctx, `WHERE run_id = ? LIMIT 1`, id)
}

func (s *Store) AnalyticsNext(ctx context.Context) (analyticsapi.SyncRun, error) {
	ctx, cancel := analyticsContext(ctx)
	defer cancel()
	return s.analyticsReadRun(ctx, `WHERE state IN ('queued', 'running', 'validating', 'publishing') ORDER BY created_at, run_id LIMIT 1`)
}

func (s *Store) analyticsReadRun(ctx context.Context, condition string, args ...any) (analyticsapi.SyncRun, error) {
	var run analyticsapi.SyncRun
	var id uuid.UUID
	var body string
	err := s.connection.QueryRow(ctx, `SELECT run_id, query_hash, state,
		if(length(query_json) <= 16384, query_json, ''), reason, created_at, updated_at, version
		FROM analytics_requests_v1 FINAL `+condition, args...).Scan(
		&id, &run.QueryHash, &run.State, &body, &run.Reason, &run.CreatedAt, &run.UpdatedAt, &run.Version)
	if err != nil {
		return run, analyticsStorageError(ctx, err)
	}
	run.ID = id.String()
	if len(body) > analyticsQueryLimit || json.Unmarshal([]byte(body), &run.Query) != nil {
		return analyticsapi.SyncRun{}, errors.New("invalid stored analytics query")
	}
	hash, _, err := analyticsQuery(run.Query)
	if err != nil || hash != run.QueryHash || !analyticsDigest(run.QueryHash) {
		return analyticsapi.SyncRun{}, errors.New("invalid stored analytics query")
	}
	return run, nil
}

func (s *Store) AnalyticsUpdate(ctx context.Context, run analyticsapi.SyncRun) error {
	if _, err := analyticsUUID(run.ID); err != nil {
		return err
	}
	hash, _, err := analyticsQuery(run.Query)
	if err != nil || hash != run.QueryHash || run.Version < 2 || run.State == "queued" ||
		(!analyticsActive(run.State) && run.State != "completed" && run.State != "failed" && run.State != "cancelled") {
		return analyticsapi.ErrInvalidQuery
	}
	s.analyticsMu.Lock()
	defer s.analyticsMu.Unlock()
	ctx, cancel := analyticsContext(ctx)
	defer cancel()
	stored, err := s.AnalyticsRun(ctx, run.ID)
	if err != nil {
		return err
	}
	if !analyticsActive(stored.State) || stored.QueryHash != run.QueryHash || run.Version <= stored.Version {
		return analyticsapi.ErrConflict
	}
	run.CreatedAt = stored.CreatedAt
	run.UpdatedAt = s.now().UTC()
	return s.analyticsInsertRun(ctx, run)
}

func (s *Store) analyticsInsertRun(ctx context.Context, run analyticsapi.SyncRun) error {
	_, body, err := analyticsQuery(run.Query)
	if err != nil {
		return err
	}
	id, err := analyticsUUID(run.ID)
	if err != nil {
		return err
	}
	if err := s.connection.Exec(ctx, `INSERT INTO analytics_requests_v1
		(run_id, query_hash, state, query_json, reason, created_at, updated_at, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, run.QueryHash, run.State, body, run.Reason, sqlTime(run.CreatedAt), sqlTime(run.UpdatedAt), run.Version); err != nil {
		return analyticsStorageError(ctx, err)
	}
	return nil
}

func analyticsDecodeReport(body, hash, revision, liveRevision string, validUntil, now time.Time) (analyticsapi.Report, error) {
	if !now.Before(validUntil) || !analyticsDigest(revision) || revision != liveRevision {
		return analyticsapi.Report{}, analyticsapi.ErrInvalidated
	}
	var report analyticsapi.Report
	if len(body) > analyticsReportLimit || json.Unmarshal([]byte(body), &report) != nil ||
		report.ContractVersion != analyticsapi.Version || report.QueryHash != hash || !analyticsDigest(hash) {
		return analyticsapi.Report{}, errors.New("invalid stored analytics report")
	}
	return report, nil
}

func (s *Store) AnalyticsReport(ctx context.Context, q analyticsapi.Query) (analyticsapi.Report, error) {
	hash, _, err := analyticsQuery(q)
	if err != nil {
		return analyticsapi.Report{}, err
	}
	ctx, cancel := analyticsContext(ctx)
	defer cancel()
	var body, revision string
	var validUntil time.Time
	var generation uuid.UUID
	sourceHash := hash
	read := func(key string) error {
		return s.connection.QueryRow(ctx, `SELECT if(length(report_json) <= 2097152, report_json, ''), privacy_revision, valid_until, generation_id
			FROM analytics_snapshots_v1 FINAL WHERE query_hash = ? LIMIT 1`, key).Scan(&body, &revision, &validUntil, &generation)
	}
	err = read(hash)
	if errors.Is(err, sql.ErrNoRows) && len(q.Products) < len(analyticsapi.Products()) {
		// Product-local results can be projected from the exact same all-product
		// slice; distinct counts are neither summed nor re-aggregated here.
		all := q
		all.Products = nil
		for _, product := range analyticsapi.Products() {
			all.Products = append(all.Products, product.ID)
		}
		sourceHash, err = all.Hash()
		if err != nil {
			return analyticsapi.Report{}, err
		}
		err = read(sourceHash)
	}
	if err != nil {
		return analyticsapi.Report{}, analyticsStorageError(ctx, err)
	}
	live, err := s.analyticsPrivacyRevision(ctx)
	if err != nil {
		return analyticsapi.Report{}, err
	}
	report, err := analyticsDecodeReport(body, sourceHash, revision, live, validUntil, s.now().UTC())
	if err == nil && (generation == uuid.Nil || report.GenerationID != generation.String()) {
		return analyticsapi.Report{}, errors.New("invalid stored analytics generation")
	}
	if err == nil && sourceHash != hash {
		allowed := make(map[string]bool, len(q.Products))
		for _, product := range q.Products {
			allowed[product] = true
		}
		var results []analyticsapi.Result
		for _, result := range report.Results {
			if allowed[result.Product] {
				results = append(results, result)
			}
		}
		var capabilities []analyticsapi.Capability
		for _, capability := range report.Capabilities {
			if allowed[capability.Product] {
				capabilities = append(capabilities, capability)
			}
		}
		var insights []analyticsapi.Insight
		for _, insight := range report.Insights {
			if insight.Product != "" && allowed[insight.Product] {
				insights = append(insights, insight)
			}
		}
		report.QueryHash, report.Results, report.Capabilities, report.Insights = hash, results, capabilities, insights
		report.Status = analyticsResultsStatus(results)
	}
	return report, err
}

func (s *Store) AnalyticsPublish(ctx context.Context, run analyticsapi.SyncRun, report analyticsapi.Report, revision string, validUntil time.Time) error {
	id, err := analyticsUUID(run.ID)
	if err != nil {
		return err
	}
	if (run.State != "running" && run.State != "publishing") || !analyticsDigest(run.QueryHash) || !analyticsDigest(revision) || report.ContractVersion != analyticsapi.Version ||
		report.QueryHash != run.QueryHash || report.GenerationID != id.String() {
		return analyticsapi.ErrInvalidQuery
	}
	body, err := json.Marshal(report)
	if err != nil || len(body) > analyticsReportLimit {
		return analyticsapi.ErrInvalidQuery
	}
	s.analyticsMu.Lock()
	defer s.analyticsMu.Unlock()
	ctx, cancel := analyticsContext(ctx)
	defer cancel()
	stored, err := s.AnalyticsRun(ctx, run.ID)
	if err != nil {
		return err
	}
	if stored.QueryHash != run.QueryHash || (stored.State != "publishing" && stored.State != "running") {
		return analyticsapi.ErrConflict
	}
	live, err := s.analyticsPrivacyRevision(ctx)
	if err != nil {
		return err
	}
	var expiry time.Time
	for _, result := range report.Results {
		if result.Source == "metrics-v2" && result.Status == "ready" {
			expiry, err = s.analyticsQueryExpiry(ctx, run.Query)
			if err != nil {
				return err
			}
			break
		}
	}
	now := s.now().UTC()
	if !expiry.IsZero() && expiry.Before(validUntil) {
		validUntil = expiry
	}
	if live != revision || !now.Before(validUntil) {
		return analyticsapi.ErrInvalidated
	}
	var snapshotRevision uint64
	if err := s.connection.QueryRow(ctx, `SELECT max(revision) FROM analytics_snapshots_v1 WHERE query_hash = ?`, run.QueryHash).Scan(&snapshotRevision); err != nil {
		return analyticsStorageError(ctx, err)
	}
	if snapshotRevision == ^uint64(0) {
		return errors.New("analytics snapshot revision exhausted")
	}
	if err := s.connection.Exec(ctx, `INSERT INTO analytics_snapshots_v1
		(query_hash, generation_id, report_json, privacy_revision, published_at, valid_until, revision) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		run.QueryHash, id, string(body), revision, sqlTime(now), sqlTime(validUntil.UTC()), snapshotRevision+1); err != nil {
		return analyticsStorageError(ctx, err)
	}
	return nil
}

func (s *Store) analyticsQueryExpiry(ctx context.Context, query analyticsapi.Query) (time.Time, error) {
	from, _ := time.Parse(time.DateOnly, query.SeriesFrom)
	to, _ := time.Parse(time.DateOnly, query.SeriesTo)
	cutoff, _ := time.Parse(time.DateOnly, query.CutoffDate)
	to, cutoff = to.AddDate(0, 0, 1), cutoff.AddDate(0, 0, 1)
	for _, metric := range query.Metrics {
		if metric == "calendar_active" {
			monthFrom := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
			lastMonth := time.Date(cutoff.Year(), cutoff.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
			if lastMonth.Before(monthFrom) {
				monthFrom = lastMonth
			}
			if monthFrom.Before(from) {
				from = monthFrom
			}
			break
		}
	}
	parts := make([]string, 0, 4)
	var args []any
	for _, table := range []string{"common_events_v2", "plays_events_v2", "product_events_v2", "product_outcomes_v2"} {
		parts = append(parts, `SELECT expires_at FROM `+table+` WHERE expires_at > ? AND product IN (?) AND ((occurred_at >= ? AND occurred_at < ?) OR (occurred_at >= ? AND occurred_at < ?))`)
		args = append(args, s.now().UTC(), query.Products, from, to, cutoff.AddDate(0, 0, -30), cutoff)
	}
	var expiry *time.Time
	if err := s.connection.QueryRow(ctx, "SELECT minOrNull(expires_at) FROM ("+strings.Join(parts, " UNION ALL ")+")", args...).Scan(&expiry); err != nil {
		return time.Time{}, analyticsStorageError(ctx, err)
	}
	if expiry != nil {
		return expiry.UTC(), nil
	}
	return time.Time{}, nil
}

func (s *Store) AnalyticsPrivacy(ctx context.Context) (string, time.Time, error) {
	ctx, cancel := analyticsContext(ctx)
	defer cancel()
	revision, err := s.analyticsPrivacyRevision(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	// Source expiry is checked by the serial publisher, not by dashboard reads.
	var expiry *time.Time
	now := s.now().UTC()
	err = s.connection.QueryRow(ctx, `SELECT minOrNull(expires_at) FROM (
		SELECT expires_at FROM common_events_v2 WHERE expires_at > ?
		UNION ALL SELECT expires_at FROM plays_events_v2 WHERE expires_at > ?
		UNION ALL SELECT expires_at FROM product_events_v2 WHERE expires_at > ?
		UNION ALL SELECT expires_at FROM product_outcomes_v2 WHERE expires_at > ?
	)`, now, now, now, now).Scan(&expiry)
	if err != nil {
		return "", time.Time{}, analyticsStorageError(ctx, err)
	}
	var validUntil time.Time
	if expiry != nil {
		validUntil = expiry.UTC()
	}
	return revision, validUntil, nil
}

func (s *Store) analyticsPrivacyRevision(ctx context.Context) (string, error) {
	ctx, cancel := analyticsContext(ctx)
	defer cancel()
	// Only suppression scope/state is hashed; source event payloads never leave ClickHouse.
	rows, err := s.connection.Query(ctx, `SELECT scope_state FROM (SELECT toJSONString(tuple(product, product_key, subject_key, person_key,
		org_key, active, status, action, ingested_at, updated_at, legacy_installation_id)) AS scope_state
		FROM privacy_suppressions_v2 FINAL
		UNION ALL SELECT toJSONString(tuple('consent', product, subject_key, consent_epoch, version, preference)) AS scope_state
		FROM consent_ledger_v2 FINAL WHERE applied = true) ORDER BY scope_state`)
	if err != nil {
		return "", analyticsStorageError(ctx, err)
	}
	defer rows.Close()
	digest := sha256.New()
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			return "", analyticsStorageError(ctx, err)
		}
		_, _ = digest.Write([]byte(scope))
		_, _ = digest.Write([]byte{'\n'})
	}
	if err := rows.Err(); err != nil {
		return "", analyticsStorageError(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return "", analyticsStorageError(ctx, err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
