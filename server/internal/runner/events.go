package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// The events file (tech design 7.7) is written by sbx inside the container
// and read here. It is NDJSON: one JSON object per line, each line written
// with a single append. Format version 1:
//
//	{"v":1,"ts":"2026-11-02T08:00:01.234Z","kind":"stage.start","stage":"fetch"}
//	{"v":1,"ts":"2026-11-02T08:00:03.120Z","kind":"stage.done","stage":"fetch","message":"fetched 120 records"}
//	{"v":1,"ts":"2026-11-02T08:00:09.877Z","kind":"stage.fail","stage":"save","message":"HTTP 503"}
//	{"v":1,"ts":"2026-11-02T08:00:09.901Z","kind":"result","status":"partial","summary":"2 of 3 saved","data":{"saved":2}}
//
// The fields:
//
//   - v is the format version; a missing v is read as 1.
//   - ts is RFC 3339 in UTC; when missing, invalid or more than an hour from
//     the time the runner reads the line, the read time is used.
//   - kind is stage.start, stage.done, stage.fail or result.
//   - Stage events carry stage, an ID like the manifest's
//     (^[a-z0-9][a-z0-9_-]{0,62}$), and an optional message.
//   - Result events carry status (ok, partial or failed), summary and an
//     optional data value of any JSON type.
//   - Stage events written by sbx also carry mark, the name of the marker
//     sbx wrote to stdout for the same event (markers.go).
//   - tool.call events, written by sbx mcp call, carry tool (a registered
//     tool's name), method, ok, duration_ms, args_summary (at most 200
//     characters of the compact arguments) and, when ok is false, error.
//     The summary and error are redacted like stage messages.
//   - Unknown fields are ignored.
//
// Limits: message 1 KiB and summary 2 KiB (longer text is cut at a
// character boundary and ends in "…"), data 64 KiB encoded (larger data is
// dropped with a warning, the rest of the result is kept), a line 128 KiB
// (longer lines are skipped). A line that breaks these rules becomes a
// warning in the run's system log and the run carries on.
//
// The SDKs in runtimes/sdk (Python, Node and the sh command) write this
// format; runtimes/sdk/python/README.md documents it for task authors.
const (
	eventFormatVersion = 1

	maxEventMessageBytes = 1 << 10
	maxEventSummaryBytes = 2 << 10
	maxEventDataBytes    = 64 << 10
	maxEventLineBytes    = 128 << 10

	// maxRunEvents caps the events of one run; a task that reports
	// thousands of stages is broken, and the cap keeps it from flooding
	// the run's record.
	maxRunEvents = 10_000
	// maxEventsFileBytes caps how much of the file is read at all.
	maxEventsFileBytes = 64 << 20
	// eventReadChunk is how much one poll reads at most, so a burst does
	// not hold up the uploader.
	eventReadChunk = 1 << 20
	// eventBatch caps the events of one AppendEvents call.
	eventBatch = 500
	// maxEventClockSkew is how far an event's ts may be from the time it
	// was read before the read time is used instead.
	maxEventClockSkew = time.Hour
)

var stageIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

var resultStatuses = []string{"ok", "partial", "failed"}

// toolNamePattern is the rule for registered tool names.
var toolNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type wireEvent struct {
	V       *int            `json:"v"`
	TS      string          `json:"ts"`
	Kind    string          `json:"kind"`
	Stage   string          `json:"stage"`
	Message string          `json:"message"`
	Status  string          `json:"status"`
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data"`
	Mark    string          `json:"mark"`
	// tool.call fields.
	Tool        string `json:"tool"`
	Method      string `json:"method"`
	OK          *bool  `json:"ok"`
	DurationMS  *int64 `json:"duration_ms"`
	ArgsSummary string `json:"args_summary"`
	Error       string `json:"error"`
}

