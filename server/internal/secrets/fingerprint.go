package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"slices"
	"strings"
)

// Fingerprints let the server find a credential value, or one of its
// common encodings, inside submitted text without decrypting every
// credential on every request (tech design 11.7). A fingerprint of a form
// (the value or an encoding of it) is two keyed digests:
//
//   - Prefix: the first 8 bytes of AES-256 over the form's first 8 bytes,
//     zero-padded to a block. Text is scanned by computing this for every
//     8-byte window, one AES block each, and looking it up.
//   - MAC: HMAC-SHA256 over the whole form, truncated to 16 bytes, which
//     confirms a prefix hit at the form's length.
//
// Both keys are derived from the current master key, so fingerprints
// stored in the database say nothing about the values to someone without
// the key file, and every server sharing the key file computes the same
// ones. Equal values give equal fingerprints; a database reader can tell
// that two credentials hold the same value, but not what it is.
//
// Forms shorter than FingerprintMinBytes are not fingerprinted: they
// would match ordinary text and reject harmless submissions.
const FingerprintMinBytes = 8

// Fingerprint identifies one form of a credential value.
type Fingerprint struct {
	Prefix uint64
	Len    int
	MAC    [16]byte
}

// Fingerprinter computes and matches fingerprints under keys derived from
// one master key.
type Fingerprinter struct {
	keyID  string
	block  cipher.Block
	macKey []byte
}

// Purposes of the derived keys. Changing them invalidates every stored
// fingerprint; the server then recomputes them from the values.
const (
	fingerprintPrefixPurpose = "credential-fingerprint/prefix/v1"
	fingerprintMACPurpose    = "credential-fingerprint/mac/v1"
)

// NewFingerprinter returns a fingerprinter keyed from kr's current master
// key.
func NewFingerprinter(kr Keyring) (*Fingerprinter, error) {
	block, err := aes.NewCipher(kr.DeriveKey(fingerprintPrefixPurpose))
	if err != nil {
		return nil, fmt.Errorf("secrets: fingerprint key: %w", err)
	}
	return &Fingerprinter{keyID: kr.CurrentID(), block: block, macKey: kr.DeriveKey(fingerprintMACPurpose)}, nil
}

// KeyID is the ID of the master key the fingerprints derive from. Stored
// fingerprints with another key ID are stale and must be recomputed.
func (f *Fingerprinter) KeyID() string { return f.keyID }

// Of returns the fingerprints of value: of the value itself, of every
// line of a multi-line value, and of their EncodedForms, each at least
// FingerprintMinBytes long. The same forms the runner redacts from
// output.
func (f *Fingerprinter) Of(value string) []Fingerprint {
	forms := []string{value}
	if strings.ContainsAny(value, "\r\n") {
		forms = append(forms, strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\r' })...)
	}
	seen := map[string]bool{}
	var out []Fingerprint
	mac := hmac.New(sha256.New, f.macKey)
	add := func(s string) {
		if len(s) < FingerprintMinBytes || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, Fingerprint{Prefix: f.prefix(s), Len: len(s), MAC: sumMAC(mac, s)})
	}
	for _, form := range forms {
		add(form)
		for _, enc := range EncodedForms(form) {
			add(enc)
		}
	}
	return out
}

func (f *Fingerprinter) prefix(s string) uint64 {
	var in, out [aes.BlockSize]byte
	copy(in[:], s[:FingerprintMinBytes])
	f.block.Encrypt(out[:], in[:])
	return binary.BigEndian.Uint64(out[:8])
}

func sumMAC(mac hash.Hash, s string) [16]byte {
	mac.Reset()
	_, _ = io.WriteString(mac, s)
	var sum [sha256.Size]byte
	var out [16]byte
	copy(out[:], mac.Sum(sum[:0]))
	return out
}

// Matcher finds fingerprinted forms in text. Each added fingerprint
// carries a label, such as the index of the credential it belongs to.
type Matcher struct {
	f        *Fingerprinter
	byPrefix map[uint64][]matchEntry
}

type matchEntry struct {
	length int
	mac    [16]byte
	label  int
}

// NewMatcher returns an empty matcher using f's keys. Fingerprints added
// to it must have been computed with the same key ID.
func (f *Fingerprinter) NewMatcher() *Matcher {
	return &Matcher{f: f, byPrefix: map[uint64][]matchEntry{}}
}

// Add adds fingerprints under label.
func (m *Matcher) Add(label int, fps []Fingerprint) {
	for _, fp := range fps {
		if fp.Len < FingerprintMinBytes {
			continue
		}
		m.byPrefix[fp.Prefix] = append(m.byPrefix[fp.Prefix], matchEntry{length: fp.Len, mac: fp.MAC, label: label})
	}
}

// Empty reports whether nothing was added.
func (m *Matcher) Empty() bool { return len(m.byPrefix) == 0 }

// Find returns the labels of the forms found anywhere in text, each once,
// in the order of their first occurrence. It costs one AES block per byte
// of text plus an HMAC per prefix hit.
func (m *Matcher) Find(text string) []int {
	if len(m.byPrefix) == 0 || len(text) < FingerprintMinBytes {
		return nil
	}
	var (
		in, out [aes.BlockSize]byte
		found   []int
		mac     = hmac.New(sha256.New, m.f.macKey)
	)
	for i := 0; i+FingerprintMinBytes <= len(text); i++ {
		copy(in[:FingerprintMinBytes], text[i:i+FingerprintMinBytes])
		m.f.block.Encrypt(out[:], in[:])
		entries := m.byPrefix[binary.BigEndian.Uint64(out[:8])]
		for _, e := range entries {
			if i+e.length > len(text) || slices.Contains(found, e.label) {
				continue
			}
			sum := sumMAC(mac, text[i:i+e.length])
			if hmac.Equal(sum[:], e.mac[:]) {
				found = append(found, e.label)
			}
		}
	}
	return found
}
