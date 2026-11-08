package clickhouse

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	v2 "github.com/linkasu/linka.plays-metric/internal/contract/v2"
	"github.com/linkasu/linka.plays-metric/internal/product"
)

func TestConsentTransitionsAndBatchGates(t *testing.T) {
	unknown := consentState{}
	allowed1 := consentState{Epoch: 1, Version: 1, Preference: "allowed"}
	denied1 := consentState{Epoch: 1, Version: 2, Preference: "denied"}
	allowed2 := consentState{Epoch: 2, Version: 3, Preference: "allowed"}
	for _, tc := range []struct {
		name              string
		state             consentState
		epoch, version    uint64
		preference        string
		applied, conflict bool
	}{
		{"initial allow", unknown, 1, 1, "allowed", true, false},
		{"initial deny", unknown, 1, 1, "denied", true, false},
		{"revoke", allowed1, 1, 2, "denied", true, false},
		{"reenable new epoch", denied1, 2, 3, "allowed", true, false},
		{"reopen forbidden", denied1, 1, 3, "allowed", false, true},
		{"same epoch allowed revision refresh", allowed1, 1, 2, "allowed", true, false},
		{"stale grant acknowledged", denied1, 1, 1, "allowed", false, false},
		{"stale revoke acknowledged", allowed2, 1, 2, "denied", false, false},
		{"equal state acknowledged", denied1, 1, 2, "denied", false, false},
		{"equal version conflicting state", denied1, 2, 2, "allowed", false, true},
		{"epoch regression", allowed2, 1, 4, "denied", false, true},
		{"allowed epoch regression", allowed2, 1, 4, "allowed", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applied, err := consentTransition(tc.state, v2.ConsentRequest{ConsentEpoch: tc.epoch, Version: tc.version, Preference: tc.preference})
			if applied != tc.applied || errors.Is(err, v2.ErrConsentConflict) != tc.conflict {
				t.Fatalf("applied=%v err=%v", applied, err)
			}
		})
	}
	zero, one, two := uint64(0), uint64(1), uint64(2)
	for _, tc := range []struct {
		state   consentState
		epoch   *uint64
		allowed bool
	}{
		{unknown, nil, true}, {unknown, &one, false}, {unknown, &zero, false},
		{allowed1, nil, false}, {allowed1, &one, true}, {denied1, &one, false},
		{allowed2, &one, false}, {allowed2, &two, true}, {allowed2, nil, false},
	} {
		if got := consentAllowsBatch(tc.state, tc.epoch); got != tc.allowed {
			t.Errorf("batch gate %+v epoch=%v: %v", tc.state, tc.epoch, got)
		}
	}
}

func TestConsentReceiptsAreAtomicAndStaleControlsAreDurableNoOps(t *testing.T) {
	request := v2.ValidatedConsentRequest{ConsentRequest: v2.ConsentRequest{
		RequestID: "10000000-0000-4000-8000-000000000001", Scope: v2.ConsentScope{Product: product.LinkaPictures, SubjectKey: strings.Repeat("a", 64)},
		ConsentEpoch: 1, Version: 2, Preference: "denied", RecordedAt: "2026-11-08T12:00:00.000Z"}}
	for _, tc := range []struct {
		name                    string
		rows                    []analyticsTestRow
		replay, writes, applied bool
		want                    error
	}{
		{"new", []analyticsTestRow{{err: sql.ErrNoRows}, {values: []any{uint64(1), uint64(1), "allowed"}}}, false, true, true, nil},
		{"stale revoke", []analyticsTestRow{{err: sql.ErrNoRows}, {values: []any{uint64(2), uint64(3), "allowed"}}}, false, true, false, nil},
		{"replay receipt", []analyticsTestRow{{values: []any{"body"}}}, true, false, false, nil},
		{"different body", []analyticsTestRow{{values: []any{"different"}}}, false, false, false, v2.ErrIdempotencyConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &analyticsTestConn{rows: tc.rows}
			store := &Store{connection: conn, now: time.Now}
			result, err := store.CreateConsent(t.Context(), request, "body")
			if !errors.Is(err, tc.want) || result.Replayed != tc.replay || (len(conn.writes) == 1) != tc.writes {
				t.Fatalf("result=%+v err=%v writes=%v", result, err, conn.writes)
			}
			if tc.writes && conn.writes[0][9] != tc.applied {
				t.Fatalf("stale receipt changed live state: %+v", conn.writes[0])
			}
		})
	}
	conn := &analyticsTestConn{rows: []analyticsTestRow{{err: sql.ErrNoRows}, {err: sql.ErrNoRows}, {err: sql.ErrNoRows}}, execErr: errors.New("disk failure")}
	store := &Store{connection: conn, now: time.Now}
	request.Preference = "allowed"
	if result, err := store.CreateConsent(t.Context(), request, "body"); err == nil || result.Status == "completed" {
		t.Fatal("acknowledged failed durable write")
	}
}

func TestCompletedBatchReplayCannotBypassRevocation(t *testing.T) {
	epoch := uint64(1)
	batch := v2.ValidatedBatch{Header: v2.BatchHeader{BatchID: "10000000-0000-4000-8000-000000000001", Scope: v2.Scope{Product: product.LinkaPictures, SubjectKey: strings.Repeat("a", 64), ConsentEpoch: &epoch}}}
	for _, preference := range []string{"allowed", "denied"} {
		conn := &analyticsTestConn{rows: []analyticsTestRow{
			{values: []any{"body", "completed", time.Now(), (*time.Time)(nil)}}, {err: sql.ErrNoRows},
			{values: []any{epoch, uint64(2), preference}},
		}}
		store := &Store{connection: conn, now: time.Now}
		result, err := store.InsertV2(t.Context(), batch, "body")
		if preference == "denied" && !errors.Is(err, v2.ErrSuppressed) {
			t.Fatalf("revoked replay: %+v %v", result, err)
		}
		if preference == "allowed" && (err != nil || !result.Replayed) {
			t.Fatalf("active replay: %+v %v", result, err)
		}
		if len(conn.writes) != 0 {
			t.Fatal("completed replay wrote events")
		}
	}
}
