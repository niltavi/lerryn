package runner

import (
	"bytes"
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/niltavi/lerryn/server/internal/secrets"
)

// redactedMark replaces a credential value in output.
const redactedMark = "[REDACTED]"

// Redaction limits (tech design 12.6).
const (
	// minRedactValue is the shortest value that is redacted. Shorter
	// values would match ordinary text everywhere and make logs
	// unreadable; such a value is no secret worth the name anyway.
	minRedactValue = 4
	// minRedactVariant is the shortest encoded fragment that is
	// redacted. The base64 fragments of a value taken out of a longer
	// encoding lose a few characters at each end.
	minRedactVariant = 8
)

// redactor removes a run's credential values from output: the values
// themselves, every line of a multi-line value, and their base64 (at
// every alignment within a longer encoding, standard and URL alphabet),
// URL-encoded and JSON-escaped forms. Output is redacted line by line, so
// a value split across two writes of the task is still caught; a value
// cut at the line length limit is caught by redactCut.
//
// Redaction cannot stop a task that deliberately transforms a value in
// some other way before printing it (PRD section 7).
type redactor struct {
	replacer *strings.Replacer
	// patterns are longest first.
	patterns []string
}

// newRedactor returns a redactor for values, or nil when none is long
// enough to redact.
func newRedactor(values []string) *redactor {
	seen := map[string]bool{}
	var patterns []string
	add := func(p string, minLen int) {
		if len(p) >= minLen && !seen[p] {
			seen[p] = true
			patterns = append(patterns, p)
		}
	}
	for _, v := range values {
		if len(v) < minRedactValue {
			continue
		}
		forms := []string{v}
		if strings.ContainsAny(v, "\r\n") {
			forms = append(forms, strings.FieldsFunc(v, func(r rune) bool { return r == '\n' || r == '\r' })...)
		}
		for i, f := range forms {
			minLen := minRedactValue
			if i > 0 {
				minLen = minRedactVariant
			}
			add(f, minLen)
			for _, enc := range encodings(f) {
				add(enc, minRedactVariant)
			}
		}
	}
	if len(patterns) == 0 {
		return nil
	}
	// strings.Replacer tries the old strings in argument order at each
	// position, so longest first makes the longest match win.
	slices.SortStableFunc(patterns, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	pairs := make([]string, 0, 2*len(patterns))
	for _, p := range patterns {
		pairs = append(pairs, p, redactedMark)
	}
	return &redactor{replacer: strings.NewReplacer(pairs...), patterns: patterns}
}

// encodings returns the encoded forms of v that output may contain. The
// server rejects submissions containing the same forms (tech design
// 11.7), so both sides share one list.
func encodings(v string) []string { return secrets.EncodedForms(v) }

// redact replaces every known value in s.
func (r *redactor) redact(s string) string {
	if r == nil {
		return s
	}
	return r.replacer.Replace(s)
}

// redactCut is redact for a line that was cut at the length limit: the
// cut may have split a value, leaving its beginning at the end of s,
// which is masked as well.
func (r *redactor) redactCut(s string) string {
	if r == nil {
		return s
	}
	s = r.replacer.Replace(s)
	longest := 0
	for _, p := range r.patterns {
		for k := min(len(p)-1, len(s)); k > longest; k-- {
			if strings.HasSuffix(s, p[:k]) {
				longest = k
				break
			}
		}
	}
	if longest == 0 {
		return s
	}
	cut := len(s) - longest
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + redactedMark
}

// redactJSON redacts the strings, keys and numbers of a JSON value,
// keeping it valid JSON. A number that matches a value becomes the string
// redactedMark.
func (r *redactor) redactJSON(raw json.RawMessage) json.RawMessage {
	if r == nil || len(raw) == 0 {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		// Not JSON; the event parser only passes valid JSON, so this is
		// a safety net that drops the data rather than risk a value.
		return nil
	}
	out, err := json.Marshal(r.redactValue(v))
	if err != nil {
		return nil
	}
	return out
}

func (r *redactor) redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return r.redact(x)
	case json.Number:
		if s := r.redact(x.String()); s != x.String() {
			return redactedMark
		}
		return x
	case []any:
		for i := range x {
			x[i] = r.redactValue(x[i])
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[r.redact(k)] = r.redactValue(val)
		}
		return out
	}
	return v
}