// parseEvent decodes one line of the events file. Seq is left zero. mark is
// the stage event's marker name (markers.go), empty when it has none. The
// warnings are about data that was dropped but did not invalidate the
// event. Texts are left at full length: they are redacted first and cut
// by limitEvent afterwards, since cutting first could leave the beginning
// of a credential value that redaction no longer recognises.
func parseEvent(line []byte, now time.Time) (ev runproto.Event, mark string, warnings []string, err error) {
	var w wireEvent
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&w); err != nil {
		return ev, "", nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if dec.More() {
		return ev, "", nil, errors.New("more than one JSON value on the line")
	}
	if w.V != nil && *w.V != eventFormatVersion {
		return ev, "", nil, fmt.Errorf("unsupported format version %d", *w.V)
	}

	ev.Kind = w.Kind
	// The task shares the host's clock, so its timestamps are kept; one far
	// off is a bug in the task and would scramble the run's timeline.
	ev.TS = now.UTC()
	if ts, err := time.Parse(time.RFC3339Nano, w.TS); err == nil && ts.Sub(now).Abs() <= maxEventClockSkew {
		ev.TS = ts.UTC()
	}

	switch w.Kind {
	case runproto.EventStageStart, runproto.EventStageDone, runproto.EventStageFail:
		if !stageIDPattern.MatchString(w.Stage) {
			return ev, "", nil, fmt.Errorf("stage id %q must match %s", w.Stage, stageIDPattern)
		}
		ev.Stage = w.Stage
		ev.Message = sanitize(w.Message)
		// A malformed mark only loses the marker match; the event stands.
		if markPattern.MatchString(w.Mark) {
			mark = w.Mark
		}
	case runproto.EventResult:
		if !slices.Contains(resultStatuses, w.Status) {
			return ev, "", nil, fmt.Errorf("result status %q must be ok, partial or failed", w.Status)
		}
		res := &runproto.Result{Status: w.Status, Summary: sanitize(w.Summary)}
		if data := bytes.TrimSpace(w.Data); len(data) > 0 && !bytes.Equal(data, []byte("null")) {
			var compact bytes.Buffer
			if err := json.Compact(&compact, data); err != nil {
				return ev, "", nil, fmt.Errorf("result data: %w", err)
			}
			switch {
			case !utf8.Valid(compact.Bytes()):
				// Both databases reject invalid UTF-8 in JSON.
				warnings = append(warnings, "result data is not valid UTF-8; the data was dropped")
			case compact.Len() > maxEventDataBytes:
				warnings = append(warnings, fmt.Sprintf("result data is %d bytes, over the %s limit; the data was dropped",
					compact.Len(), formatBytes(maxEventDataBytes)))
			default:
				res.Data = json.RawMessage(compact.Bytes())
			}
		}
		ev.Result = res
	case runproto.EventToolCall:
		if !toolNamePattern.MatchString(w.Tool) {
			return ev, "", nil, fmt.Errorf("tool name %q must match %s", w.Tool, toolNamePattern)
		}
		method := sanitize(w.Method)
		if method == "" || w.OK == nil {
			return ev, "", nil, errors.New("tool.call needs method and ok")
		}
		call := &runproto.ToolCall{
			Tool:        w.Tool,
			Method:      method,
			OK:          *w.OK,
			ArgsSummary: sanitize(w.ArgsSummary),
		}
		if !call.OK {
			call.Error = sanitize(w.Error)
		}
		if d := w.DurationMS; d != nil && *d > 0 {
			call.DurationMillis = min(*d, runproto.MaxToolCallDuration)
		}
		ev.ToolCall = call
	case "":
		return ev, "", nil, errors.New("kind is missing")
	default:
		return ev, "", nil, fmt.Errorf("unknown kind %q", w.Kind)
	}
	return ev, mark, warnings, nil
}

