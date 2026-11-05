//go:build integration

package acceptance

import (
	"net/http"
	"testing"
	"time"
)

// sleeper waits for params.sleep seconds, then prints and exits 0.
const sleeper = `import time
import sbx

seconds = int(sbx.params().get("sleep", 0))
time.sleep(seconds)
print(f"slept {seconds}s", flush=True)
`

type triggerSet struct {
	Revision   int  `json:"revision"`
	DryRun     bool `json:"dry_run"`
	TaskActive bool `json:"task_active"`
	Triggers   []struct {
		ID         string     `json:"id"`
		Name       string     `json:"name"`
		NextFireAt *time.Time `json:"next_fire_at"`
		Cron       *struct {
			ScheduleText string      `json:"schedule_text"`
			NextFires    []time.Time `json:"next_fires"`
		} `json:"cron"`
		Webhook *struct {
			URL   string  `json:"url"`
			Token *string `json:"token"`
		} `json:"webhook"`
	} `json:"triggers"`
}

// weekdayMornings returns the next n weekdays at 08:00 in loc after now.
func weekdayMornings(now time.Time, loc *time.Location, n int) []time.Time {
	var out []time.Time
	local := now.In(loc)
	for d := 0; len(out) < n; d++ {
		day := time.Date(local.Year(), local.Month(), local.Day()+d, 8, 0, 0, 0, loc)
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday || !day.After(now) {
			continue
		}
		out = append(out, day.UTC())
	}
	return out
}

// waitFor polls cond every second until it holds or within passes.
func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not reached within %s", what, within)
		}
		time.Sleep(time.Second)
	}
}

// A02: a weekday schedule with a time zone reports the right next fire
// times; once active, the schedule produces runs on its own, with no
// agent connected and across a server restart, each scheduled time once.
func TestA02ScheduledRuns(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	project := s.signUp("a02")
	taskID := s.applyTask(project, "minutely", sleeper, nil)
	path := "/api/v1/tasks/" + taskID + "/triggers"

	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	var preview triggerSet
	before := time.Now()
	s.call(http.MethodPut, path+"?dry_run=true", map[string]any{"triggers": []any{
		map[string]any{"name": "weekday-morning", "type": "cron", "schedule": "0 8 * * 1-5", "timezone": "Asia/Shanghai"},
	}}, http.StatusOK, &preview)
	cron := preview.Triggers[0].Cron
	if !preview.DryRun || cron == nil || cron.ScheduleText != "每个工作日 08:00（Asia/Shanghai）" {
		t.Fatalf("preview = %+v", preview)
	}
	want := weekdayMornings(before, shanghai, 5)
	for i, at := range cron.NextFires {
		if !at.Equal(want[i]) {
			t.Fatalf("next fires %v, want %v", cron.NextFires, want)
		}
	}

	var set triggerSet
	s.call(http.MethodPut, path, map[string]any{"triggers": []any{
		map[string]any{"name": "every-minute", "type": "cron", "schedule": "* * * * *", "timezone": "UTC"},
	}}, http.StatusOK, &set)
	triggerID := set.Triggers[0].ID
	if set.TaskActive || set.Triggers[0].NextFireAt != nil {
		t.Fatalf("an inactive task must show no next fire: %+v", set)
	}
	s.activate(taskID)
	s.call(http.MethodGet, path, nil, http.StatusOK, &set)
	if !set.TaskActive || set.Triggers[0].NextFireAt == nil {
		t.Fatalf("active task = %+v", set)
	}

	// The agent makes no further calls; the checks below only read. The
	// schedule runs on its own.
	// Wait for it to finish: a run still in flight at the restart is
	// interrupted, which is A10's case, not this one.
	waitFor(t, 120*time.Second, "first scheduled run completed", func() bool {
		runs := s.triggerRuns(triggerID)
		return len(runs) >= 1 && runs[0].Status == "completed"
	})

	// A restart between fires neither repeats nor loses a scheduled time.
	s.stop()
	s.start()
	waitFor(t, 150*time.Second, "two completed scheduled runs", func() bool {
		done := 0
		for _, r := range s.triggerRuns(triggerID) {
			if r.Status == "completed" {
				done++
			}
		}
		return done >= 2
	})
	seen := map[time.Time]bool{}
	for _, r := range s.triggerRuns(triggerID) {
		at := r.Trigger.ScheduledAt
		if r.Trigger.Kind != "cron" || at == nil || at.Second() != 0 || seen[*at] {
			t.Fatalf("run %+v: want one cron run per scheduled minute", r)
		}
		if r.Status != "completed" && r.Status != "queued" && r.Status != "preparing" && r.Status != "running" {
			t.Fatalf("run %s ended %s", r.ID, r.Status)
		}
		seen[*at] = true
	}
}

