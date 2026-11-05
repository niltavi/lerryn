package runner

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// logLimits are the per-run log limits (tech design 6.5).
type logLimits struct {
	// lineBytes is the longest line text; longer lines are cut and marked.
	lineBytes int
	// runLines and runBytes cap one run's log. Text bytes are counted.
	runLines int64
	runBytes int64
	// reserveLines and reserveBytes are kept back from task output for the
	// runner's own system lines, so the truncation notice and the closing
	// lines about how the run ended still fit once the task hit the limit.
	reserveLines int64
	reserveBytes int64
	// batchBytes triggers an upload before the flush interval, and caps
	// the text of one AppendLogs call.
	batchBytes int
}

func defaultLogLimits() logLimits {
	return logLimits{
		lineBytes:    runproto.MaxLineBytes,
		runLines:     runproto.MaxRunLines,
		runBytes:     runproto.MaxRunBytes,
		reserveLines: 1000,
		reserveBytes: 1 << 20,
		batchBytes:   256 << 10,
	}
}

// truncatedMarker ends a line that was cut at the line limit.
const truncatedMarker = " …[line truncated]"

// logPipeline turns a run's raw output into numbered log lines and queues
// them for upload. stdout, stderr and the runner's system messages share one
// sequence. Lines are redacted before they are numbered, so a line that
// redaction shortens or drops never leaves a gap.
type logPipeline struct {
	limits logLimits
	// stages is the stage lines are labelled with. It follows stage
	// markers on stdout where there are any, and the events file
	// otherwise; see marks.
	stages *stageTracker
	redact func(string) string
	// secrets redacts the run's credential values; nil until the run has
	// fetched them.
	secrets *redactor
	now     func() time.Time

	mu sync.Mutex
	// marks is nil until useMarkers.
	marks   *markState
	seq     int64
	lines   int64
	bytes   int64
	dropped runproto.Dropped
	noticed bool
	pending []runproto.LogLine
	// pendingBytes is the text size of pending.
	pendingBytes int
	// sentDropped is the Dropped total the server last acknowledged.
	sentDropped runproto.Dropped

	// kick asks the uploader to send now instead of at the next tick.
	kick chan struct{}
	// beforeOutput, when set, runs before container output is split, with
	// no lock held. The runner reads new sbx events there, so stage changes
	// the task made before printing are applied before its lines are
	// labelled.
	beforeOutput func()
}

func newLogPipeline(limits logLimits, stages *stageTracker, redact func(string) string, now func() time.Time) *logPipeline {
	return &logPipeline{
		limits: limits,
		stages: stages,
		redact: redact,
		now:    now,
		kick:   make(chan struct{}, 1),
	}
}

// output returns writers for one container's stdout and stderr, and a
// function that emits a final line left without a newline. Call it after
// the container's output was fully written.
func (p *logPipeline) output() (executor.Output, func()) {
	return p.newOutput(false)
}

// taskOutput is output for the run's own container, whose stdout carries
// stage markers. Its flush also ends the wait for markers that did not
// arrive.
func (p *logPipeline) taskOutput() (executor.Output, func()) {
	return p.newOutput(true)
}

func (p *logPipeline) newOutput(markers bool) (executor.Output, func()) {
	stdout := &lineSplitter{p: p, stream: runproto.StreamStdout, markers: markers}
	stderr := &lineSplitter{p: p, stream: runproto.StreamStderr}
	return executor.Output{Stdout: stdout, Stderr: stderr}, func() {
		stdout.flush()
		stderr.flush()
		if markers {
			p.closeMarks()
		}
	}
}

// useSecrets makes the pipeline redact r's values from every later line,
// and from the events and messages it is asked to redact.
func (p *logPipeline) useSecrets(r *redactor) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.secrets = r
}

// redactText redacts a message that leaves the runner outside the log,
// such as a stage message or an error message.
func (p *logPipeline) redactText(s string) string {
	p.mu.Lock()
	r := p.secrets
	p.mu.Unlock()
	if p.redact != nil {
		s = p.redact(s)
	}
	return r.redact(s)
}

