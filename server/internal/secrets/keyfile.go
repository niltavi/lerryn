package secrets

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The key file (secrets.master_key_file) is text. Lines starting with '#'
// and blank lines are ignored; every other line is one master key in
// standard base64, 32 bytes. The first key is current: it wraps new data
// keys. Further keys are earlier ones, kept so records wrapped before a
// rotation still open until `lerryn secrets rewrap` has moved them to the
// current key.
const keyFileHeader = `# Lerryn master key (secrets.master_key_file).
# It decrypts every stored credential. Keep a copy apart from database
# backups: a backup without this file cannot be decrypted, and this file
# without the database holds no credentials. Every server of a deployment
# needs the same file. The first key below is current; later keys are
# earlier ones, kept until "lerryn secrets rewrap" has moved all records.
`

// keyFileMode is the mode a generated key file gets.
const keyFileMode = 0o600

// LocalKeyring holds master keys read from a local key file.
type LocalKeyring struct {
	keys []masterKey
}

type masterKey struct {
	id  string
	key []byte
}

var _ Keyring = (*LocalKeyring)(nil)

// NewLocalKeyring returns a keyring holding keys, the first one current.
func NewLocalKeyring(keys ...[]byte) (*LocalKeyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("secrets: a keyring needs at least one key")
	}
	kr := &LocalKeyring{}
	seen := map[string]bool{}
	for i, k := range keys {
		if len(k) != KeySize {
			return nil, fmt.Errorf("secrets: key %d is %d bytes, want %d", i+1, len(k), KeySize)
		}
		id := KeyID(k)
		if seen[id] {
			return nil, fmt.Errorf("secrets: key %d is listed twice", i+1)
		}
		seen[id] = true
		kr.keys = append(kr.keys, masterKey{id: id, key: bytes.Clone(k)})
	}
	return kr, nil
}

// KeyID names a master key without revealing it: "local-" and the first
// 8 bytes of a SHA-256 over a fixed label and the key. Records store it,
// so the server can tell a wrong key file from tampered data. The original
// format label is immutable because existing records contain these IDs.
func KeyID(key []byte) string {
	h := sha256.New()
	h.Write([]byte("waleng/secrets/kek-id/v1\x00"))
	h.Write(key)
	return "local-" + hex.EncodeToString(h.Sum(nil)[:8])
}

// CurrentID implements Keyring.
func (k *LocalKeyring) CurrentID() string { return k.keys[0].id }

// IDs lists the key IDs, current first.
func (k *LocalKeyring) IDs() []string {
	ids := make([]string, len(k.keys))
	for i, mk := range k.keys {
		ids[i] = mk.id
	}
	return ids
}

// Wrap implements Keyring.
func (k *LocalKeyring) Wrap(dek, aad []byte) ([]byte, string, error) {
	cur := k.keys[0]
	wrapped, err := gcmSeal(cur.key, dek, withKeyID(cur.id, aad))
	if err != nil {
		return nil, "", err
	}
	return wrapped, cur.id, nil
}

// Unwrap implements Keyring.
func (k *LocalKeyring) Unwrap(wrapped []byte, kekID string, aad []byte) ([]byte, error) {
	for _, mk := range k.keys {
		if mk.id == kekID {
			return gcmOpen(mk.key, wrapped, withKeyID(kekID, aad))
		}
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownKey, kekID)
}

// withKeyID binds the key ID too, so a record cannot claim another key.
func withKeyID(id string, aad []byte) []byte {
	return withContext(id+"\x00", aad)
}

// GenerateKey returns a new random master key.
func GenerateKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("secrets: generate master key: %w", err)
	}
	return key, nil
}

// ParseKeyFile reads the keys of a key file's content, current first.
func ParseKeyFile(data []byte) ([][]byte, error) {
	var keys [][]byte
	sc := bufio.NewScanner(bytes.NewReader(data))
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, err := base64.StdEncoding.DecodeString(text)
		if err != nil || len(key) != KeySize {
			// The line itself is never quoted: it may be a key.
			return nil, fmt.Errorf("secrets: line %d is not a base64 encoded %d-byte key", line, KeySize)
		}
		keys = append(keys, key)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("secrets: read key file: %w", err)
	}
	if len(keys) == 0 {
		return nil, errors.New("secrets: the key file holds no key")
	}
	return keys, nil
}

