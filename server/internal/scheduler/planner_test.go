package scheduler

import (
	"slices"
	"testing"
	"time"
)

func TestPlannerFires(t *testing.T) {
	p := NewPlanner()
	utc := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	tests := []struct {
		name, expr, tz string
		from, to       string
		limit          int
		want           []string
	}{
		{
			name: "weekday mornings in a week, from is inclusive", expr: "0 8 * * 1-5", tz: "Asia/Shanghai",
			from: "2026-11-02T00:00:00Z", to: "2026-11-09T00:00:00Z", limit: 100,
			want: []string{"2026-11-02T00:00:00Z", "2026-11-03T00:00:00Z", "2026-11-04T00:00:00Z", "2026-11-05T00:00:00Z", "2026-11-06T00:00:00Z"},
		},
		{
			name: "to is exclusive", expr: "0 * * * *", tz: "UTC",
			from: "2026-11-02T10:00:00Z", to: "2026-11-02T12:00:00Z", limit: 100,
			want: []string{"2026-11-02T10:00:00Z", "2026-11-02T11:00:00Z"},
		},
		{
			name: "limit", expr: "* * * * *", tz: "UTC",
			from: "2026-11-02T10:00:30Z", to: "2026-11-03T10:00:00Z", limit: 3,
			want: []string{"2026-11-02T10:01:00Z", "2026-11-02T10:02:00Z", "2026-11-02T10:03:00Z"},
		},
		{
			// 2026-03-08 02:30 does not exist in New York; it fires at
			// 03:30 EDT (07:30Z), once.
			name: "spring gap is shifted", expr: "30 2 * * *", tz: "America/New_York",
			from: "2026-03-07T00:00:00Z", to: "2026-03-10T00:00:00Z", limit: 100,
			want: []string{"2026-03-07T07:30:00Z", "2026-03-08T07:30:00Z", "2026-03-09T06:30:00Z"},
		},
		{
			// 2026-11-01 01:30 occurs twice in New York; it fires once.
			name: "autumn repeat fires once", expr: "30 1 * * *", tz: "America/New_York",
			from: "2026-11-01T00:00:00Z", to: "2026-11-02T00:00:00Z", limit: 100,
			want: []string{"2026-11-01T05:30:00Z"},
		},
		{name: "empty window", expr: "* * * * *", tz: "UTC", from: "2026-11-02T10:00:00Z", to: "2026-11-02T10:00:00Z", limit: 10},
		{name: "zero limit", expr: "* * * * *", tz: "UTC", from: "2026-11-02T10:00:00Z", to: "2026-11-02T11:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.Fires(tt.expr, tt.tz, utc(tt.from), utc(tt.to), tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			var s []string
			for _, v := range got {
				s = append(s, v.UTC().Format(time.RFC3339))
			}
			if !slices.Equal(s, tt.want) {
				t.Fatalf("Fires = %v, want %v", s, tt.want)
			}
		})
	}
}

func TestPlannerBoundsAndErrors(t *testing.T) {
	p := NewPlanner()
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	got, err := p.Fires("* * * * *", "UTC", from, from.Add(31*24*time.Hour), 1<<30)
	if err != nil || len(got) != MaxPlannedFires {
		t.Fatalf("every minute for 31 days = %d fires, %v; want the cap %d", len(got), err, MaxPlannedFires)
	}
	if _, err := p.Fires("not cron", "UTC", from, from.Add(time.Hour), 10); err == nil {
		t.Error("invalid schedule accepted")
	}
	if _, err := p.Fires("* * * * *", "Mars/Olympus", from, from.Add(time.Hour), 10); err == nil {
		t.Error("invalid time zone accepted")
	}
	if got := p.Describe("0 8 * * 1-5", "Asia/Shanghai"); got != "每个工作日 08:00（Asia/Shanghai）" {
		t.Errorf("Describe = %q", got)
	}
	if got := p.Describe("@every 1h", "UTC"); got != "" {
		t.Errorf("Describe of an invalid schedule = %q", got)
	}
}
