package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testKeyring(t *testing.T, keys ...[]byte) *LocalKeyring {
	t.Helper()
	if len(keys) == 0 {
		k, err := GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		keys = [][]byte{k}
	}
	kr, err := NewLocalKeyring(keys...)
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func TestSealOpenRoundTrip(t *testing.T) {
	kr := testKeyring(t)
	for _, plain := range [][]byte{nil, []byte("x"), []byte(`{"LLM_API_KEY":"sk-test-123"}`), bytes.Repeat([]byte{0xff}, 1<<16)} {
		s, err := Seal(kr, plain, []byte("cred-1/v1"))
		if err != nil {
			t.Fatal(err)
		}
		// A byte or two can occur in random ciphertext by chance; longer
		// plaintext cannot.
		if len(plain) >= 8 && bytes.Contains(s.Ciphertext, plain) {
			t.Fatal("ciphertext contains the plaintext")
		}
		if s.KEKID != kr.CurrentID() || !strings.HasPrefix(s.KEKID, "local-") {
			t.Fatalf("kek id = %q, want %q", s.KEKID, kr.CurrentID())
		}
		got, err := Open(kr, s, []byte("cred-1/v1"))
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("open = %q, %v", got, err)
		}
	}
}

func TestSealUsesFreshKeysAndNonces(t *testing.T) {
	kr := testKeyring(t)
	a, _ := Seal(kr, []byte("same"), []byte("aad"))
	b, _ := Seal(kr, []byte("same"), []byte("aad"))
	if bytes.Equal(a.Ciphertext, b.Ciphertext) || bytes.Equal(a.WrappedDEK, b.WrappedDEK) {
		t.Fatal("two seals of the same value are identical")
	}
}

// Any change to a stored record, or opening it under another record's
// associated data or key, must fail authentication rather than decrypt.
func TestOpenDetectsTampering(t *testing.T) {
	kr := testKeyring(t)
	aad := []byte("credential 0190/version 2")
	s, err := Seal(kr, []byte("sk-secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	flip := func(b []byte, i int) []byte {
		c := bytes.Clone(b)
		c[i] ^= 0x01
		return c
	}
	other := testKeyring(t)
	rotated := testKeyring(t, mustKey(t), mustKey(t))
	tests := []struct {
		name string
		kr   Keyring
		s    Sealed
		aad  []byte
		want error
	}{
		{"ciphertext nonce", kr, Sealed{flip(s.Ciphertext, 0), s.WrappedDEK, s.KEKID}, aad, ErrDecrypt},
		{"ciphertext body", kr, Sealed{flip(s.Ciphertext, len(s.Ciphertext)-1), s.WrappedDEK, s.KEKID}, aad, ErrDecrypt},
		{"truncated ciphertext", kr, Sealed{s.Ciphertext[:10], s.WrappedDEK, s.KEKID}, aad, ErrDecrypt},
		{"wrapped key", kr, Sealed{s.Ciphertext, flip(s.WrappedDEK, 20), s.KEKID}, aad, ErrDecrypt},
		{"empty wrapped key", kr, Sealed{s.Ciphertext, nil, s.KEKID}, aad, ErrDecrypt},
		{"other record", kr, s, []byte("credential 0190/version 3"), ErrDecrypt},
		{"no associated data", kr, s, nil, ErrDecrypt},
		{"unknown key id", kr, Sealed{s.Ciphertext, s.WrappedDEK, "local-0000000000000000"}, aad, ErrUnknownKey},
		{"other master key", other, Sealed{s.Ciphertext, s.WrappedDEK, other.CurrentID()}, aad, ErrDecrypt},
		{"key not in keyring", rotated, s, aad, ErrUnknownKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Open(tt.kr, tt.s, tt.aad)
			if !errors.Is(err, tt.want) || got != nil {
				t.Fatalf("open = %q, %v; want %v", got, err, tt.want)
			}
		})
	}
}

// Swapping the wrapped keys of two records must not let one record's key
// open the other's value.
func TestWrappedKeysAreBoundToTheirRecord(t *testing.T) {
	kr := testKeyring(t)
	a, _ := Seal(kr, []byte("value a"), []byte("a"))
	b, _ := Seal(kr, []byte("value b"), []byte("b"))
	if _, err := Open(kr, Sealed{a.Ciphertext, b.WrappedDEK, b.KEKID}, []byte("a")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("open with a swapped key: %v", err)
	}
	if _, err := Open(kr, Sealed{b.Ciphertext, b.WrappedDEK, b.KEKID}, []byte("a")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("open under another record's name: %v", err)
	}
}

