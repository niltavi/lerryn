package scheduler

import (
	"sync"
	"time"
)

// maxPlannerCache bounds how many parsed schedules a Planner keeps.
const maxPlannerCache = 1024

// MaxPlannedFires caps the fires one Fires call returns, whatever limit
// the caller asks for: a schedule firing every minute has 44,640 fires in
// a 31-day window.
const MaxPlannedFires = 10_000

// Planner expands cron schedules over a time window and describes them,
// for the run calendar and the overview (service.FirePlanner). It applies
// the same daylight saving rules as the scheduling loop (tech design 8.1),
// so the calendar shows exactly the times the triggers will fire. It is
// safe for concurrent use.
type Planner struct {
	mu    sync.Mutex
	cache map[plannerKey]*Cron
}

type plannerKey struct{ schedule, timezone string }

// NewPlanner returns a planner.
func NewPlanner() *Planner {
	return &Planner{cache: map[plannerKey]*Cron{}}
}

func (p *Planner) parse(schedule, timezone string) (*Cron, error) {
	k := plannerKey{schedule, timezone}
	p.mu.Lock()
	c, ok := p.cache[k]
	p.mu.Unlock()
	if ok {
		return c, nil
	}
	c, err := ParseCron(schedule, timezone)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if len(p.cache) >= maxPlannerCache {
		clear(p.cache)
	}
	p.cache[k] = c
	p.mu.Unlock()
	return c, nil
}

// Fires returns the fire times of the schedule in [from, to), in UTC and
// in order, at most limit of them (and never more than MaxPlannedFires).
func (p *Planner) Fires(schedule, timezone string, from, to time.Time, limit int) ([]time.Time, error) {
	c, err := p.parse(schedule, timezone)
	if err != nil {
		return nil, err
	}
	limit = min(limit, MaxPlannedFires)
	if limit <= 0 || !to.After(from) {
		return nil, nil
	}
	var out []time.Time
	// Next is strictly after its argument; fire times are whole minutes,
	// so starting just before from includes a fire at from itself.
	after := from.Add(-time.Nanosecond)
	for len(out) < limit {
		t := c.Next(after)
		if t.IsZero() || !t.Before(to) {
			break
		}
		out = append(out, t)
		after = t
	}
	return out, nil
}

// Describe returns the schedule in words, such as
// 每个工作日 08:00（Asia/Shanghai）, or "" when it does not parse.
func (p *Planner) Describe(schedule, timezone string) string {
	c, err := p.parse(schedule, timezone)
	if err != nil {
		return ""
	}
	return c.Describe()
}
