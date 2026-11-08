package clickhouse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
)

type consentState struct {
	Epoch      uint64
	Version    uint64
	Preference string
}

func (s *Store) consentState(ctx context.Context, scope v2.Scope) (consentState, error) {
	var state consentState
	err := s.connection.QueryRow(ctx, `SELECT consent_epoch, version, preference FROM consent_ledger_v2
		WHERE product = ? AND subject_key = ? AND applied = true ORDER BY version DESC LIMIT 1`, string(scope.Product), scope.SubjectKey).Scan(&state.Epoch, &state.Version, &state.Preference)
	if errors.Is(err, sql.ErrNoRows) {
		return consentState{}, nil
	}
	if err != nil {
		// InsertV2 callers log errors; driver exceptions can contain bound keys.
		return consentState{}, errors.New("query consent state failed")
	}
	return state, nil
}

func consentTransition(state consentState, request v2.ConsentRequest) (bool, error) {
	if request.Version < state.Version {
		return false, nil
	}
	if request.Version == state.Version {
		if request.ConsentEpoch == state.Epoch && request.Preference == state.Preference {
			return false, nil
		}
		return false, v2.ErrConsentConflict
	}
	// An allowed epoch may refresh its revision, but a revoked epoch stays closed.
	if request.ConsentEpoch < state.Epoch || (request.Preference == "allowed" && request.ConsentEpoch == state.Epoch && state.Preference == "denied") {
		return false, v2.ErrConsentConflict
	}
	return true, nil
}

func consentAllowsBatch(state consentState, epoch *uint64) bool {
	if epoch == nil {
		return state.Version == 0
	}
	return *epoch > 0 && state.Version > 0 && state.Epoch == *epoch && state.Preference == "allowed"
}

func (s *Store) CreateConsent(ctx context.Context, request v2.ValidatedConsentRequest, bodySHA string) (v2.ConsentResult, error) {
	s.v2Mu.Lock()
	defer s.v2Mu.Unlock()
	result := v2.ConsentResult{RequestID: request.RequestID, Status: "completed"}
	var existingSHA string
	err := s.connection.QueryRow(ctx, `SELECT body_sha256 FROM consent_ledger_v2 WHERE request_id = ? LIMIT 1`, uuid.MustParse(request.RequestID)).Scan(&existingSHA)
	if err == nil {
		if existingSHA != bodySHA {
			return v2.ConsentResult{}, v2.ErrIdempotencyConflict
		}
		// Replaying a receipt never executes its historical transition again.
		result.Replayed = true
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return v2.ConsentResult{}, fmt.Errorf("query consent receipt: %w", err)
	}
	scope := v2.Scope{Product: request.Scope.Product, SubjectKey: request.Scope.SubjectKey}
	state, err := s.consentState(ctx, scope)
	if err != nil {
		return v2.ConsentResult{}, err
	}
	applied, err := consentTransition(state, request.ConsentRequest)
	if err != nil {
		return v2.ConsentResult{}, err
	}
	if applied && request.Preference == "allowed" {
		suppressed, err := s.isSuppressed(ctx, scope, request.ProductKey)
		if err != nil {
			return v2.ConsentResult{}, err
		}
		if suppressed {
			return v2.ConsentResult{}, v2.ErrSuppressed
		}
	}
	// One durable row is both receipt and transition; there is no partial commit.
	err = s.connection.Exec(ctx, `INSERT INTO consent_ledger_v2
		(request_id, product, subject_key, consent_epoch, version, preference, body_sha256, recorded_at, ingested_at, applied)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, uuid.MustParse(request.RequestID), string(scope.Product), scope.SubjectKey,
		request.ConsentEpoch, request.Version, request.Preference, bodySHA, request.RecordedAt, sqlTime(s.now().UTC()), applied)
	if err != nil {
		return v2.ConsentResult{}, fmt.Errorf("persist consent: %w", err)
	}
	return result, nil
}
