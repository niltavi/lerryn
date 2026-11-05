package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
)

// Token prefixes. They make tokens recognisable in logs and secret scanners
// and tell the server which kind of credential it is looking at. These
// established credential formats stay stable across product renames.
const (
	AccessTokenPrefix  = "wl_pat_" // #nosec G101 -- a prefix, not a credential
	SessionTokenPrefix = "wl_ses_" // #nosec G101 -- a prefix, not a credential
	// TriggerTokenPrefix marks webhook trigger tokens, which only call
	// POST /hooks/{triggerId} and stand for no user.
	TriggerTokenPrefix = "wl_hook_" // #nosec G101 -- a prefix, not a credential
)

// tokenBytes is the entropy of a token: 256 bits.
const tokenBytes = 32

// displayLen is how many characters after the prefix DisplayPrefix keeps.
const displayLen = 4

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewToken returns a random token that starts with prefix, and the hash to
// store for it. The token itself is never stored.
func NewToken(prefix string) (token, hash string, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	token = prefix + strings.ToLower(tokenEncoding.EncodeToString(b))
	return token, HashToken(token), nil
}

// HashToken returns the hex SHA-256 of token. Tokens carry 256 bits of
// entropy, so a fast hash is enough; there is nothing to brute-force.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// DisplayPrefix returns the start of token that is safe to show, such as
// wl_pat_k7q2, so users can tell their tokens apart.
func DisplayPrefix(token string) string {
	for _, p := range []string{AccessTokenPrefix, SessionTokenPrefix, TriggerTokenPrefix} {
		if strings.HasPrefix(token, p) && len(token) >= len(p)+displayLen {
			return token[:len(p)+displayLen]
		}
	}
	return ""
}