// limitEvent cuts the texts of a parsed event to the protocol limits. It
// runs after redaction (eventReader.handle).
func limitEvent(ev runproto.Event) runproto.Event {
	ev.Message = truncateText(ev.Message, maxEventMessageBytes)
	if ev.Result != nil {
		res := *ev.Result
		res.Summary = truncateText(res.Summary, maxEventSummaryBytes)
		ev.Result = &res
	}
	if ev.ToolCall != nil {
		call := *ev.ToolCall
		call.Method = truncateText(call.Method, runproto.MaxToolMethodBytes)
		call.ArgsSummary = truncateRunes(call.ArgsSummary, runproto.MaxToolArgsRunes)
		call.Error = truncateText(call.Error, runproto.MaxToolErrorBytes)
		ev.ToolCall = &call
	}
	return ev
}

// truncateRunes cuts text to limit runes, ending in "…".
func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	r := []rune(text)
	return string(r[:limit-1]) + "…"
}

// truncateText cuts text to limit bytes at a rune boundary, ending in "…".
func truncateText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	const ellipsis = "…"
	n := limit - len(ellipsis)
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n] + ellipsis
}

// stageTracker follows the stage the task is in, for labelling log lines
// and for the stage a failure is attributed to.
//
// Stages may nest: start marks a stage current, and done or fail of a stage
// removes it, making the enclosing stage current again.
type stageTracker struct {
	mu    sync.Mutex
	stack []string
	// failed is the last stage reported failed since the last start. A
	// task that reports "stage fail X" and then exits non-zero has left X,
	// but X is still where it failed.
	failed string
}

const maxStageDepth = 64

func (s *stageTracker) apply(ev runproto.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.Kind {
	case runproto.EventStageStart:
		s.remove(ev.Stage)
		if len(s.stack) == maxStageDepth {
			s.stack = s.stack[1:]
		}
		s.stack = append(s.stack, ev.Stage)
		s.failed = ""
	case runproto.EventStageDone:
		s.remove(ev.Stage)
	case runproto.EventStageFail:
		s.remove(ev.Stage)
		s.failed = ev.Stage
	}
}

func (s *stageTracker) remove(id string) {
	if i := slices.Index(s.stack, id); i >= 0 {
		s.stack = slices.Delete(s.stack, i, i+1)
	}
}

func (s *stageTracker) current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.stack); n > 0 {
		return s.stack[n-1]
	}
	return ""
}

