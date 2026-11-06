// Package analyticsapi defines the versioned, aggregate-only CRM read contract.
// A result's identity includes its source, methodology and unit; identifiers
// from different products or sources must never be summed as unique people.
package analyticsapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
)

const Version = 1

type Product struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func Products() []Product {
	return []Product{{"linka-type", "Напиши"}, {"linka-looks", "Смотри"}, {"linka-pictures", "Покажи"}, {"linka-paperboard", "Paper Board"}, {"linka-plays", "Plays"}, {"linka-tts", "TTS"}}
}

// Query uses inclusive source-calendar dates; each result reports the source
// timezone and exact half-open timestamp window independently.
type Query struct {
	ContractVersion int                 `json:"contract_version"`
	Products        []string            `json:"products"`
	Metrics         []string            `json:"metrics"`
	CutoffDate      string              `json:"cutoff_date"`
	SeriesFrom      string              `json:"series_from"`
	SeriesTo        string              `json:"series_to"`
	Dimensions      []string            `json:"dimensions,omitempty"`
	Filters         map[string][]string `json:"filters,omitempty"`
	Methodology     string              `json:"methodology,omitempty"`
}

type Point struct {
	Date         string `json:"date"`
	Value        *int64 `json:"value"`
	Status       string `json:"status"`
	Contributors *int64 `json:"contributors,omitempty"`
}

type Result struct {
	Product        string    `json:"product"`
	Metric         string    `json:"metric"`
	Unit           string    `json:"unit"`
	Source         string    `json:"source"`
	Methodology    string    `json:"methodology"`
	Timezone       string    `json:"timezone"`
	WindowFrom     time.Time `json:"window_from"`
	WindowTo       time.Time `json:"window_to"`
	Value          *int64    `json:"value"`
	Contributors   *int64    `json:"contributors,omitempty"`
	Previous       *int64    `json:"previous,omitempty"`
	Status         string    `json:"status"`
	Reason         string    `json:"reason,omitempty"`
	Approximate    bool      `json:"approximate"`
	PopulationNote string    `json:"population_note"`
	Series         []Point   `json:"series,omitempty"`
}

type Capability struct {
	Product     string   `json:"product"`
	Source      string   `json:"source"`
	Methodology string   `json:"methodology"`
	Unit        string   `json:"unit"`
	Metrics     []string `json:"metrics"`
	Dimensions  []string `json:"dimensions"`
	Status      string   `json:"status"`
	Reason      string   `json:"reason,omitempty"`
}

type Insight struct {
	Kind    string `json:"kind"`
	Product string `json:"product"`
	Metric  string `json:"metric,omitempty"`
	Message string `json:"message"`
}

type Report struct {
	ContractVersion int          `json:"contract_version"`
	QueryHash       string       `json:"query_hash"`
	GenerationID    string       `json:"generation_id,omitempty"`
	GeneratedAt     time.Time    `json:"generated_at"`
	DataAsOf        time.Time    `json:"data_as_of"`
	Status          string       `json:"status"`
	Results         []Result     `json:"results"`
	Capabilities    []Capability `json:"capabilities"`
	Insights        []Insight    `json:"insights"`
}

type SyncRun struct {
	ID        string    `json:"id"`
	QueryHash string    `json:"query_hash"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Reason    string    `json:"reason,omitempty"`
	Query     Query     `json:"query"`
	Version   uint64    `json:"version"`
}

var ErrInvalidQuery = errors.New("invalid analytics query")

var metricNames = map[string]bool{
	"active_1d": true, "active_7d": true, "active_30d": true,
	"useful_actions": true, "sessions": true, "service_operations": true,
	"service_failed": true, "funnel_entered": true, "funnel_activated": true,
	"calendar_active": true,
	"funnel_repeated": true, "funnel_immature": true,
}

func (q Query) Validate() error {
	if q.ContractVersion != Version || len(q.Products) == 0 || len(q.Products) > len(Products()) || len(q.Metrics) == 0 || len(q.Metrics) > len(metricNames) || len(q.Dimensions) > 5 || len(q.Filters) > 5 {
		return ErrInvalidQuery
	}
	products := make(map[string]bool)
	for _, p := range Products() {
		products[p.ID] = true
	}
	if !uniqueAllowed(q.Products, products) || !uniqueAllowed(q.Metrics, metricNames) {
		return ErrInvalidQuery
	}
	allowed := map[string]bool{"platform": true, "app_version": true, "country": true, "region": true, "locale": true}
	if !uniqueAllowed(q.Dimensions, allowed) {
		return ErrInvalidQuery
	}
	for dimension, values := range q.Filters {
		if !allowed[dimension] || len(values) == 0 || len(values) > 8 {
			return ErrInvalidQuery
		}
		seen := make(map[string]bool)
		for _, value := range values {
			if value == "" || len(value) > 100 || seen[value] || strings.TrimSpace(value) != value || strings.Contains(value, "@") || strings.ContainsFunc(value, unicode.IsControl) {
				return ErrInvalidQuery
			}
			seen[value] = true
		}
	}
	cutoff, e1 := time.Parse(time.DateOnly, q.CutoffDate)
	from, e2 := time.Parse(time.DateOnly, q.SeriesFrom)
	to, e3 := time.Parse(time.DateOnly, q.SeriesTo)
	if e1 != nil || e2 != nil || e3 != nil || from.Year() < 2000 || cutoff.Year() > 2100 || to.Before(from) || to.After(cutoff) || to.After(from.AddDate(0, 0, 365)) {
		return ErrInvalidQuery
	}
	if q.Methodology != "" && q.Methodology != "account-v3" && q.Methodology != "installation-v2" && q.Methodology != "ga4" && q.Methodology != "tts" {
		return ErrInvalidQuery
	}
	return nil
}

func uniqueAllowed(values []string, allowed map[string]bool) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !allowed[value] || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func (q Query) Hash() (string, error) {
	if err := q.Validate(); err != nil {
		return "", err
	}
	q.Products = sorted(q.Products)
	q.Metrics = sorted(q.Metrics)
	q.Dimensions = sorted(q.Dimensions)
	filters := make(map[string][]string, len(q.Filters))
	for key, values := range q.Filters {
		filters[key] = sorted(values)
	}
	q.Filters = filters
	body, err := json.Marshal(q)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func sorted(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