// redactEvent redacts the texts and result data of an event the task
// reported (tech design 12.6).
func (p *logPipeline) redactEvent(ev runproto.Event) runproto.Event {
	p.mu.Lock()
	r := p.secrets
	p.mu.Unlock()
	if r == nil && p.redact == nil {
		return ev
	}
	ev.Message = p.redactTaskText(r, ev.Message)
	if ev.Result != nil {
		res := *ev.Result
		res.Summary = p.redactTaskText(r, res.Summary)
		res.Data = r.redactJSON(res.Data)
		ev.Result = &res
	}
	if ev.ToolCall != nil {
		call := *ev.ToolCall
		call.Method = p.redactTaskText(r, call.Method)
		call.ArgsSummary = p.redactTaskText(r, call.ArgsSummary)
		call.Error = p.redactTaskText(r, call.Error)
		ev.ToolCall = &call
	}
	return ev
}

// sdkCutMark ends a text the SDKs in runtimes/sdk cut to their limits
// before writing it to the events file.
const sdkCutMark = "…"

// redactTaskText redacts an event text from the task. The SDKs cut long
// texts inside the container and end them in sdkCutMark; the runner never
// sees the part that was cut off, so such a text may end in the beginning
// of a value, which is masked as on a log line cut at its limit. A text
// that merely ends in "…" may lose a few characters to this; that is the
// price of not leaking a value's prefix.
func (p *logPipeline) redactTaskText(r *redactor, s string) string {
	if p.redact != nil {
		s = p.redact(s)
	}
	body, cut := strings.CutSuffix(s, sdkCutMark)
	if !cut {
		return r.redact(s)
	}
	return r.redactCut(body) + sdkCutMark
}

// system adds a line from the runner itself.
func (p *logPipeline) system(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emitLocked(runproto.StreamSystem, fmt.Sprintf(format, args...), false)
}

// emitLocked turns one raw line into a log line and queues it, unless the
// run's limits are reached. cut is set when the raw line was already cut at
// the line limit.
func (p *logPipeline) emitLocked(stream, text string, cut bool) {
	text = sanitize(text)
	if p.redact != nil {
		text = p.redact(text)
	}
	if cut {
		text = p.secrets.redactCut(text)
	} else {
		text = p.secrets.redact(text)
	}
	text = fitLine(text, p.limits.lineBytes, cut)

	maxLines, maxBytes := p.limits.runLines, p.limits.runBytes
	if stream != runproto.StreamSystem {
		maxLines -= p.limits.reserveLines
		maxBytes -= p.limits.reserveBytes
	}
	if p.lines+1 > maxLines || p.bytes+int64(len(text)) > maxBytes {
		p.dropped.Lines++
		p.dropped.Bytes += int64(len(text))
		if !p.noticed {
			p.noticed = true
			p.emitLocked(runproto.StreamSystem, fmt.Sprintf(
				"log limit reached (%d lines or %s per run): further output of this run is dropped",
				p.limits.runLines, formatBytes(p.limits.runBytes)), false)
		}
		return
	}

	p.seq++
	p.lines++
	p.bytes += int64(len(text))
	p.pending = append(p.pending, runproto.LogLine{
		Seq:    p.seq,
		TS:     p.now().UTC(),
		Stream: stream,
		Stage:  p.stages.current(),
		Text:   text,
	})
	p.pendingBytes += len(text)
	if p.pendingBytes >= p.limits.batchBytes {
		select {
		case p.kick <- struct{}{}:
		default:
		}
	}
}

// batch returns the oldest pending lines, at most batchBytes of text but
// at least one line, and the current Dropped total. It does not remove
// them: ack does, once the server stored them, so a failed upload resends
// the same lines with the same numbers.
func (p *logPipeline) batch() ([]runproto.LogLine, runproto.Dropped, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pending) == 0 && p.dropped == p.sentDropped {
		return nil, p.dropped, false
	}
	n, size := 0, 0
	for n < len(p.pending) && (n == 0 || size+len(p.pending[n].Text) <= p.limits.batchBytes) {
		size += len(p.pending[n].Text)
		n++
	}
	return append([]runproto.LogLine(nil), p.pending[:n]...), p.dropped, true
}

