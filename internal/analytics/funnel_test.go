package analytics

import (
	"reflect"
	"testing"
	"time"
)

func TestFunnel(t *testing.T) {
	entry := time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)
	end := entry.Add(7 * 24 * time.Hour)
	observation := func(subject string, at time.Time, useful bool) Observation {
		return Observation{Product: "looks", Subject: subject, At: at, Activity: !useful, Useful: useful}
	}
	for _, tt := range []struct {
		name   string
		events []Observation
		asOf   time.Time
		want   FunnelResult
	}{
		{"out of order and duplicates", []Observation{
			observation("a", entry.Add(2*time.Hour), true),
			observation("a", entry.Add(time.Hour), true),
			observation("a", entry, false),
			observation("a", entry.Add(time.Hour), true),
		}, end, FunnelResult{Entered: 1, Activated: 1}},
		{"repeat on another UTC day", []Observation{
			observation("a", entry, true),
			observation("a", entry.Add(time.Hour).In(time.FixedZone("west", -5*3600)), true),
		}, end, FunnelResult{Entered: 1, Activated: 1, Repeated: 1}},
		{"same UTC day despite local date", []Observation{
			observation("a", entry, true),
			observation("a", entry.Add(30*time.Minute).In(time.FixedZone("east", 2*3600)), true),
		}, end, FunnelResult{Entered: 1, Activated: 1}},
		{"exact activation cutoff", []Observation{observation("a", entry, false), observation("a", end, true)}, end, FunnelResult{Entered: 1, Activated: 1}},
		{"exact repeat cutoff", []Observation{observation("a", entry, true), observation("a", end, true)}, end, FunnelResult{Entered: 1, Activated: 1, Repeated: 1}},
		{"past activation cutoff", []Observation{observation("a", entry, false), observation("a", end.Add(time.Nanosecond), true)}, end, FunnelResult{Entered: 1}},
		{"past repeat cutoff", []Observation{observation("a", entry, true), observation("a", end.Add(time.Nanosecond), true)}, end, FunnelResult{Entered: 1, Activated: 1}},
		{"immature is not denominator", []Observation{observation("a", entry, true), observation("a", entry.Add(time.Hour), true)}, end.Add(-time.Nanosecond), FunnelResult{Immature: 1}},
		{"future-only subject in cohort crossing asOf", []Observation{observation("a", entry.Add(time.Nanosecond), true)}, entry, FunnelResult{}},
		{"entry exactly asOf is observed", []Observation{observation("a", entry, true)}, entry, FunnelResult{Immature: 1}},
		{"first observed outside cohort", []Observation{observation("a", entry, true), observation("a", entry.Add(-time.Hour), false)}, end, FunnelResult{}},
		{"cohort end excluded", []Observation{observation("a", entry.Add(24*time.Hour), true)}, end.Add(24 * time.Hour), FunnelResult{}},
		{"technical does not establish entry", []Observation{
			{Product: "looks", Subject: "a", At: entry.Add(-time.Hour)}, observation("a", entry, true),
		}, end, FunnelResult{Entered: 1, Activated: 1}},
		{"empty subject and other product excluded", []Observation{
			observation("", entry, true), {Product: "other", Subject: "a", At: entry, Useful: true},
		}, end, FunnelResult{}},
		{"two installations one account", []Observation{
			{Product: "looks", Subject: "a", Installation: "one", At: entry, Useful: true},
			{Product: "looks", Subject: "a", Installation: "two", At: entry.Add(time.Hour), Useful: true},
		}, end, FunnelResult{Entered: 1, Activated: 1, Repeated: 1}},
		{"duplicate timestamps are not repeat", []Observation{observation("a", entry, true), observation("a", entry, true)}, end, FunnelResult{Entered: 1, Activated: 1}},
		{"distinct subjects", []Observation{observation("a", entry, true), observation("b", entry, false)}, end, FunnelResult{Entered: 2, Activated: 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := append([]Observation(nil), tt.events...)
			if got := Funnel(tt.events, "looks", entry, entry.Add(24*time.Hour), tt.asOf); got != tt.want {
				t.Fatalf("Funnel = %+v, want %+v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.events, before) {
				t.Fatal("Funnel mutated observations")
			}
		})
	}
}
