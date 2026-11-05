// Package events is the event bus that tells every server process about
// changes to runs (tech design 4.4): a run was queued, so long-polling
// runners can claim it; a run ended, so requests waiting for it can return.
//
// Events carry only the kind and the run ID. Receivers read whatever they
// need from the database, so a lost event costs latency, not correctness:
// every receiver also polls on a slower timer.
//
// Local delivers within one process and is all a single server on SQLite
// needs. Postgres adds LISTEN/NOTIFY so that events reach every server
// that shares the database.
package events

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// Kind is the type of an event.
type Kind string

// Event kinds.
const (
	// RunQueued: a run entered the queue.
	RunQueued Kind = "run.queued"
	// RunEnded: a run reached a terminal status.
	RunEnded Kind = "run.ended"
	// RunChanged: a run's status, stage or result changed, for the
	// event streams of other servers (streamEvents).
	RunChanged Kind = "run.changed"
)

// Event is a change to a run.
type Event struct {
	Kind  Kind      `json:"k"`
	RunID uuid.UUID `json:"r"`
	// Origin identifies the process that published the event, so that a
	// receiver can skip what it already handled locally. Empty when the
	// publisher does not say.
	Origin string `json:"o,omitempty"`
}

// Bus publishes events to every subscriber, in this process and, depending
// on the implementation, in other server processes.
type Bus interface {
	// Publish sends e. It does not wait for subscribers; a subscriber that
	// falls behind misses events.
	Publish(ctx context.Context, e Event) error
	// Subscribe returns a channel of events and a function that ends the
	// subscription. Events published before Subscribe returns may be
	// missed.
	Subscribe() (<-chan Event, func())
}

// subscriberBuffer is how many events a slow subscriber may lag behind
// before it misses some.
const subscriberBuffer = 256

// Local is a Bus within one process.
type Local struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

var _ Bus = (*Local)(nil)

// NewLocal returns an in-process bus.
func NewLocal() *Local {
	return &Local{subs: map[chan Event]struct{}{}}
}

// Publish implements Bus. It never fails.
func (l *Local) Publish(_ context.Context, e Event) error {
	l.deliver(e)
	return nil
}

func (l *Local) deliver(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ch := range l.subs {
		select {
		case ch <- e:
		default:
			// The subscriber is behind; it polls as well, so dropping is
			// safe and keeps publishers from blocking.
		}
	}
}

// Subscribe implements Bus.
func (l *Local) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	l.mu.Lock()
	l.subs[ch] = struct{}{}
	l.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			l.mu.Lock()
			delete(l.subs, ch)
			l.mu.Unlock()
		})
	}
}
