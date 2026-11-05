// Package scheduler runs the scheduling loop (tech design 8.2) and holds
// the cron rules it and the trigger service share: parsing, the next fire
// times in a time zone, and a readable description of a schedule.
//
// The loop only decides when to look; what a due trigger does (fires,
// runs, skipped records) is business logic in the service package, which
// the loop reaches through the Firer interface.
package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zone data in the binary: minimal images lack /usr/share/zoneinfo

	"github.com/robfig/cron/v3"
)

// starBit is the bit robfig/cron sets on a field written as * or ?. It is
// needed for the classic day rule: when both day of month and day of week
// are restricted, a day matching either one fires.
const starBit = 1 << 63

// searchDays bounds the search for the next fire time. Nine years covers
// the sparsest schedule that can fire at all, 29 February.
const searchDays = 366 * 9

// Errors of ParseCron. Callers report them against different fields.
var (
	ErrInvalidTimezone = errors.New("invalid time zone")
	ErrInvalidSchedule = errors.New("invalid cron schedule")
)

// shortcuts maps the accepted @ shortcuts to the five fields they stand
// for, so that every schedule is described from the same fields.
var shortcuts = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// Cron is a parsed five-field schedule bound to an IANA time zone.
type Cron struct {
	expr   string
	fields [5]string
	loc    *time.Location
	spec   *cron.SpecSchedule
}

// ParseCron parses a five-field cron expression (minute, hour, day of
// month, month, day of week) or one of the @ shortcuts, evaluated in the
// IANA time zone timezone. @every and TZ= prefixes are not accepted: the
// time zone is a separate field, and fire times must be calendar times.
func ParseCron(expr, timezone string) (*Cron, error) {
	loc, err := loadLocation(timezone)
	if err != nil {
		return nil, err
	}
	expr = strings.TrimSpace(expr)
	fields := expr
	if strings.HasPrefix(expr, "@") {
		f, ok := shortcuts[strings.ToLower(expr)]
		if !ok {
			return nil, fmt.Errorf("%w: unknown shortcut %s", ErrInvalidSchedule, expr)
		}
		fields = f
	}
	parts := strings.Fields(fields)
	if len(parts) != 5 {
		return nil, fmt.Errorf("%w: want 5 fields (minute hour day-of-month month day-of-week), got %d", ErrInvalidSchedule, len(parts))
	}
	sched, err := cron.ParseStandard(strings.Join(parts, " "))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSchedule, err)
	}
	spec, ok := sched.(*cron.SpecSchedule)
	if !ok {
		return nil, fmt.Errorf("%w: not a calendar schedule", ErrInvalidSchedule)
	}
	c := &Cron{expr: expr, loc: loc, spec: spec}
	copy(c.fields[:], parts)
	if c.Next(time.Now()).IsZero() {
		return nil, fmt.Errorf("%w: the schedule never fires", ErrInvalidSchedule)
	}
	return c, nil
}

func loadLocation(name string) (*time.Location, error) {
	// "Local" would follow the server's zone, and "" means UTC to Go;
	// neither is an explicit IANA zone.
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("%w: an IANA time zone name such as Asia/Shanghai is required", ErrInvalidTimezone)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTimezone, err)
	}
	return loc, nil
}

// Location returns the schedule's time zone.
func (c *Cron) Location() *time.Location { return c.loc }

// Next returns the first fire time strictly after after, in UTC, or the
// zero time if there is none within nine years.
//
// Fire times are local wall-clock times in the schedule's zone. Across a
// daylight saving change, a local time that does not exist (the hour
// skipped in spring) fires at the same offset after the change, so 02:30
// becomes 03:30; a local time that occurs twice (the hour repeated in
// autumn) fires once. A time can therefore never fire twice.
func (c *Cron) Next(after time.Time) time.Time {
	local := after.In(c.loc)
	y, m, d := local.Date()
	for i := range searchDays {
		// Noon is never skipped by a zone change, so it names the day
		// reliably.
		day := time.Date(y, m, d+i, 12, 0, 0, 0, c.loc)
		if !c.dayMatches(day) {
			continue
		}
		if t, ok := c.firstOnDay(day, after); ok {
			return t.UTC()
		}
	}
	return time.Time{}
}