// A08: a run that lasts longer than the trigger interval makes the next
// fire a skipped record with reason overlap; repeating a webhook call with
// the same Idempotency-Key produces no new run.
func TestA08OverlapAndIdempotency(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	project := s.signUp("a08")

	slow := s.applyTask(project, "slow", sleeper, map[string]any{"concurrency": "forbid"})
	var set triggerSet
	s.call(http.MethodPut, "/api/v1/tasks/"+slow+"/triggers", map[string]any{"triggers": []any{
		map[string]any{"name": "every-minute", "type": "cron", "schedule": "* * * * *", "timezone": "UTC", "params": map[string]any{"sleep": 100}},
	}}, http.StatusOK, &set)
	cronID := set.Triggers[0].ID
	s.activate(slow)

	hookTask := s.applyTask(project, "hook", sleeper, map[string]any{"concurrency": "allow"})
	s.call(http.MethodPut, "/api/v1/tasks/"+hookTask+"/triggers", map[string]any{"triggers": []any{
		map[string]any{"name": "push", "type": "webhook"},
	}}, http.StatusOK, &set)
	hookID, hookToken := set.Triggers[0].ID, *set.Triggers[0].Webhook.Token
	s.activate(hookTask)

	// Idempotent webhook delivery.
	agent := s.token
	s.token = ""
	type accepted struct {
		RunID     string `json:"run_id"`
		Status    string `json:"status"`
		Duplicate bool   `json:"duplicate"`
	}
	var first, again accepted
	auth := []string{"Authorization", "Bearer " + hookToken, "Idempotency-Key", "delivery-7"}
	s.call(http.MethodPost, "/hooks/"+hookID, []byte(`{"lead":7}`), http.StatusAccepted, &first, auth...)
	s.call(http.MethodPost, "/hooks/"+hookID, []byte(`{"lead":7}`), http.StatusAccepted, &again, auth...)
	if first.Duplicate || !again.Duplicate || again.RunID != first.RunID {
		t.Fatalf("first %+v, again %+v", first, again)
	}
	s.call(http.MethodPost, "/hooks/"+hookID, []byte(`{"lead":8}`), http.StatusConflict, nil, auth...)
	s.token = agent
	if runs := s.triggerRuns(hookID); len(runs) != 1 {
		t.Fatalf("webhook runs = %+v, want one", runs)
	}

	// Overlap: the first scheduled run sleeps 100s, so the next minute's
	// fire finds it running.
	var running apiRun
	waitFor(t, 200*time.Second, "an overlap record", func() bool {
		for _, r := range s.triggerRuns(cronID) {
			if r.Status == "running" || r.Status == "preparing" {
				running = r
			}
			if r.Status == "skipped" && r.SkipReason == "overlap" {
				return true
			}
		}
		return false
	})
	runs := s.triggerRuns(cronID)
	if runs[0].Status == "skipped" {
		t.Fatalf("the first scheduled run was skipped: %+v", runs)
	}
	if running.ID != "" {
		s.call(http.MethodPost, "/api/v1/runs/"+running.ID+"/cancel", nil, http.StatusAccepted, nil)
	}
}
