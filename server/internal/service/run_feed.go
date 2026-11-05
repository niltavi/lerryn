package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/events"
	"github.com/niltavi/lerryn/server/internal/store"
)

// recordRunEvent appends an event written by the server, such as a
// status change, to a run. It takes the next number in the run's event
// sequence, which runner events share, so call it in the transaction that
// makes the change.
func recordRunEvent(ctx context.Context, tx bun.IDB, runID uuid.UUID, kind, message string, data any, ts time.Time) error {
	var payload string
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("encode %s event: %w", kind, err)
		}
		payload = string(b)
	}
	var seq int64
	err := tx.NewUpdate().Model((*store.Run)(nil)).
		Set("event_seq = event_seq + 1").
		Where("id = ?", runID).
		Returning("event_seq").
		Scan(ctx, &seq)
	if err != nil {
		return fmt.Errorf("number %s event of run %s: %w", kind, runID, err)
	}
	_, err = tx.NewInsert().Model(&store.RunEvent{
		ID:      store.NewID(),
		RunID:   runID,
		Seq:     seq,
		TS:      ts,
		Kind:    kind,
		Message: message,
		Payload: payload,
	}).Exec(ctx)
	if err != nil {
		return fmt.Errorf("record %s event of run %s: %w", kind, runID, err)
	}
	return nil
}

// statusChange is the data of a status event. From is empty when the
// previous status is not known, as for runs the reconciler interrupts.
type statusChange struct {
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

// recordStatus records a status change of a run.
func recordStatus(ctx context.Context, tx bun.IDB, runID uuid.UUID, from, to string, ts time.Time) error {
	return recordRunEvent(ctx, tx, runID, store.EventStatus, "", statusChange{From: from, To: to}, ts)
}

// RunUpdate is a change of a run's status or current stage, pushed to
// streamEvents subscribers.
type RunUpdate struct {
	RunID     uuid.UUID
	TaskID    uuid.UUID
	ProjectID uuid.UUID
	Status    string
	// CurrentStage is the ID of the stage in progress, empty if none.
	CurrentStage string
	// ResultStatus is the reported result status, empty if none.
	ResultStatus string
	TS           time.Time
}

// feedBuffer is how many updates a slow subscriber may fall behind before
// it is told to resynchronise.
const feedBuffer = 256

// runFeed fans run updates out to the subscribers in this process. Changes
// made on other servers arrive through the event bus (Runs.Listen).
type runFeed struct {
	mu   sync.Mutex
	subs map[*Subscription]struct{}
}

// Subscription receives run updates. When it falls more than feedBuffer
// updates behind, updates are dropped and Overflowed reports true once.
type Subscription struct {
	C    <-chan RunUpdate
	c    chan RunUpdate
	feed *runFeed
	mu   sync.Mutex
	lost bool
}

// Overflowed reports, and clears, whether updates were dropped since the
// last call.
func (s *Subscription) Overflowed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	lost := s.lost
	s.lost = false
	return lost
}

// Close ends the subscription.
func (s *Subscription) Close() {
	s.feed.mu.Lock()
	defer s.feed.mu.Unlock()
	delete(s.feed.subs, s)
}

func newRunFeed() *runFeed {
	return &runFeed{subs: map[*Subscription]struct{}{}}
}

func (f *runFeed) subscribe() *Subscription {
	c := make(chan RunUpdate, feedBuffer)
	s := &Subscription{C: c, c: c, feed: f}
	f.mu.Lock()
	f.subs[s] = struct{}{}
	f.mu.Unlock()
	return s
}

func (f *runFeed) active() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs) > 0
}

func (f *runFeed) publish(u RunUpdate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for s := range f.subs {
		select {
		case s.c <- u:
		default:
			s.mu.Lock()
			s.lost = true
			s.mu.Unlock()
		}
	}
}

// Subscribe returns a subscription to run updates. Updates are not
// filtered: the caller checks what each subscriber may see (Visible).
func (r *Runs) Subscribe() *Subscription {
	return r.feed.subscribe()
}

// changed tells subscribers that runs changed: their status, stage or
// result. Call it after the change commits. Subscribers here get the
// update at once; with a bus that spans servers, the change is also
// published so that the event streams of other servers report it.
func (r *Runs) changed(ctx context.Context, ids ...uuid.UUID) {
	if r == nil {
		return
	}
	for _, id := range ids {
		r.publishUpdate(ctx, id)
		if r.crossNode {
			r.publish(ctx, events.Event{Kind: events.RunChanged, RunID: id})
		}
	}
}

// publishUpdate reads the run's state and hands it to the local
// subscribers, if there are any.
func (r *Runs) publishUpdate(ctx context.Context, id uuid.UUID) {
	if !r.feed.active() {
		return
	}
	u, err := r.update(ctx, id)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			r.log.WarnContext(ctx, "read run update", "run_id", id, "error", err)
		}
		return
	}
	r.feed.publish(u)
}

// update reads what a RunUpdate reports about a run.
func (r *Runs) update(ctx context.Context, id uuid.UUID) (RunUpdate, error) {
	var run store.Run
	if err := r.db.NewSelect().Model(&run).Column("id", "task_id", "project_id", "status", "result").
		Where("id = ?", id).Scan(ctx); err != nil {
		return RunUpdate{}, err
	}
	u := RunUpdate{RunID: run.ID, TaskID: run.TaskID, ProjectID: run.ProjectID, Status: run.Status, TS: store.Now()}
	if run.Result != "" {
		var res Result
		if err := json.Unmarshal([]byte(run.Result), &res); err == nil {
			u.ResultStatus = res.Status
		}
	}
	if store.RunActive(run.Status) {
		stage, err := currentStages(ctx, r.db, []uuid.UUID{id})
		if err != nil {
			return RunUpdate{}, err
		}
		u.CurrentStage = stage[id]
	}
	return u, nil
}

// currentStages returns, for each run, the ID of the stage it is in: the
// stage of its last stage event when that event started the stage.
func currentStages(ctx context.Context, idb bun.IDB, runIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if len(runIDs) == 0 {
		return out, nil
	}
	var events []store.RunEvent
	err := idb.NewSelect().Model(&events).Column("run_id", "seq", "kind", "stage").
		Where("run_id IN (?) AND kind IN (?)", bun.List(runIDs),
			bun.List([]string{store.EventStageStart, store.EventStageDone, store.EventStageFail})).
		Order("run_id", "seq").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("stage events: %w", err)
	}
	// Several stages can be open at once; the most recently started one
	// that is still open is the current stage.
	open := map[uuid.UUID][]string{}
	for _, e := range events {
		stack := open[e.RunID]
		for i, s := range stack {
			if s == e.Stage {
				stack = append(stack[:i:i], stack[i+1:]...)
				break
			}
		}
		if e.Kind == store.EventStageStart {
			stack = append(stack, e.Stage)
		}
		open[e.RunID] = stack
	}
	for id, stack := range open {
		if len(stack) > 0 {
			out[id] = stack[len(stack)-1]
		}
	}
	return out, nil
}
