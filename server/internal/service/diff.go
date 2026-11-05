package service

import (
	"fmt"
	"strings"
)

// Diff limits (API schema TaskDiff): a patch is left out for a file too
// large to compare line by line, and once the patches of a diff add up to
// maxDiffBytes the rest are left out and the diff is marked truncated.
const (
	maxDiffFileBytes = 256 << 10
	maxDiffBytes     = 1 << 20
	// maxDiffEdits bounds the work of the line diff. Two versions of a
	// task rarely differ by more; beyond it the file counts as replaced.
	maxDiffEdits = 1000
	diffContext  = 3
)

// lineEdit is one line of an edit script.
type lineEdit struct {
	op   byte // ' ', '-' or '+'
	text string
}

// noEOL marks, inside the diff only, a last line without a newline, so
// that "x" and "x\n" compare different as they do for diff(1).
const noEOL = "\x00no-eol"

// diffInput splits s into lines without their newlines, marking a final
// line that has none.
func diffInput(s string) []string {
	if s == "" {
		return nil
	}
	missingEOL := !strings.HasSuffix(s, "\n")
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if missingEOL {
		lines[len(lines)-1] += noEOL
	}
	return lines
}

// diffLines returns the edit script from a to b, or ok=false when the two
// differ by more than maxDiffEdits lines. It is Myers' O(ND) algorithm.
func diffLines(a, b []string) (script []lineEdit, ok bool) {
	n, m := len(a), len(b)
	limit := min(n+m, maxDiffEdits)
	offset := limit + 1
	v := make([]int, 2*limit+3)
	var trace [][]int
	found := -1
	for d := 0; d <= limit && found < 0; d++ {
		// Round d reads the frontier at diagonals -d-1 to d+1; keep only
		// those, so memory grows with the square of the edit count rather
		// than with the edit count times the file length.
		trace = append(trace, append([]int(nil), v[offset-d-1:offset+d+2]...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = d
				break
			}
		}
	}
	if found < 0 {
		return nil, false
	}
	// Walk back through the frontiers saved before each round.
	x, y := n, m
	for d := found; d > 0; d-- {
		vd := trace[d]
		at := func(k int) int { return vd[k+d+1] }
		k := x - y
		prevK := k - 1
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			script = append(script, lineEdit{' ', a[x]})
		}
		if x == prevX {
			y--
			script = append(script, lineEdit{'+', b[y]})
		} else {
			x--
			script = append(script, lineEdit{'-', a[x]})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		script = append(script, lineEdit{' ', a[x]})
	}
	for i, j := 0, len(script)-1; i < j; i, j = i+1, j-1 {
		script[i], script[j] = script[j], script[i]
	}
	return script, true
}

// fileDiff compares two versions of a text file. It returns the number
// of added and deleted lines and the unified diff, which is empty when
// the files are too large or too different to compare line by line; the
// counts then treat the file as replaced.
func fileDiff(path, before, after string, beforeExists, afterExists bool) (additions, deletions int, patch string) {
	a := diffInput(before)
	b := diffInput(after)
	if len(before) > maxDiffFileBytes || len(after) > maxDiffFileBytes {
		return len(b), len(a), ""
	}
	script, ok := diffLines(a, b)
	if !ok {
		return len(b), len(a), ""
	}
	for _, e := range script {
		switch e.op {
		case '+':
			additions++
		case '-':
			deletions++
		}
	}
	if additions == 0 && deletions == 0 {
		return 0, 0, ""
	}
	from, to := "a/"+path, "b/"+path
	if !beforeExists {
		from = "/dev/null"
	}
	if !afterExists {
		to = "/dev/null"
	}
	var w strings.Builder
	fmt.Fprintf(&w, "--- %s\n+++ %s\n", from, to)
	writeHunks(&w, script)
	return additions, deletions, w.String()
}

// writeHunks writes the edit script as unified diff hunks with
// diffContext lines of context.
func writeHunks(w *strings.Builder, script []lineEdit) {
	// Line numbers in a and b before each edit, for the hunk headers.
	type pos struct{ a, b int }
	at := make([]pos, len(script))
	ai, bi := 0, 0
	for i, e := range script {
		at[i] = pos{ai, bi}
		if e.op != '+' {
			ai++
		}
		if e.op != '-' {
			bi++
		}
	}
	for i := 0; i < len(script); {
		if script[i].op == ' ' {
			i++
			continue
		}
		start := max(0, i-diffContext)
		last := i
		for j := i; j < len(script); j++ {
			if script[j].op != ' ' {
				last = j
			} else if j-last > 2*diffContext {
				break
			}
		}
		end := min(len(script), last+diffContext+1)
		var aLen, bLen int
		for _, e := range script[start:end] {
			if e.op != '+' {
				aLen++
			}
			if e.op != '-' {
				bLen++
			}
		}
		fmt.Fprintf(w, "@@ -%s +%s @@\n", hunkRange(at[start].a, aLen), hunkRange(at[start].b, bLen))
		for _, e := range script[start:end] {
			text, missingEOL := strings.CutSuffix(e.text, noEOL)
			w.WriteByte(e.op)
			w.WriteString(text)
			w.WriteByte('\n')
			if missingEOL {
				w.WriteString("\\ No newline at end of file\n")
			}
		}
		i = end
	}
}

// hunkRange formats the start,length part of a hunk header. Lines are
// numbered from 1; an empty range names the line before it.
func hunkRange(start, length int) string {
	switch length {
	case 0:
		return fmt.Sprintf("%d,0", start)
	case 1:
		return fmt.Sprintf("%d", start+1)
	default:
		return fmt.Sprintf("%d,%d", start+1, length)
	}
}
