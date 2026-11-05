package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters: OWASP's recommended minimum of 19 MiB, 2 passes and
// one thread. They are stored in every hash, so raising them later does not
// invalidate existing passwords.
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonKeyLen    = 32
	argonSaltLen   = 16

	// maxArgonMemoryKiB bounds the parameters accepted from a stored hash.
	maxArgonMemoryKiB = 1024 * 1024
)

var errMalformedHash = errors.New("malformed password hash")

// argonSlots bounds the Argon2id computations running at once. Each one
// holds argonMemoryKiB of memory for its whole run, so without a bound a
// burst of logins or registrations could exhaust the server's memory; with
// it, excess requests queue and the rest of the server keeps working.
// Twice GOMAXPROCS keeps every CPU busy while some slots wait on the
// scheduler; the minimum of 2 keeps single-CPU hosts from serialising.
var argonSlots = make(chan struct{}, max(2, 2*runtime.GOMAXPROCS(0)))

// argonKey is argon2.IDKey; tests replace it to observe concurrency.
var argonKey = argon2.IDKey

// deriveKey runs argonKey in one of the argonSlots, waiting for a free
// slot unless ctx ends first.
func deriveKey(ctx context.Context, password, salt []byte, iterations, memory uint32, threads uint8, keyLen uint32) ([]byte, error) {
	select {
	case argonSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for a password hashing slot: %w", ctx.Err())
	}
	defer func() { <-argonSlots }()
	return argonKey(password, salt, iterations, memory, threads, keyLen), nil
}

// HashPassword returns an Argon2id hash of password in the PHC string
// format, for example $argon2id$v=19$m=19456,t=2,p=1$<salt>$<key>. It
// fails when ctx ends while waiting for a hashing slot, or when the system
// has no randomness.
func HashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key, err := deriveKey(ctx, []byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches encoded, a hash produced
// by HashPassword. It returns an error for a malformed hash, and when ctx
// ends while waiting for a hashing slot.
func VerifyPassword(ctx context.Context, encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errMalformedHash
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil ||
		memory == 0 || memory > maxArgonMemoryKiB || iterations == 0 || threads == 0 {
		return false, errMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errMalformedHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errMalformedHash
	}
	got, err := deriveKey(ctx, []byte(password), salt, iterations, memory, threads, uint32(len(want))) // #nosec G115 -- key length comes from a hash this package wrote
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var dummyHash = sync.OnceValue(func() string {
	// The hash is computed once and shared by every caller, so it must not
	// depend on the cancellation of whichever request happens to come first.
	h, err := HashPassword(context.Background(), "lerryn-dummy-password")
	if err != nil {
		panic(err)
	}
	return h
})

// VerifyDummy spends the time of a password check without a real hash, so
// that a login for an unknown user takes as long as one with a wrong
// password and does not reveal which usernames exist. It uses the same
// hashing slots as a real check, so it is bounded the same way.
func VerifyDummy(ctx context.Context, password string) {
	_, _ = VerifyPassword(ctx, dummyHash(), password)
}
