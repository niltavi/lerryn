package scheduler

import (
	"testing"
	"time"
)

func TestCronPrevious(t *testing.T) {
	for _, tt := range []struct {
		name, expr, zone, before, want string
	}{
		{"strict boundary", "0 8 * * *", "UTC", "2026-11-06T08:00:00Z", "2026-11-05T08:00:00Z"},
		{"fractional boundary", "0 8 * * *", "UTC", "2026-11-06T08:00:00.000000001Z", "2026-11-06T08:00:00Z"},
		{"local day", "0 8 * * 1-5", "Asia/Shanghai", "2026-11-09T00:00:00Z", "2026-11-06T00:00:00Z"},
		{"leap day", "0 0 29 2 *", "UTC", "2026-11-01T00:00:00Z", "2024-02-29T00:00:00Z"},
		{"day of month or week", "0 0 13 * 5", "UTC", "2026-11-14T00:00:00Z", "2026-11-13T00:00:00Z"},
		{"spring gap", "30 2 * * *", "America/New_York", "2026-03-08T07:31:00Z", "2026-03-08T07:30:00Z"},
		{"spring ordering", "0,30 2,3 * * *", "America/New_York", "2026-03-08T07:30:00Z", "2026-03-08T07:00:00Z"},
		{"autumn repeat", "30 1 * * *", "America/New_York", "2026-11-01T06:15:00Z", "2026-11-01T05:30:00Z"},
		{"autumn previous day", "30 1 * * *", "America/New_York", "2026-11-01T05:30:00Z", "2026-10-31T05:30:00Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := mustParse(t, tt.expr, tt.zone)
			got := c.Previous(utc(tt.before))
			if !got.Equal(utc(tt.want)) || got.Location() != time.UTC {
				t.Fatalf("Previous(%s) = %v, want %s", tt.before, got, tt.want)
			}
			// Forward and reverse expansion must select the same instant,
			// especially where local times are shifted or repeated.
			if next := c.Next(got.Add(-time.Nanosecond)); !next.Equal(got) {
				t.Fatalf("Next disagrees with Previous: %v, want %v", next, got)
			}
		})
	}
}
