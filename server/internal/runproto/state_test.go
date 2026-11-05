package runproto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestStateCanonicalRoundTrip(t *testing.T) {
	snapshot := StateSnapshot{Version: StateFormatVersion, Entries: []StateEntry{
		{Path: "tree/zero"},
		{Path: "tree/exec", Content: []byte{0, 255, 1}, Executable: true},
		{Path: "tree", Directory: true},
		{Path: "empty", Directory: true},
	}}
	original := append([]StateEntry(nil), snapshot.Entries...)
	encoded, size, err := EncodeState(snapshot)
	if err != nil || size != 3 {
		t.Fatalf("EncodeState size=%d err=%v", size, err)
	}
	if !reflect.DeepEqual(snapshot.Entries, original) {
		t.Fatal("encoding changed the caller's entry order")
	}
	decoded, gotSize, err := DecodeState(encoded)
	if err != nil || gotSize != size {
		t.Fatalf("DecodeState size=%d err=%v", gotSize, err)
	}
	want := []StateEntry{original[3], original[2], original[1], original[0]}
	if !reflect.DeepEqual(decoded.Entries, want) {
		t.Fatalf("round trip entries=%+v", decoded.Entries)
	}
	again, _, err := EncodeState(decoded)
	if err != nil || !bytes.Equal(encoded, again) {
		t.Fatalf("encoding is not canonical: %v", err)
	}
	for _, entries := range [][]StateEntry{nil, {}} {
		empty, size, err := EncodeState(StateSnapshot{Version: StateFormatVersion, Entries: entries})
		if err != nil || size != 0 || string(empty) != `{"version":1,"entries":[]}` {
			t.Fatalf("empty snapshot=%s size=%d err=%v", empty, size, err)
		}
	}
}