// Previous returns the last fire time strictly before before, in UTC, or
// zero when none exists within nine years. It uses Next's daylight saving
// rules, including the same choice for repeated local times.
func (c *Cron) Previous(before time.Time) time.Time {
	local := before.In(c.loc)
	y, m, d := local.Date()
	for i := range searchDays {
		day := time.Date(y, m, d-i, 12, 0, 0, 0, c.loc)
		if !c.dayMatches(day) {
			continue
		}
		var best time.Time
		for h := range 24 {
			if c.spec.Hour&(1<<uint(h)) == 0 {
				continue
			}
			for mi := range 60 {
				if c.spec.Minute&(1<<uint(mi)) == 0 {
					continue
				}
				t := localTime(day, h, mi, c.loc)
				if t.Before(before) && (best.IsZero() || t.After(best)) {
					best = t
				}
			}
		}
		if !best.IsZero() {
			return best.UTC()
		}
	}
	return time.Time{}
}

// NextN returns up to n fire times after after, in UTC.
func (c *Cron) NextN(after time.Time, n int) []time.Time {
	out := make([]time.Time, 0, n)
	for len(out) < n {
		t := c.Next(after)
		if t.IsZero() {
			break
		}
		out = append(out, t)
		after = t
	}
	return out
}

func (c *Cron) dayMatches(day time.Time) bool {
	s := c.spec
	if s.Month&(1<<uint(day.Month())) == 0 {
		return false
	}
	domMatch := s.Dom&(1<<uint(day.Day())) != 0
	dowMatch := s.Dow&(1<<uint(day.Weekday())) != 0
	if s.Dom&starBit != 0 || s.Dow&starBit != 0 {
		return domMatch && dowMatch
	}
	return domMatch || dowMatch
}

// firstOnDay returns the earliest fire time on day after after. Wall-clock
// order is not instant order around a zone change (02:30 normalised to
// 03:30 comes after 03:00), so every candidate of the day is compared.
func (c *Cron) firstOnDay(day, after time.Time) (time.Time, bool) {
	var best time.Time
	found := false
	for h := range 24 {
		if c.spec.Hour&(1<<uint(h)) == 0 {
			continue
		}
		for mi := range 60 {
			if c.spec.Minute&(1<<uint(mi)) == 0 {
				continue
			}
			t := localTime(day, h, mi, c.loc)
			if t.After(after) && (!found || t.Before(best)) {
				best, found = t, true
			}
		}
	}
	return best, found
}

// localTime returns the instant of wall-clock time h:mi on day in loc. A
// wall time skipped by a zone change is moved forward by the length of
// the gap, so 02:30 on a spring-forward night becomes 03:30. time.Date
// leaves the direction unspecified and in practice moves it back an
// hour, which would fire before the change.
func localTime(day time.Time, h, mi int, loc *time.Location) time.Time {
	t := time.Date(day.Year(), day.Month(), day.Day(), h, mi, 0, 0, loc)
	want := time.Date(day.Year(), day.Month(), day.Day(), h, mi, 0, 0, time.UTC)
	got := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
	if gap := want.Sub(got); gap > 0 {
		t = t.Add(gap)
	}
	return t
}

// Describe returns a readable Chinese description of the schedule, such
// as 每个工作日 08:00（Asia/Shanghai）.
func (c *Cron) Describe() string {
	day := c.describeDays()
	clock := c.describeTime()
	var b strings.Builder
	switch {
	case day == "每天" && strings.HasPrefix(clock, "每"):
		// 每 15 分钟, not 每天每 15 分钟.
		b.WriteString(clock)
	case strings.HasPrefix(clock, "每"):
		b.WriteString(day + clock)
	default:
		b.WriteString(day + " " + clock)
	}
	b.WriteString("（" + c.loc.String() + "）")
	return b.String()
}

