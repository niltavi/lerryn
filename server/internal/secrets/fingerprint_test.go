package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func testFingerprinter(t *testing.T, key byte) *Fingerprinter {
	t.Helper()
	kr, err := NewLocalKeyring(bytes.Repeat([]byte{key}, KeySize))
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewFingerprinter(kr)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestMatcherFindsValuesAndEncodings(t *testing.T) {
	f := testFingerprinter(t, 1)
	const token = "sk-live-4f9a2c71e0b3" // #nosec G101 -- a test value
	const other = "p@ss word/with+chars"
	m := f.NewMatcher()
	m.Add(0, f.Of(token))
	m.Add(1, f.Of(other))
	m.Add(2, f.Of("short")) // below the minimum: never matched

	quoted, _ := json.Marshal(other)
	tests := []struct {
		name string
		text string
		want []int
	}{
		{"raw", "token=" + token, []int{0}},
		{"whole text", token, []int{0}},
		{"at the end", "Bearer " + token, []int{0}},
		{"base64", base64.StdEncoding.EncodeToString([]byte(token)), []int{0}},
		{"url-safe base64", base64.URLEncoding.EncodeToString([]byte(token)), []int{0}},
		{"basic auth", "Basic " + base64.StdEncoding.EncodeToString([]byte("bob:"+token)), []int{0}},
		{"query escape", "q=" + url.QueryEscape(other), []int{1}},
		{"path escape", "/x/" + url.PathEscape(other), []int{1}},
		{"json string", `{"a":` + string(quoted) + `}`, []int{1}},
		{"both, in order", other + " " + token + " " + token, []int{1, 0}},
		{"near miss", strings.TrimSuffix(token, "3") + "4", nil},
		{"prefix only", token[:FingerprintMinBytes], nil},
		{"short value", "the value is short", nil},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.Find(tt.text); !slices.Equal(got, tt.want) {
				t.Errorf("Find(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestMultilineValueLinesAreMatched(t *testing.T) {
	f := testFingerprinter(t, 2)
	key := "-----BEGIN KEY-----\nMIIEvQIBADANBgkqhkiG\n-----END KEY-----"
	m := f.NewMatcher()
	m.Add(7, f.Of(key))
	if got := m.Find(`"MIIEvQIBADANBgkqhkiG"`); !slices.Equal(got, []int{7}) {
		t.Errorf("a line of the value: %v", got)
	}
}

func TestFingerprintsDependOnTheMasterKey(t *testing.T) {
	a, b := testFingerprinter(t, 3), testFingerprinter(t, 4)
	if a.KeyID() == b.KeyID() {
		t.Fatal("different keys share an ID")
	}
	const v = "correct horse battery staple"
	fa, fb := a.Of(v), b.Of(v)
	if len(fa) == 0 || len(fa) != len(fb) {
		t.Fatalf("forms: %d and %d", len(fa), len(fb))
	}
	for i := range fa {
		if fa[i].Prefix == fb[i].Prefix || fa[i].MAC == fb[i].MAC {
			t.Fatalf("form %d has the same digest under two master keys", i)
		}
	}
	// Fingerprints from one key never match under another.
	m := b.NewMatcher()
	m.Add(0, fa)
	if got := m.Find(v); got != nil {
		t.Errorf("stale fingerprints matched: %v", got)
	}
	// The same key gives the same fingerprints, so servers agree.
	if again := testFingerprinter(t, 3).Of(v); !slices.Equal(again, fa) {
		t.Error("fingerprints are not deterministic")
	}
}

func TestFingerprintsDoNotContainTheValue(t *testing.T) {
	f := testFingerprinter(t, 5)
	const v = "AAAAAAAAAAAAAAAAAAAAAAAA"
	for _, fp := range f.Of(v) {
		if bytes.Contains(fp.MAC[:], []byte("AAAA")) {
			t.Fatalf("digest carries the value: %x", fp.MAC)
		}
	}
}

func BenchmarkMatcherFind(b *testing.B) {
	kr, _ := NewLocalKeyring(bytes.Repeat([]byte{9}, KeySize))
	f, _ := NewFingerprinter(kr)
	m := f.NewMatcher()
	for i := range 20 {
		m.Add(i, f.Of(strings.Repeat(string(rune('a'+i)), 24)+"-secret-value"))
	}
	text := strings.Repeat("ordinary task file content, nothing secret here.\n", 1<<14) // ~800 KiB
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		if m.Find(text) != nil {
			b.Fatal("unexpected match")
		}
	}
}