func TestStateRejectsInvalidTrees(t *testing.T) {
	for _, name := range []string{"", ".", "..", "/outside", "a/../outside", "a//b", "a/./b", "a/", "a\\b", "a\x00b", "C:/outside", "c:outside", string([]byte{255})} {
		t.Run(fmt.Sprintf("path_%q", name), func(t *testing.T) {
			_, _, err := EncodeState(StateSnapshot{Version: StateFormatVersion, Entries: []StateEntry{{Path: name}}})
			if err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	for _, tt := range []struct {
		name    string
		entries []StateEntry
	}{
		{"duplicate", []StateEntry{{Path: "a"}, {Path: "a"}}},
		{"missing parent", []StateEntry{{Path: "a/b"}}},
		{"missing ancestor", []StateEntry{{Path: "a/b", Directory: true}, {Path: "a/b/c"}}},
		{"file parent", []StateEntry{{Path: "a"}, {Path: "a/b"}}},
		{"directory content", []StateEntry{{Path: "a", Directory: true, Content: []byte("data")}}},
		{"directory execute", []StateEntry{{Path: "a", Directory: true, Executable: true}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := EncodeState(StateSnapshot{Version: StateFormatVersion, Entries: tt.entries}); err == nil {
				t.Fatal("invalid tree accepted")
			}
		})
	}
}

func TestStateBounds(t *testing.T) {
	content := make([]byte, MaxStateBytes)
	snapshot := StateSnapshot{Version: StateFormatVersion, Entries: []StateEntry{{Path: "payload", Content: content}}}
	encoded, size, err := EncodeState(snapshot)
	if err != nil || size != MaxStateBytes || len(encoded) > MaxStateWireBytes {
		t.Fatalf("exact content bound size=%d encoded=%d err=%v", size, len(encoded), err)
	}
	if _, size, err := DecodeState(encoded); err != nil || size != MaxStateBytes {
		t.Fatalf("decode exact content bound size=%d err=%v", size, err)
	}
	snapshot.Entries = append(snapshot.Entries, StateEntry{Path: "extra", Content: []byte{0}})
	if _, _, err := EncodeState(snapshot); err == nil {
		t.Fatal("accepted content beyond limit across files")
	}
	entries := make([]StateEntry, MaxStateEntries)
	for i := range entries {
		entries[i].Path = fmt.Sprintf("entry-%04d", i)
	}
	if _, _, err := EncodeState(StateSnapshot{Version: 1, Entries: entries}); err != nil {
		t.Fatalf("exact entry limit: %v", err)
	}
	entries = append(entries, StateEntry{Path: "extra"})
	if _, _, err := EncodeState(StateSnapshot{Version: 1, Entries: entries}); err == nil {
		t.Fatal("accepted extra entry")
	}
	for _, n := range []int{MaxStatePathBytes, MaxStatePathBytes + 1} {
		_, _, err := EncodeState(StateSnapshot{Version: 1, Entries: []StateEntry{{Path: strings.Repeat("a", n)}}})
		if (err == nil) != (n == MaxStatePathBytes) {
			t.Fatalf("path byte bound %d: %v", n, err)
		}
	}
	// Multibyte paths are bounded by bytes, not rune count.
	if _, _, err := EncodeState(StateSnapshot{Version: 1, Entries: []StateEntry{{Path: strings.Repeat("文", 86)}}}); err == nil {
		t.Fatal("accepted multibyte path beyond byte limit")
	}
	valid := []byte(`{"version":1,"entries":[]}`)
	exact := append(valid, bytes.Repeat([]byte{' '}, MaxStateWireBytes-len(valid))...)
	if _, _, err := DecodeState(exact); err != nil {
		t.Fatalf("exact wire limit: %v", err)
	}
	if _, _, err := DecodeState(append(exact, ' ')); err == nil {
		t.Fatal("accepted encoded bytes beyond wire limit")
	}
}

func TestStateDecodeIsStrict(t *testing.T) {
	for _, body := range []string{
		`null`, `[]`, `{}`, `{"version":1}`, `{"entries":[]}`, `{"version":2,"entries":[]}`,
		`{"version":1,"entries":null}`, `{"version":1,"entries":{}}`,
		`{"version":1,"entries":[],"unknown":true}`, `{"version":1,"entries":[{"path":"a","unknown":true}]}`,
		`{"version":1,"version":1,"entries":[]}`, `{"version":1,"entries":[],"entries":[]}`,
		`{"version":1,"entries":[]} {}`, `{"version":1,"entries":[]}x`,
		`{"version":1,"entries":[null]}`, `{"version":1,"entries":[{"path":"a","content":"???"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			if _, _, err := DecodeState([]byte(body)); err == nil {
				t.Fatal("malformed snapshot accepted")
			}
		})
	}
	if _, _, err := DecodeState([]byte("{\"version\":1,\"entries\":[{\"path\":\"\xff\"}]}")); err == nil {
		t.Fatal("invalid wire UTF-8 accepted after JSON replacement")
	}
	entries := make([]StateEntry, MaxStateEntries)
	for i := range entries {
		entries[i].Path = fmt.Sprintf("entry-%04d", i)
	}
	head, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	// An invalid extra entry is never decoded or allocated after the limit.
	body := `{"version":1,"entries":` + string(head[:len(head)-1]) + `,{"incomplete":`
	if _, _, err := DecodeState([]byte(body)); err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("entry limit was not enforced during decoding: %v", err)
	}
}

func TestStateErrorsDoNotEchoPrivateData(t *testing.T) {
	private := "synthetic-private-file-name"
	_, _, err := EncodeState(StateSnapshot{Version: 1, Entries: []StateEntry{{Path: private + "/"}}})
	if err == nil || strings.Contains(err.Error(), private) {
		t.Fatal("path validation echoed private data")
	}
	_, _, err = DecodeState([]byte(`{"version":1,"entries":[{"` + private + `":"value"}]}`))
	if err == nil || strings.Contains(err.Error(), private) {
		t.Fatal("JSON validation echoed private field data")
	}
}