// FormatKeyFile returns the content of a key file holding keys, current
// first.
func FormatKeyFile(keys ...[]byte) []byte {
	var b bytes.Buffer
	b.WriteString(keyFileHeader)
	for _, k := range keys {
		b.WriteString(base64.StdEncoding.EncodeToString(k))
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// KeyFileStatus describes a loaded key file.
type KeyFileStatus struct {
	// Created is true when the file did not exist and was generated.
	Created bool
	// LooseMode is true when group or others may read the file.
	LooseMode bool
}

// LoadKeyFile reads the keyring from path.
func LoadKeyFile(path string) (*LocalKeyring, KeyFileStatus, error) {
	var st KeyFileStatus
	fi, err := os.Stat(path)
	if err != nil {
		return nil, st, fmt.Errorf("secrets: master key file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, st, fmt.Errorf("secrets: master key file %s is not a regular file", path)
	}
	st.LooseMode = fi.Mode().Perm()&0o077 != 0
	data, err := os.ReadFile(path) // #nosec G304 -- the configured key file
	if err != nil {
		return nil, st, fmt.Errorf("secrets: master key file: %w", err)
	}
	defer clear(data)
	keys, err := ParseKeyFile(data)
	if err != nil {
		return nil, st, fmt.Errorf("%w (%s)", err, path)
	}
	kr, err := NewLocalKeyring(keys...)
	for _, k := range keys {
		clear(k)
	}
	if err != nil {
		return nil, st, err
	}
	return kr, st, nil
}

// LoadOrCreateKeyFile reads the keyring from path, generating a key file
// with a new key first if none exists (tech design 15.2). Several servers
// starting together on a shared volume agree on one key: the file is
// written under a temporary name and linked into place, which fails for
// all but the first.
func LoadOrCreateKeyFile(path string) (*LocalKeyring, KeyFileStatus, error) {
	kr, st, err := LoadKeyFile(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return kr, st, err
	}
	key, err := GenerateKey()
	if err != nil {
		return nil, st, err
	}
	defer clear(key)
	created, err := writeNewKeyFile(path, FormatKeyFile(key))
	if err != nil {
		return nil, st, err
	}
	kr, st, err = LoadKeyFile(path)
	st.Created = created
	return kr, st, err
}

// writeNewKeyFile creates path with data unless it exists. It reports
// whether this call created it.
func writeNewKeyFile(path string, data []byte) (bool, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("secrets: create the master key directory: %w", err)
	}
	tmp, err := writeTemp(dir, data)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("secrets: create the master key file: %w", err)
	}
	return true, nil
}

// ReplaceKeyFile atomically replaces path with data, keeping mode 0600.
func ReplaceKeyFile(path string, data []byte) error {
	tmp, err := writeTemp(filepath.Dir(path), data)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secrets: replace the master key file: %w", err)
	}
	return nil
}

func writeTemp(dir string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, ".master-key-*")
	if err != nil {
		return "", fmt.Errorf("secrets: write the master key file: %w", err)
	}
	name := f.Name()
	err = f.Chmod(keyFileMode)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("secrets: write the master key file: %w", err)
	}
	return name, nil
}

// DeriveKey returns a 32-byte key for purpose, derived from the current
// master key with HMAC-SHA256. Servers sharing a key file derive the same
// key, so it suits values that any server must verify, such as the fill
// page's CSRF tokens. A rotation changes it. The original derivation label
// is part of the key format and remains stable across product renames.
func (k *LocalKeyring) DeriveKey(purpose string) []byte {
	m := hmac.New(sha256.New, k.keys[0].key)
	m.Write([]byte("waleng/secrets/derive/v1\x00" + purpose))
	return m.Sum(nil)
}