// errorStage is the stage to blame for a failure: the current one, or the
// one that last reported failing.
func (s *stageTracker) errorStage() string {
	if c := s.current(); c != "" {
		return c
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// eventReader follows the events file of one run. It is polled on a timer
// and also whenever the container writes output, so a line printed after
// "sbx stage start" already carries that stage.
type eventReader struct {
	dir    string
	stages *stageTracker
	logs   *logPipeline
	now    func() time.Time

	// mu guards everything below. It is taken before the log pipeline's
	// lock, never after it.
	mu       sync.Mutex
	root     *os.Root
	f        *os.File
	disabled bool
	read     int64
	partial  []byte
	// skipping is set while the rest of an over-long line is discarded.
	skipping bool
	lineNo   int
	seq      int64
	pending  []runproto.Event
	capped   bool
}

func newEventReader(dir string, stages *stageTracker, logs *logPipeline, now func() time.Time) *eventReader {
	return &eventReader{dir: dir, stages: stages, logs: logs, now: now}
}

// poll reads new complete lines. With final set it also reports a last line
// left without a newline, which means the writer was killed mid-write.
func (e *eventReader) poll(final bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.disabled {
		return
	}
	if e.f == nil && !e.open() {
		return
	}
	limit := int64(eventReadChunk)
	if final {
		limit = maxEventsFileBytes
	}
	buf := make([]byte, 64<<10)
	var got int64
	for got < limit {
		if e.read >= maxEventsFileBytes {
			e.logs.system("sbx events: the events file is larger than %s; the rest is ignored", formatBytes(maxEventsFileBytes))
			e.closeLocked()
			e.disabled = true
			return
		}
		n, err := e.f.Read(buf)
		if n > 0 {
			got += int64(n)
			e.read += int64(n)
			e.consume(buf[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				e.logs.system("sbx events: reading the events file failed: %v", err)
				e.closeLocked()
				e.disabled = true
			}
			break
		}
	}
	if final && len(e.partial) > 0 && !e.skipping {
		e.lineNo++
		e.logs.system("sbx events: line %d ignored: incomplete line at the end of the file", e.lineNo)
		e.partial = nil
	}
}

// open opens the events file once it exists. The file lives in a directory
// the task can write, so it is opened through an os.Root (a symbolic link
// cannot point the runner at a host file) and must be a regular file (a
// FIFO would block the reader; O_NONBLOCK keeps the open itself from
// blocking).
func (e *eventReader) open() bool {
	if e.root == nil {
		root, err := os.OpenRoot(e.dir)
		if errors.Is(err, fs.ErrNotExist) {
			// Polling may start before the run directory is made.
			return false
		}
		if err != nil {
			e.logs.system("sbx events: cannot open the events directory: %v", err)
			e.disabled = true
			return false
		}
		e.root = root
	}
	f, err := openNonBlocking(e.root, eventsFileName)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		e.logs.system("sbx events: cannot open the events file: %v", err)
		e.disabled = true
		return false
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		e.logs.system("sbx events: %s is not a regular file; events are ignored", eventsFileName)
		e.disabled = true
		return false
	}
	e.f = f
	return true
}

// close releases the file. Output that still arrives afterwards, from a
// container left running at shutdown, no longer reads events.
func (e *eventReader) close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeLocked()
	e.disabled = true
}

func (e *eventReader) closeLocked() {
	if e.f != nil {
		_ = e.f.Close()
		e.f = nil
	}
	if e.root != nil {
		_ = e.root.Close()
		e.root = nil
	}
}

func (e *eventReader) consume(b []byte) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		chunk := b
		if i >= 0 {
			chunk = b[:i]
		}
		if !e.skipping {
			if len(e.partial)+len(chunk) > maxEventLineBytes {
				e.lineNo++
				e.logs.system("sbx events: line %d ignored: longer than %s", e.lineNo, formatBytes(maxEventLineBytes))
				e.partial = e.partial[:0]
				e.skipping = true
			} else {
				e.partial = append(e.partial, chunk...)
			}
		}
		if i < 0 {
			return
		}
		if e.skipping {
			e.skipping = false
		} else {
			e.lineNo++
			e.handle(e.partial)
			e.partial = e.partial[:0]
		}
		b = b[i+1:]
	}
}

func (e *eventReader) handle(line []byte) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	ev, mark, warnings, err := parseEvent(line, e.now())
	if err != nil {
		e.logs.system("sbx events: line %d ignored: %v", e.lineNo, err)
		return
	}
	ev = limitEvent(e.logs.redactEvent(ev))
	for _, w := range warnings {
		e.logs.system("sbx events: line %d: %s", e.lineNo, w)
	}
	if e.seq >= maxRunEvents {
		if !e.capped {
			e.capped = true
			e.logs.system("sbx events: more than %d events; further events are ignored", maxRunEvents)
		}
		return
	}
	e.seq++
	ev.Seq = e.seq
	if ev.Kind == runproto.EventToolCall {
		ev.Stage = e.stages.current()
	}
	e.stages.apply(ev)
	e.logs.fileEvent(ev, mark)
	e.pending = append(e.pending, ev)
}

// send uploads pending events in batches, keeping a failed batch for the
// next attempt. The lock is not held during the call, so output is not held
// up by a slow server.
func (e *eventReader) send(ctx context.Context, appendEvents func(context.Context, []runproto.Event) error) error {
	for {
		e.mu.Lock()
		batch := slices.Clone(e.pending[:min(len(e.pending), eventBatch)])
		e.mu.Unlock()
		if len(batch) == 0 {
			return nil
		}
		if err := appendEvents(ctx, batch); err != nil {
			return err
		}
		e.mu.Lock()
		e.pending = append(e.pending[:0], e.pending[len(batch):]...)
		e.mu.Unlock()
	}
}