func (p *logPipeline) ack(n int, dropped runproto.Dropped) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.pending[:n] {
		p.pendingBytes -= len(l.Text)
	}
	p.pending = append(p.pending[:0], p.pending[n:]...)
	p.sentDropped = dropped
}

// send uploads everything pending, batch by batch. It stops at the first
// error, keeping the failed batch for the next attempt.
func (p *logPipeline) send(ctx context.Context, appendLogs func(context.Context, []runproto.LogLine, runproto.Dropped) error) error {
	for {
		lines, dropped, ok := p.batch()
		if !ok {
			return nil
		}
		if err := appendLogs(ctx, lines, dropped); err != nil {
			return err
		}
		p.ack(len(lines), dropped)
	}
}

func (p *logPipeline) idle() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pending) == 0 && p.dropped == p.sentDropped
}

// lineSplitter splits one stream into lines. The executor writes to it
// from a single goroutine; the pipeline lock still guards it because
// system lines are added concurrently.
type lineSplitter struct {
	p      *logPipeline
	stream string
	buf    []byte
	// discarding is set after a line was cut at the limit: the rest of that
	// line is dropped up to the next newline.
	discarding bool
	// markers is set on the run container's stdout, where sbx writes
	// stage markers.
	markers bool
}

func (s *lineSplitter) Write(b []byte) (int, error) {
	n := len(b)
	if s.p.beforeOutput != nil {
		s.p.beforeOutput()
	}
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	if m := s.p.marks; m != nil && s.markers {
		m.stdoutBytes += int64(n)
		m.lastStdout = s.p.now()
	}
	s.p.expireMarksLocked()
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			s.appendLocked(b)
			break
		}
		s.appendLocked(b[:i])
		s.endLineLocked()
		b = b[i+1:]
	}
	return n, nil
}

// appendLocked adds part of a line. One byte more than the limit is kept so
// that a line of exactly the limit followed by "\r\n" is not cut.
func (s *lineSplitter) appendLocked(chunk []byte) {
	if s.discarding {
		return
	}
	room := s.p.limits.lineBytes + 1 - len(s.buf)
	if len(chunk) <= room {
		s.buf = append(s.buf, chunk...)
		return
	}
	s.buf = append(s.buf, chunk[:room]...)
	s.p.emitLocked(s.stream, string(s.buf), true)
	s.buf = s.buf[:0]
	s.discarding = true
}

func (s *lineSplitter) endLineLocked() {
	if s.discarding {
		s.discarding = false
		return
	}
	line := s.buf
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	if !s.markers || !s.p.takeMarkerLocked(s.stream, line) {
		s.p.emitLocked(s.stream, string(line), false)
	}
	s.buf = s.buf[:0]
}

// flush emits a final line that had no newline.
func (s *lineSplitter) flush() {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	if len(s.buf) > 0 && !s.discarding {
		s.endLineLocked()
	}
	s.buf, s.discarding = nil, false
}

// sanitize makes text storable as text in both databases: invalid UTF-8
// becomes U+FFFD, and so does NUL, which PostgreSQL rejects in text.
func sanitize(text string) string {
	text = strings.ToValidUTF8(text, "�")
	return strings.ReplaceAll(text, "\x00", "�")
}

// fitLine cuts text to limit bytes at a rune boundary and marks the cut.
// cut forces the marker for a line that was already shortened.
func fitLine(text string, limit int, cut bool) string {
	if !cut && len(text) <= limit {
		return text
	}
	n := max(0, min(len(text), limit-len(truncatedMarker)))
	for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n] + truncatedMarker
}

// formatBytes renders a size for people.
func formatBytes(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
