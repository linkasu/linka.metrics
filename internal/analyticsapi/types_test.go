package analyticsapi

import (
	"reflect"
	"testing"
)

func validQuery() Query {
	return Query{ContractVersion: 1, Products: []string{"linka-type"}, Metrics: []string{"active_30d"}, CutoffDate: "2026-11-05", SeriesFrom: "2026-10-01", SeriesTo: "2026-11-05"}
}

func TestQueryValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Query)
	}{
		{"unknown product", func(q *Query) { q.Products = []string{"another-product"} }},
		{"duplicate product", func(q *Query) { q.Products = []string{"linka-type", "linka-type"} }},
		{"unknown metric", func(q *Query) { q.Metrics = []string{"arbitrary_sql"} }},
		{"unknown dimension", func(q *Query) { q.Dimensions = []string{"email"} }},
		{"PII filter", func(q *Query) { q.Filters = map[string][]string{"email": {"x@example.test"}} }},
		{"control character", func(q *Query) { q.Filters = map[string][]string{"platform": {"android\n"}} }},
		{"oversized range", func(q *Query) { q.SeriesFrom = "2020-01-01" }},
		{"reversed range", func(q *Query) { q.SeriesFrom = "2026-11-06" }},
		{"after cutoff", func(q *Query) { q.SeriesTo = "2026-11-06" }},
		{"invalid date", func(q *Query) { q.CutoffDate = "2026-02-30" }},
		{"unknown version", func(q *Query) { q.ContractVersion = 2 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			q := validQuery()
			test.edit(&q)
			if q.Validate() == nil {
				t.Fatal("invalid query accepted")
			}
		})
	}
	if err := validQuery().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryHashCanonicalAndDoesNotMutateInput(t *testing.T) {
	q := validQuery()
	q.Products = []string{"linka-type", "linka-looks"}
	q.Filters = map[string][]string{"platform": {"windows", "android"}}
	a, err := q.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(q.Filters["platform"], []string{"windows", "android"}) {
		t.Fatal("hash mutated caller filter")
	}
	q.Products = []string{"linka-looks", "linka-type"}
	q.Filters["platform"] = []string{"android", "windows"}
	b, _ := q.Hash()
	if a != b || len(a) != 64 {
		t.Fatal("set-equivalent queries have different hashes")
	}
	q.CutoffDate = "2026-11-06"
	c, _ := q.Hash()
	if a == c {
		t.Fatal("different cutoff reused snapshot key")
	}
}
