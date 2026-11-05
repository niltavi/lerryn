package runner

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Stage markers make the stage labels of log lines exact (tech design 7.7).
//
// The events file and the container's output are separate channels, so a
// stage event read from the file says nothing about which output lines came
// before it. sbx therefore also writes each stage event as a marker line on
// stdout, after flushing the task's output and before appending the event to
// the file:
//
//	\x1elerryn <token> <mark> <kind> <stage>
//
// token is LERRYN_LOG_MARKER, a random value the runner gives each run, so
// output that merely looks like a marker (a task printing a file, say) is
// not taken for one. mark is a random hex name for this event, which the
// event's line in the file repeats as "mark". The runner removes marker
// lines from the log and switches the label stage at exactly that point in
// the stream; text before the marker on the same line stays a line of its
// own.
//
// The events file remains the source of truth for the stage state and times
// reported to the server. For labels, it is the fallback: an event without
// a mark labels lines from when it is read, as before markers; an event
// whose marker did not arrive (stdout redirected, the marker cut off in an
// over-long line) waits for it a bounded time, and labels lines from then
// on. Stderr is a separate stream, so its lines carry the stage of the
// stdout position they arrive next to, which is close but not exact.
const (
	markerPrefix = "\x1elerryn "
	// markerWaitBytes is how much stdout may arrive after an event was
	// read from the file before the runner stops waiting for its marker.
	// sbx writes the marker before the event, so it is already in the
	// pipes between the container and the runner, which hold far less.
	markerWaitBytes = 4 << 20
	// markerQuiet is how long stdout must be silent, with the event
	// waiting at least as long, before the runner stops waiting: with no
	// output in flight the marker is not coming.
	markerQuiet = time.Second
	// maxTrackedMarks bounds the marks remembered while one side of a
	// pair is missing.
	maxTrackedMarks = maxRunEvents
)

var markPattern = regexp.MustCompile(`^[0-9a-f]{1,32}$`)

// markState is the marker side of a log pipeline, guarded by its lock.
type markState struct {
	// prefix is markerPrefix with the run's token and a space.
	prefix []byte
	// seen holds marks whose marker arrived before the event was read
	// from the file; that event then changes no labels.
	seen map[string]bool
	// fellBack holds marks applied from the file because their marker
	// did not arrive in time; a late marker is then only removed.
	fellBack map[string]bool
	// awaiting are events read from the file, in order, whose markers
	// have not arrived yet.
	awaiting    []awaitingMark
	stdoutBytes int64
	lastStdout  time.Time
	// closed is set once the run container's stdout ended.
	closed bool
}

type awaitingMark struct {
	mark string
	ev   runproto.Event
	// at and offset are the time and the stdout byte count when the
	// event was read.
	at     time.Time
	offset int64
}

// useMarkers turns on marker handling with the run's token.
func (p *logPipeline) useMarkers(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.marks = &markState{
		prefix:   []byte(markerPrefix + token + " "),
		seen:     map[string]bool{},
		fellBack: map[string]bool{},
	}
}

// fileEvent applies a stage event read from the events file to the labels,
// now or once its marker settles it. mark is the event's "mark", if any.
func (p *logPipeline) fileEvent(ev runproto.Event, mark string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m := p.marks; m != nil && mark != "" {
		if m.seen[mark] {
			delete(m.seen, mark)
			return
		}
		if !m.closed {
			m.awaiting = append(m.awaiting, awaitingMark{mark: mark, ev: ev, at: p.now(), offset: m.stdoutBytes})
			return
		}
	}
	p.stages.apply(ev)
}

// takeMarkerLocked handles a complete stdout line that may carry a marker.
// It reports whether it did; the line is then not logged as it is.
func (p *logPipeline) takeMarkerLocked(stream string, line []byte) bool {
	m := p.marks
	if m == nil {
		return false
	}
	i := bytes.Index(line, m.prefix)
	if i < 0 {
		return false
	}
	ev, mark, ok := parseMarker(string(line[i+len(m.prefix):]))
	if !ok {
		return false
	}
	if i > 0 {
		// The task left a line unfinished before the stage changed.
		p.emitLocked(stream, string(line[:i]), false)
	}
	p.markerLocked(ev, mark)
	return true
}

// parseMarker parses "<mark> <kind> <stage>".
func parseMarker(s string) (runproto.Event, string, bool) {
	f := strings.Split(s, " ")
	if len(f) != 3 || !markPattern.MatchString(f[0]) || !stageIDPattern.MatchString(f[2]) {
		return runproto.Event{}, "", false
	}
	switch f[1] {
	case runproto.EventStageStart, runproto.EventStageDone, runproto.EventStageFail:
	default:
		return runproto.Event{}, "", false
	}
	return runproto.Event{Kind: f[1], Stage: f[2]}, f[0], true
}

func (p *logPipeline) markerLocked(ev runproto.Event, mark string) {
	m := p.marks
	if m.fellBack[mark] {
		delete(m.fellBack, mark)
		return
	}
	// Events read before this one whose markers have not come wrote them
	// to some other place: sbx writes a marker before its event, so they
	// would be ahead of this marker in the stream. They go first, in
	// order.
	i := slices.IndexFunc(m.awaiting, func(a awaitingMark) bool { return a.mark == mark })
	n := len(m.awaiting)
	if i >= 0 {
		n = i
	}
	for _, a := range m.awaiting[:n] {
		p.fallBackLocked(a)
	}
	if i >= 0 {
		m.awaiting = slices.Delete(m.awaiting, 0, i+1)
	} else {
		m.awaiting = m.awaiting[:0]
		if len(m.seen) < maxTrackedMarks {
			m.seen[mark] = true
		}
	}
	p.stages.apply(ev)
}

func (p *logPipeline) fallBackLocked(a awaitingMark) {
	p.stages.apply(a.ev)
	if len(p.marks.fellBack) < maxTrackedMarks {
		p.marks.fellBack[a.mark] = true
	}
}

// expireMarks stops waiting for markers that are not coming.
func (p *logPipeline) expireMarks() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireMarksLocked()
}

func (p *logPipeline) expireMarksLocked() {
	m := p.marks
	if m == nil || len(m.awaiting) == 0 {
		return
	}
	now := p.now()
	quiet := now.Sub(m.lastStdout) >= markerQuiet
	n := 0
	for _, a := range m.awaiting {
		if m.stdoutBytes-a.offset < markerWaitBytes && (!quiet || now.Sub(a.at) < markerQuiet) {
			break
		}
		p.fallBackLocked(a)
		n++
	}
	m.awaiting = slices.Delete(m.awaiting, 0, n)
}

// closeMarks ends the wait for markers once the run container's stdout
// ended: whatever is still awaited applies now, and later events from the
// file apply when read.
func (p *logPipeline) closeMarks() {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.marks
	if m == nil {
		return
	}
	m.closed = true
	for _, a := range m.awaiting {
		p.fallBackLocked(a)
	}
	m.awaiting = nil
}
