package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/product"
)

func TestProductTokenIsProductScopedAndOpaque(t *testing.T) {
	manager, err := NewProductTokens(ServiceKey{ID: "current", Secret: []byte(strings.Repeat("p", 32))}, nil, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	installationID := "20000000-0000-4000-8000-000000000002"
	issued, token, err := manager.IssueAnonymous(product.LinkaPlays, installationID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token, installationID) || len(issued.SubjectKey) != 64 {
		t.Fatal("product token exposed a raw installation identifier")
	}
	verified, err := manager.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Product != product.LinkaPlays || verified.SubjectKey != issued.SubjectKey {
		t.Fatalf("verified claims = %+v", verified)
	}
}

func TestLegacyProductTokenCannotSilentlyDropConsentEpoch(t *testing.T) {
	manager, err := NewProductTokens(ServiceKey{ID: "test", Secret: []byte(strings.Repeat("s", 32))}, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	epoch := uint64(1)
	if _, _, err := manager.IssueScope(ProductClaims{Product: product.LinkaPictures, SubjectKey: strings.Repeat("a", 64), ConsentEpoch: &epoch}); err == nil {
		t.Fatal("legacy token silently discarded a consent epoch")
	}
}

func TestProductTokenAcceptsPreviousKey(t *testing.T) {
	oldKey := ServiceKey{ID: "old", Secret: []byte(strings.Repeat("o", 32))}
	oldManager, err := NewProductTokens(oldKey, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := oldManager.IssueAnonymous(product.LinkaPlays, "20000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	newManager, err := NewProductTokens(ServiceKey{ID: "new", Secret: []byte(strings.Repeat("n", 32))}, &oldKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newManager.Verify(token); err != nil {
		t.Fatal(err)
	}
}

func TestSubjectKeyDoesNotChangeWithSigningKeyRotation(t *testing.T) {
	subjectSecret := []byte(strings.Repeat("s", 32))
	oldManager, err := NewProductTokensWithSubjectSecret(
		ServiceKey{ID: "old", Secret: []byte(strings.Repeat("o", 32))}, nil, subjectSecret, time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	newManager, err := NewProductTokensWithSubjectSecret(
		ServiceKey{ID: "new", Secret: []byte(strings.Repeat("n", 32))}, nil, subjectSecret, time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	oldClaims, _, err := oldManager.IssueAnonymous(product.LinkaPlays, "20000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	newClaims, _, err := newManager.IssueAnonymous(product.LinkaPlays, "20000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	if oldClaims.SubjectKey != newClaims.SubjectKey {
		t.Fatal("signing key rotation changed the stable subject key")
	}
}
