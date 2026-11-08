package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/product"
)

const productTokenVersion = "v2"

type ProductClaims struct {
	ConsentEpoch *uint64
	Product      product.ID
	SubjectKey   string
	PersonKey    *string
	OrgKey       *string
	IssuedAt     time.Time
	ExpiresAt    time.Time
}

type ProductTokens struct {
	active        ServiceKey
	previous      *ServiceKey
	subjectSecret []byte
	ttl           time.Duration
	now           func() time.Time
}

func NewProductTokens(active ServiceKey, previous *ServiceKey, ttl time.Duration) (*ProductTokens, error) {
	return NewProductTokensWithSubjectSecret(active, previous, active.Secret, ttl)
}

func NewProductTokensWithSubjectSecret(active ServiceKey, previous *ServiceKey, subjectSecret []byte, ttl time.Duration) (*ProductTokens, error) {
	if err := validateServiceKey(active); err != nil {
		return nil, fmt.Errorf("active product token key: %w", err)
	}
	if previous != nil {
		if err := validateServiceKey(*previous); err != nil {
			return nil, fmt.Errorf("previous product token key: %w", err)
		}
		if previous.ID == active.ID {
			return nil, errors.New("active and previous product token key IDs must differ")
		}
	}
	if ttl <= 0 || ttl > 365*24*time.Hour {
		return nil, errors.New("product token TTL must be between zero and one year")
	}
	if len(subjectSecret) < 32 {
		return nil, errors.New("subject HMAC secret must contain at least 32 bytes")
	}
	return &ProductTokens{
		active: cloneServiceKey(active), previous: cloneServiceKeyPointer(previous), subjectSecret: append([]byte(nil), subjectSecret...), ttl: ttl, now: time.Now,
	}, nil
}

func (t *ProductTokens) IssueAnonymous(productID product.ID, installationID string) (ProductClaims, string, error) {
	if _, ok := product.Lookup(productID); !ok {
		return ProductClaims{}, "", errors.New("unknown product")
	}
	mac := hmac.New(sha256.New, t.subjectSecret)
	_, _ = io.WriteString(mac, "linka-product-subject-v2\n"+string(productID)+"\n"+installationID)
	return t.issue(ProductClaims{Product: productID, SubjectKey: hex.EncodeToString(mac.Sum(nil))})
}

// IssueScope is intentionally not exposed by an HTTP handler. A trusted Identity
// integration may call it only with already-derived opaque keys.
func (t *ProductTokens) IssueScope(claims ProductClaims) (ProductClaims, string, error) {
	return t.issue(claims)
}

func (t *ProductTokens) Verify(token string) (ProductClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 9 || parts[0] != productTokenVersion {
		return ProductClaims{}, errors.New("invalid product token")
	}
	key, ok := t.key(parts[1])
	if !ok {
		return ProductClaims{}, errors.New("invalid product token")
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[8])
	if err != nil {
		return ProductClaims{}, errors.New("invalid product token")
	}
	expected := signProductToken(key.Secret, strings.Join(parts[:8], "."))
	if !hmac.Equal(provided, expected) {
		return ProductClaims{}, errors.New("invalid product token")
	}
	claims := ProductClaims{Product: product.ID(parts[2]), SubjectKey: parts[3]}
	if _, ok := product.Lookup(claims.Product); !ok || !opaqueKey(parts[3]) {
		return ProductClaims{}, errors.New("invalid product token")
	}
	claims.PersonKey, err = decodeOptionalOpaque(parts[4])
	if err != nil {
		return ProductClaims{}, errors.New("invalid product token")
	}
	claims.OrgKey, err = decodeOptionalOpaque(parts[5])
	if err != nil {
		return ProductClaims{}, errors.New("invalid product token")
	}
	issuedUnix, err := strconv.ParseInt(parts[6], 10, 64)
	if err != nil {
		return ProductClaims{}, errors.New("invalid product token")
	}
	expiresUnix, err := strconv.ParseInt(parts[7], 10, 64)
	if err != nil {
		return ProductClaims{}, errors.New("invalid product token")
	}
	claims.IssuedAt = time.Unix(issuedUnix, 0).UTC()
	claims.ExpiresAt = time.Unix(expiresUnix, 0).UTC()
	now := t.now()
	if claims.IssuedAt.After(now.Add(5*time.Minute)) || !claims.ExpiresAt.After(now) || !claims.ExpiresAt.After(claims.IssuedAt) {
		return ProductClaims{}, errors.New("invalid product token")
	}
	return claims, nil
}

func (t *ProductTokens) issue(claims ProductClaims) (ProductClaims, string, error) {
	if claims.ConsentEpoch != nil {
		return ProductClaims{}, "", errors.New("legacy product tokens cannot carry consent epochs")
	}
	if _, ok := product.Lookup(claims.Product); !ok || !opaqueKey(claims.SubjectKey) {
		return ProductClaims{}, "", errors.New("invalid product token scope")
	}
	for _, key := range []*string{claims.PersonKey, claims.OrgKey} {
		if key != nil && !opaqueKey(*key) {
			return ProductClaims{}, "", errors.New("identity scope keys must be opaque lowercase SHA-256 values")
		}
	}
	claims.IssuedAt = t.now().UTC().Truncate(time.Second)
	claims.ExpiresAt = claims.IssuedAt.Add(t.ttl)
	unsigned := strings.Join([]string{
		productTokenVersion,
		t.active.ID,
		string(claims.Product),
		claims.SubjectKey,
		encodeOptionalOpaque(claims.PersonKey),
		encodeOptionalOpaque(claims.OrgKey),
		strconv.FormatInt(claims.IssuedAt.Unix(), 10),
		strconv.FormatInt(claims.ExpiresAt.Unix(), 10),
	}, ".")
	signature := base64.RawURLEncoding.EncodeToString(signProductToken(t.active.Secret, unsigned))
	return claims, unsigned + "." + signature, nil
}

func (t *ProductTokens) key(id string) (ServiceKey, bool) {
	if id == t.active.ID {
		return t.active, true
	}
	if t.previous != nil && id == t.previous.ID {
		return *t.previous, true
	}
	return ServiceKey{}, false
}

func signProductToken(secret []byte, unsigned string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = io.WriteString(mac, "linka-product-token-v2\n"+unsigned)
	return mac.Sum(nil)
}

func opaqueKey(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func encodeOptionalOpaque(value *string) string {
	if value == nil {
		return "-"
	}
	return *value
}

func decodeOptionalOpaque(value string) (*string, error) {
	if value == "-" {
		return nil, nil
	}
	if !opaqueKey(value) {
		return nil, errors.New("invalid opaque key")
	}
	return &value, nil
}
