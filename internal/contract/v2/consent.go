package v2

import (
	"errors"
	"regexp"
	"time"

	"github.com/linkasu/linka.plays-metric/internal/jsonstrict"
	"github.com/linkasu/linka.plays-metric/internal/product"
)

// ConsentScope deliberately excludes person/org keys and the batch epoch field.
type ConsentScope struct {
	Product    product.ID `json:"product"`
	SubjectKey string     `json:"subject_key"`
}

type ConsentRequest struct {
	SchemaVersion int          `json:"schema_version"`
	RequestID     string       `json:"request_id"`
	Scope         ConsentScope `json:"scope"`
	ConsentEpoch  uint64       `json:"consent_epoch"`
	Version       uint64       `json:"version"`
	Preference    string       `json:"preference"`
	RecordedAt    string       `json:"recorded_at"`
}

type ValidatedConsentRequest struct {
	ConsentRequest
	ProductKey string
}

type ConsentResult struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Replayed  bool   `json:"replayed"`
}

var consentTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}(Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$`)

func ParseConsentRequest(data []byte) (ValidatedConsentRequest, error) {
	var request ConsentRequest
	if len(data) > 16*1024 {
		return ValidatedConsentRequest{}, errors.New("consent request exceeds maximum size")
	}
	if err := jsonstrict.DecodeObject(data, &request, MaxJSONDepth); err != nil {
		return ValidatedConsentRequest{}, err
	}
	if request.SchemaVersion != SchemaVersion || validateUUID(request.RequestID) != nil || request.ConsentEpoch == 0 || request.Version == 0 || (request.Preference != "allowed" && request.Preference != "denied") {
		return ValidatedConsentRequest{}, errors.New("invalid consent request")
	}
	spec, err := validateScope(Scope{Product: request.Scope.Product, SubjectKey: request.Scope.SubjectKey})
	if err != nil {
		return ValidatedConsentRequest{}, err
	}
	_, err = time.Parse(time.RFC3339Nano, request.RecordedAt)
	if err != nil || !consentTimestamp.MatchString(request.RecordedAt) {
		return ValidatedConsentRequest{}, errors.New("recorded_at must be RFC3339 milliseconds")
	}
	// Version, not the client clock, orders durable controls and delayed retries.
	return ValidatedConsentRequest{ConsentRequest: request, ProductKey: spec.OpaqueKey}, nil
}
