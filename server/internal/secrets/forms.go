package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// EncodedForms returns the encoded forms of v that output and submissions
// commonly contain: base64 in the standard and URL alphabets, whole and
// as the fragments v leaves inside a longer encoding, URL encodings of
// several encoders and JSON escapes. v itself is not included. The runner
// redacts these forms from output (tech design 12.6), and the server
// rejects task submissions and run parameters containing them (11.7).
func EncodedForms(v string) []string {
	var out []string
	b := []byte(v)
	for _, alphabet := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
		out = append(out, alphabet.EncodeToString(b))
		out = append(out, base64Fragments(alphabet, b)...)
	}
	out = append(out, url.QueryEscape(v), url.PathEscape(v))
	out = append(out, percentEncodings(v)...)
	out = append(out, jsonEscapes(v)...)
	return out
}

// percentEncodings returns v as common URL encoders write it besides Go's:
// RFC 3986 strict (only unreserved characters kept), Python's
// urllib.parse.quote (also keeps '/'), JavaScript's encodeURIComponent
// (also keeps !*'()), and form encoding with '+' for spaces, each with
// upper- and lower-case hex digits.
func percentEncodings(v string) []string {
	unreserved := func(c byte) bool {
		return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~", c) >= 0
	}
	keeps := []func(byte) bool{
		unreserved,
		func(c byte) bool { return unreserved(c) || c == '/' },
		func(c byte) bool { return unreserved(c) || strings.IndexByte("!*'()", c) >= 0 },
	}
	var out []string
	for _, keep := range keeps {
		for _, plus := range []bool{false, true} {
			for _, hex := range []string{"0123456789ABCDEF", "0123456789abcdef"} {
				var b strings.Builder
				for i := 0; i < len(v); i++ {
					c := v[i]
					switch {
					case keep(c):
						b.WriteByte(c)
					case c == ' ' && plus:
						b.WriteByte('+')
					default:
						b.WriteByte('%')
						b.WriteByte(hex[c>>4])
						b.WriteByte(hex[c&15])
					}
				}
				out = append(out, b.String())
			}
		}
	}
	return out
}

// base64Fragments returns, for each of the three byte alignments v can
// have inside a longer base64 encoding (such as "user:token" in an HTTP
// Basic header), the characters that depend on v alone.
func base64Fragments(enc *base64.Encoding, v []byte) []string {
	var out []string
	for k := range 3 {
		padded := append(make([]byte, k), v...)
		s := enc.EncodeToString(padded)
		// Character i covers bits [6i, 6i+6); v covers [8k, 8(k+len)).
		start := (8*k + 5) / 6
		end := 8 * (k + len(v)) / 6
		if end > start && end <= len(s) {
			out = append(out, s[start:end])
		}
	}
	return out
}

// jsonEscapes returns v as it appears inside JSON strings written by
// common encoders: Go's (with and without HTML escaping) and the
// ASCII-only form of Python's json.dumps and JavaScript's JSON.stringify
// for non-ASCII text.
func jsonEscapes(v string) []string {
	var out []string
	for _, html := range []bool{false, true} {
		var buf bytes.Buffer
		e := json.NewEncoder(&buf)
		e.SetEscapeHTML(html)
		_ = e.Encode(v)
		s := strings.TrimSuffix(buf.String(), "\n")
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(s, `"`), `"`))
	}
	var ascii strings.Builder
	for _, r := range v {
		switch {
		case r == '"':
			ascii.WriteString(`\"`)
		case r == '\\':
			ascii.WriteString(`\\`)
		case r == '\n':
			ascii.WriteString(`\n`)
		case r == '\r':
			ascii.WriteString(`\r`)
		case r == '\t':
			ascii.WriteString(`\t`)
		case r < 0x20 || r > 0x7e:
			if r > 0xffff {
				hi, lo := utf16Pair(r)
				fmt.Fprintf(&ascii, `\u%04x\u%04x`, hi, lo)
			} else {
				fmt.Fprintf(&ascii, `\u%04x`, r)
			}
		default:
			ascii.WriteRune(r)
		}
	}
	return append(out, ascii.String())
}

func utf16Pair(r rune) (rune, rune) {
	r -= 0x10000
	return 0xd800 + (r>>10)&0x3ff, 0xdc00 + r&0x3ff
}
