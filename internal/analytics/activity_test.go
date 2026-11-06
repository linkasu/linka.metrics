package analytics

import (
	"testing"
	"time"
)

func TestActiveUnits(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	events := []Observation{
		{Product: "looks", Subject: "a", Installation: "one", At: from, Activity: true},
		{Product: "looks", Subject: "a", Installation: "two", At: from.Add(time.Hour), Useful: true},
		{Product: "looks", Subject: "a", Installation: "one", At: from, Activity: true},
		{Product: "looks", Subject: "b", At: from, Useful: true},
		{Product: "looks", Installation: "three", At: from, Activity: true},
		{Product: "looks", Subject: "technical", Installation: "technical", At: from},
		{Product: "other", Subject: "other", Installation: "other", At: from, Activity: true},
		{Product: "looks", Subject: "old", Installation: "old", At: from.Add(-time.Nanosecond), Activity: true},
		{Product: "looks", Subject: "next", Installation: "next", At: to.In(time.FixedZone("east", 3*3600)), Activity: true},
	}
	for _, tt := range []struct {
		name          string
		installations bool
		want          int
	}{{"accounts", false, 2}, {"installations", true, 3}} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ActiveUnits(events, "looks", from, to, tt.installations); got != tt.want {
				t.Fatalf("ActiveUnits = %d, want %d", got, tt.want)
			}
		})
	}
	if got := ActiveUnits(events, "looks", to, to.Add(24*time.Hour), false); got != 1 {
		t.Fatalf("adjacent window = %d, want 1", got)
	}
	if got := ActiveUnits(events, "looks", to, from, false); got != 0 {
		t.Fatalf("reversed window = %d, want 0", got)
	}
}

func TestActiveUnitsUnionIsNotDailySum(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []Observation{
		{Product: "looks", Subject: "a", At: from, Activity: true},
		{Product: "looks", Subject: "a", At: from.Add(24 * time.Hour), Activity: true},
	}
	if got := ActiveUnits(events, "looks", from, from.Add(48*time.Hour), false); got != 1 {
		t.Fatalf("union distinct = %d, want 1", got)
	}
}