func mustKey(t *testing.T) []byte {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRewrapMovesToTheCurrentKey(t *testing.T) {
	oldKey, newKey := mustKey(t), mustKey(t)
	before := testKeyring(t, oldKey)
	s, _ := Seal(before, []byte("sk-rotate"), []byte("r"))

	after := testKeyring(t, newKey, oldKey)
	moved, err := Rewrap(after, s, []byte("r"))
	if err != nil {
		t.Fatal(err)
	}
	if moved.KEKID != KeyID(newKey) || !bytes.Equal(moved.Ciphertext, s.Ciphertext) {
		t.Fatalf("rewrapped = %q, ciphertext changed %v", moved.KEKID, !bytes.Equal(moved.Ciphertext, s.Ciphertext))
	}
	onlyNew := testKeyring(t, newKey)
	if got, err := Open(onlyNew, moved, []byte("r")); err != nil || string(got) != "sk-rotate" {
		t.Fatalf("open after rotation = %q, %v", got, err)
	}
	if _, err := Open(onlyNew, s, []byte("r")); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("open the old record without the old key: %v", err)
	}
	if _, err := Rewrap(after, s, []byte("other")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("rewrap under the wrong name: %v", err)
	}
}

func TestNewLocalKeyringRejectsBadKeys(t *testing.T) {
	k := mustKey(t)
	for name, keys := range map[string][][]byte{
		"none":      nil,
		"short":     {k[:16]},
		"duplicate": {k, bytes.Clone(k)},
	} {
		if _, err := NewLocalKeyring(keys...); err == nil {
			t.Errorf("%s: keyring accepted", name)
		}
	}
}

func TestParseKeyFile(t *testing.T) {
	a, b := mustKey(t), mustKey(t)
	keys, err := ParseKeyFile(FormatKeyFile(a, b))
	if err != nil || len(keys) != 2 || !bytes.Equal(keys[0], a) || !bytes.Equal(keys[1], b) {
		t.Fatalf("parse = %d keys, %v", len(keys), err)
	}
	for name, content := range map[string]string{
		"empty":         "",
		"comments only": "# nothing\n\n",
		"not base64":    "not-a-key!\n",
		"short key":     "c2hvcnQ=\n",
	} {
		_, err := ParseKeyFile([]byte(content))
		if err == nil {
			t.Errorf("%s: parsed", name)
			continue
		}
		if strings.Contains(err.Error(), "not-a-key") {
			t.Errorf("%s: error quotes the line: %v", name, err)
		}
	}
}

func TestLoadOrCreateKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "master.key")
	kr, st, err := LoadOrCreateKeyFile(path)
	if err != nil || !st.Created || st.LooseMode {
		t.Fatalf("create = %+v, %v", st, err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, %v", fi.Mode(), err)
	}
	if dir, _ := os.Stat(filepath.Dir(path)); dir.Mode().Perm() != 0o700 {
		t.Fatalf("key directory mode = %v", dir.Mode())
	}
	data, _ := os.ReadFile(path) // #nosec G304 -- the test's temp file
	if !strings.HasPrefix(string(data), "# Lerryn master key") {
		t.Fatalf("key file has no header: %q", data[:20])
	}

	again, st, err := LoadOrCreateKeyFile(path)
	if err != nil || st.Created || again.CurrentID() != kr.CurrentID() {
		t.Fatalf("reload = %+v, %v; id %s vs %s", st, err, again.CurrentID(), kr.CurrentID())
	}

	if err := os.Chmod(path, 0o644); err != nil { // #nosec G302 -- loosening the mode is what the test checks
		t.Fatal(err)
	}
	if _, st, err := LoadKeyFile(path); err != nil || !st.LooseMode {
		t.Fatalf("loose mode not reported: %+v, %v", st, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

// Servers starting at the same time on a shared volume end up with one key.
func TestLoadOrCreateKeyFileConcurrently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	var wg sync.WaitGroup
	ids := make([]string, 8)
	created := make([]bool, 8)
	for i := range ids {
		wg.Go(func() {
			kr, st, err := LoadOrCreateKeyFile(path)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i], created[i] = kr.CurrentID(), st.Created
		})
	}
	wg.Wait()
	n := 0
	for i := range ids {
		if ids[i] != ids[0] {
			t.Fatalf("servers disagree on the key: %v", ids)
		}
		if created[i] {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d servers report creating the key", n)
	}
}

func TestLoadKeyFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := LoadKeyFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file loaded")
	}
	if _, _, err := LoadKeyFile(dir); err == nil {
		t.Fatal("directory loaded")
	}
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateKeyFile(bad); err == nil {
		t.Fatal("a damaged key file was accepted or replaced")
	}
	if data, _ := os.ReadFile(bad); string(data) != "garbage\n" { // #nosec G304 -- the test's temp file
		t.Fatal("a damaged key file was overwritten")
	}
}

func TestReplaceKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	a, b := mustKey(t), mustKey(t)
	if err := ReplaceKeyFile(path, FormatKeyFile(a)); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceKeyFile(path, FormatKeyFile(b, a)); err != nil {
		t.Fatal(err)
	}
	kr, _, err := LoadKeyFile(path)
	if err != nil || kr.CurrentID() != KeyID(b) || len(kr.IDs()) != 2 {
		t.Fatalf("replaced keyring = %v, %v", kr.IDs(), err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode())
	}
}
