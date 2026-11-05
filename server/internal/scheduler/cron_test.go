package scheduler

import (
	"errors"
	"testing"
	"time"
)

func mustParse(t *testing.T, expr, tz string) *Cron {
	t.Helper()
	c, err := ParseCron(expr, tz)
	if err != nil {
		t.Fatalf("ParseCron(%q, %q): %v", expr, tz, err)
	}
	return c
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestCronNext(t *testing.T) {
	tests := []struct {
		name, expr, tz, after string
		want                  []string
	}{
		{
			// Friday 2026-11-06 09:00 in Shanghai, after that day's fire.
			name: "weekdays in Shanghai skip the weekend", expr: "0 8 * * 1-5", tz: "Asia/Shanghai",
			after: "2026-11-06T01:00:00Z",
			want:  []string{"2026-11-09T00:00:00Z", "2026-11-10T00:00:00Z", "2026-11-11T00:00:00Z", "2026-11-12T00:00:00Z", "2026-11-13T00:00:00Z"},
		},
		{
			name: "strictly after", expr: "0 8 * * *", tz: "UTC",
			after: "2026-11-06T08:00:00Z",
			want:  []string{"2026-11-07T08:00:00Z"},
		},
		{
			// New York springs forward at 02:00 on 2026-03-08: 02:30 does
			// not exist and fires at 03:30 EDT instead.
			name: "skipped local time fires after the change", expr: "30 2 * * *", tz: "America/New_York",
			after: "2026-03-07T12:00:00Z",
			want:  []string{"2026-03-08T07:30:00Z", "2026-03-09T06:30:00Z"},
		},
		{
			// 02:00 and 02:30 do not exist; their shifted times coincide
			// with 03:00 and 03:30, which fire once each.
			name: "half-hourly across spring forward", expr: "*/30 * * * *", tz: "America/New_York",
			after: "2026-03-08T06:00:00Z",
			want:  []string{"2026-03-08T06:30:00Z", "2026-03-08T07:00:00Z", "2026-03-08T07:30:00Z", "2026-03-08T08:00:00Z"},
		},
		{
			name: "day of month or day of week when both are set", expr: "0 0 13 * 5", tz: "UTC",
			after: "2026-11-01T00:00:00Z",
			// Fridays 6 and 13, and the 13th.
			want: []string{"2026-11-06T00:00:00Z", "2026-11-13T00:00:00Z", "2026-11-20T00:00:00Z"},
		},
		{
			name: "leap day", expr: "0 0 29 2 *", tz: "UTC",
			after: "2026-11-01T00:00:00Z",
			want:  []string{"2028-02-29T00:00:00Z"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := mustParse(t, tt.expr, tt.tz)
			got := c.NextN(utc(tt.after), len(tt.want))
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if !got[i].Equal(utc(tt.want[i])) || got[i].Location() != time.UTC {
					t.Fatalf("fire %d = %v, want %s", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// New York falls back at 02:00 on 2026-11-01: 01:30 happens twice and
// fires once.
func TestCronRepeatedLocalTimeFiresOnce(t *testing.T) {
	c := mustParse(t, "30 1 * * *", "America/New_York")
	got := c.NextN(utc("2026-10-31T12:00:00Z"), 2)
	first, second := got[0], got[1]
	if !first.Equal(utc("2026-11-01T05:30:00Z")) && !first.Equal(utc("2026-11-01T06:30:00Z")) {
		t.Fatalf("first fire = %v, want one of the two 01:30 instants", first)
	}
	if !second.Equal(utc("2026-11-02T06:30:00Z")) {
		t.Fatalf("second fire = %v, want 2026-11-02 01:30 EST", second)
	}
}

func TestCronDescribe(t *testing.T) {
	tests := []struct{ expr, tz, want string }{
		{"0 8 * * 1-5", "Asia/Shanghai", "每个工作日 08:00（Asia/Shanghai）"},
		{"*/15 * * * *", "UTC", "每 15 分钟（UTC）"},
		{"* * * * *", "UTC", "每分钟（UTC）"},
		{"@daily", "UTC", "每天 00:00（UTC）"},
		{"@hourly", "UTC", "每小时的第 0 分钟（UTC）"},
		{"30 9 1 * *", "UTC", "每月 1 日 09:30（UTC）"},
		{"0 9 * * 1,3", "UTC", "每周一、三 09:00（UTC）"},
		{"0 10 * * 0,6", "UTC", "每个周末 10:00（UTC）"},
		{"0 0 1 1 *", "UTC", "每年 1 月 1 日 00:00（UTC）"},
		{"5 * * * *", "UTC", "每小时的第 5 分钟（UTC）"},
		{"0 */2 * * *", "UTC", "每 2 小时（UTC）"},
		{"*/10 * * * 1-5", "UTC", "每个工作日每 10 分钟（UTC）"},
		{"0 9-17 * * 1-5", "UTC", "每个工作日 9 点至 17 点每小时的第 0 分钟（UTC）"},
		{"0 8,20 * * *", "UTC", "每天 08:00、20:00（UTC）"},
		{"0 0 1,15 * 1", "UTC", "每月 1 日、15 日或每周一 00:00（UTC）"},
	}
	for _, tt := range tests {
		if got := mustParse(t, tt.expr, tt.tz).Describe(); got != tt.want {
			t.Errorf("Describe(%q) = %q, want %q", tt.expr, got, tt.want)
		}
	}
}

func TestParseCronRejects(t *testing.T) {
	tests := []struct {
		name, expr, tz string
		want           error
	}{
		{"empty", "", "UTC", ErrInvalidSchedule},
		{"four fields", "0 8 * *", "UTC", ErrInvalidSchedule},
		{"seconds field", "0 0 8 * * *", "UTC", ErrInvalidSchedule},
		{"out of range", "61 * * * *", "UTC", ErrInvalidSchedule},
		{"every", "@every 5m", "UTC", ErrInvalidSchedule},
		{"zone prefix", "CRON_TZ=UTC 0 8 * * *", "UTC", ErrInvalidSchedule},
		{"never fires", "0 0 30 2 *", "UTC", ErrInvalidSchedule},
		{"no zone", "0 8 * * *", "", ErrInvalidTimezone},
		{"server zone", "0 8 * * *", "Local", ErrInvalidTimezone},
		{"unknown zone", "0 8 * * *", "Mars/Olympus", ErrInvalidTimezone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCron(tt.expr, tt.tz); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
