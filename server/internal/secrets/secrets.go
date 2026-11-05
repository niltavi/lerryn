// Package secrets encrypts credential values at rest (tech design 12.3).
//
// Every sealed value gets its own random data key (DEK). The value is
// encrypted with the DEK using AES-256-GCM, and the DEK is encrypted
// ("wrapped") with the master key (KEK) using AES-256-GCM as well. Both
// encryptions bind associated data supplied by the caller, such as the
// credential ID and version, so a ciphertext or wrapped key copied onto
// another row fails to open instead of decrypting under the wrong name.
//
// The master key comes from a Keyring. The first implementation reads a
// local key file (keyfile.go); Vault, OpenBao or a cloud KMS can implement
// the same interface later. The master key never enters the database, so
// database backups alone cannot decrypt anything. Rotating it only
// re-wraps the data keys; ciphertexts stay as they are.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// KeySize is the size of master keys and data keys: AES-256.
const KeySize = 32

// Errors returned by Open and Keyring implementations.
var (
	// ErrDecrypt means a ciphertext or wrapped key failed authentication:
	// it was altered, belongs to another record, or was sealed under a
	// different key. The cases are deliberately indistinguishable.
	ErrDecrypt = errors.New("secrets: decryption failed")
	// ErrUnknownKey means the record names a master key the keyring does
	// not hold, for example after a rotation whose new key file has not
	// reached this server yet.
	ErrUnknownKey = errors.New("secrets: unknown master key")
)

// Keyring wraps and unwraps data keys with a master key.
type Keyring interface {
	// Wrap encrypts dek under the current master key, binding aad, and
	// returns the wrapped key and the ID of the master key used.
	Wrap(dek, aad []byte) (wrapped []byte, kekID string, err error)
	// Unwrap reverses Wrap. It returns ErrUnknownKey when kekID is not in
	// the keyring and ErrDecrypt when the wrapped key does not
	// authenticate.
	Unwrap(wrapped []byte, kekID string, aad []byte) ([]byte, error)
	// CurrentID is the ID of the master key Wrap uses.
	CurrentID() string
	// DeriveKey returns a 32-byte key for purpose, derived from the
	// current master key, the same on every server sharing it.
	DeriveKey(purpose string) []byte
}

// Sealed is an encrypted value as stored: the ciphertext under the data
// key, and the data key wrapped by the master key KEKID.
type Sealed struct {
	Ciphertext []byte
	WrappedDEK []byte
	KEKID      string
}

// Domain separation between the two layers, so a wrapped data key can
// never be presented as a value ciphertext or the other way round.
// These persisted format labels predate Lerryn and must stay byte-for-byte
// stable so existing credential values and wrapped keys remain readable.
const (
	valueContext = "waleng/secrets/value/v1\x00"
	dekContext   = "waleng/secrets/dek/v1\x00"
)

// Seal encrypts plaintext under a fresh data key wrapped by kr, binding
// aad to both layers.
func Seal(kr Keyring, plaintext, aad []byte) (Sealed, error) {
	dek := make([]byte, KeySize)
	if _, err := rand.Read(dek); err != nil {
		return Sealed{}, fmt.Errorf("secrets: generate data key: %w", err)
	}
	defer clear(dek)
	ct, err := gcmSeal(dek, plaintext, withContext(valueContext, aad))
	if err != nil {
		return Sealed{}, err
	}
	wrapped, kekID, err := kr.Wrap(dek, withContext(dekContext, aad))
	if err != nil {
		return Sealed{}, err
	}
	return Sealed{Ciphertext: ct, WrappedDEK: wrapped, KEKID: kekID}, nil
}

// Open decrypts s. It fails with ErrDecrypt when anything in s or aad
// differs from what Seal was given.
func Open(kr Keyring, s Sealed, aad []byte) ([]byte, error) {
	dek, err := kr.Unwrap(s.WrappedDEK, s.KEKID, withContext(dekContext, aad))
	if err != nil {
		return nil, err
	}
	defer clear(dek)
	if len(dek) != KeySize {
		return nil, ErrDecrypt
	}
	return gcmOpen(dek, s.Ciphertext, withContext(valueContext, aad))
}

// Rewrap re-encrypts the data key of s under kr's current master key,
// leaving the ciphertext unchanged. It is how a master key rotation
// reaches existing records.
func Rewrap(kr Keyring, s Sealed, aad []byte) (Sealed, error) {
	ad := withContext(dekContext, aad)
	dek, err := kr.Unwrap(s.WrappedDEK, s.KEKID, ad)
	if err != nil {
		return Sealed{}, err
	}
	defer clear(dek)
	wrapped, kekID, err := kr.Wrap(dek, ad)
	if err != nil {
		return Sealed{}, err
	}
	return Sealed{Ciphertext: s.Ciphertext, WrappedDEK: wrapped, KEKID: kekID}, nil
}

func withContext(ctx string, aad []byte) []byte {
	out := make([]byte, 0, len(ctx)+len(aad))
	return append(append(out, ctx...), aad...)
}

// gcmSeal returns nonce || AES-256-GCM(key, nonce, plaintext, aad). Nonces
// are random: every key encrypts exactly one value (data keys), or few
// enough values that random 96-bit nonces never collide in practice
// (master keys wrap one data key per credential version).
func gcmSeal(key, plaintext, aad []byte) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secrets: generate nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

func gcmOpen(key, sealed, aad []byte) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrDecrypt
	}
	nonce, ct := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	out, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return out, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secrets: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	return cipher.NewGCM(block)
}