func (c *Cron) describeDays() string {
	month := c.fields[3]
	s := c.spec
	domAll, dowAll := s.Dom&starBit != 0, s.Dow&starBit != 0
	var days string
	switch {
	case domAll && dowAll:
		days = "每天"
	case domAll:
		days = describeWeekdays(bitsOf(s.Dow, 0, 6))
	case dowAll:
		days = "每月 " + joinRanges(bitsOf(s.Dom, 1, 31), "日")
	default:
		days = "每月 " + joinRanges(bitsOf(s.Dom, 1, 31), "日") + "或" + describeWeekdays(bitsOf(s.Dow, 0, 6))
	}
	if month == "*" || month == "?" {
		return days
	}
	months := "每年 " + joinRanges(bitsOf(s.Month, 1, 12), "月")
	switch {
	case days == "每天":
		return months + "的每天"
	case strings.HasPrefix(days, "每月 "):
		return months + " " + strings.TrimPrefix(days, "每月 ")
	default:
		return months + "的" + days
	}
}

func describeWeekdays(days []int) string {
	switch {
	case equalInts(days, []int{1, 2, 3, 4, 5}):
		return "每个工作日"
	case equalInts(days, []int{0, 6}):
		return "每个周末"
	}
	names := []string{"日", "一", "二", "三", "四", "五", "六"}
	parts := make([]string, len(days))
	for i, d := range days {
		parts[i] = names[d]
	}
	return "每周" + strings.Join(parts, "、")
}

func (c *Cron) describeTime() string {
	minField, hourField := c.fields[0], c.fields[1]
	minutes := bitsOf(c.spec.Minute, 0, 59)
	hours := bitsOf(c.spec.Hour, 0, 23)
	minAll := c.spec.Minute&starBit != 0
	hourAll := c.spec.Hour&starBit != 0
	if step, ok := everyStep(minField); ok && hourAll {
		return "每 " + strconv.Itoa(step) + " 分钟"
	}
	if minAll && hourAll {
		return "每分钟"
	}
	if hourAll {
		return "每小时的第 " + joinInts(minutes) + " 分钟"
	}
	if step, ok := everyStep(hourField); ok && len(minutes) == 1 && minutes[0] == 0 {
		return "每 " + strconv.Itoa(step) + " 小时"
	}
	if minAll {
		return joinRanges(hours, "点") + "每分钟"
	}
	if len(hours)*len(minutes) <= 6 {
		times := make([]string, 0, len(hours)*len(minutes))
		for _, h := range hours {
			for _, m := range minutes {
				times = append(times, fmt.Sprintf("%02d:%02d", h, m))
			}
		}
		return strings.Join(times, "、")
	}
	return joinRanges(hours, "点") + "每小时的第 " + joinInts(minutes) + " 分钟"
}

// everyStep recognises */n and 0/n.
func everyStep(field string) (int, bool) {
	base, step, ok := strings.Cut(field, "/")
	if !ok || (base != "*" && base != "0") {
		return 0, false
	}
	n, err := strconv.Atoi(step)
	if err != nil || n < 2 {
		return 0, false
	}
	return n, true
}

func bitsOf(bits uint64, lo, hi int) []int {
	var out []int
	for i := lo; i <= hi; i++ {
		if bits&(1<<uint(i)) != 0 {
			out = append(out, i)
		}
	}
	return out
}

// joinRanges writes values with runs of three or more as ranges, each
// value followed by unit: 1 日至 5 日、10 日.
func joinRanges(values []int, unit string) string {
	var parts []string
	for i := 0; i < len(values); {
		j := i
		for j+1 < len(values) && values[j+1] == values[j]+1 {
			j++
		}
		if j-i >= 2 {
			parts = append(parts, fmt.Sprintf("%d %s至 %d %s", values[i], unit, values[j], unit))
		} else {
			for k := i; k <= j; k++ {
				parts = append(parts, fmt.Sprintf("%d %s", values[k], unit))
			}
		}
		i = j + 1
	}
	return strings.Join(parts, "、")
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, "、")
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
