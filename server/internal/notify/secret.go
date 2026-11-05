package notify

import (
	"crypto/rand"
	"encoding/base64"
	"net/url"
	"strings"
)

// SigningSecretPrefix starts every webhook signing secret, so a leaked one
// is easy to recognise. The whole string, prefix included, is the HMAC
// key.
const SigningSecretPrefix = "wl_whsec_" // #nosec G101 -- a prefix, not a secret

// NewSigningSecret returns a random signing secret for a generic webhook
// channel: 32 random bytes, base64url without padding.
func NewSigningSecret() string {
	return SigningSecretPrefix + base64.RawURLEncoding.EncodeToString(randomBytes(32))
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	// crypto/rand.Read never returns an error and always fills b.
	_, _ = rand.Read(b)
	return b
}

// previewTail is how many characters of the path a preview keeps.
const previewTail = 4

// Preview masks a target URL for display: scheme, host and the last few
// characters, such as https://open.feishu.cn/…/a91c. Bot tokens usually
// sit in the path or query, so everything between host and tail is cut,
// and user information is dropped.
func Preview(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "…"
	}
	base := u.Scheme + "://" + u.Host
	rest := strings.TrimPrefix(u.EscapedPath(), "/")
	if u.RawQuery != "" {
		rest += "?" + u.RawQuery
	}
	if rest == "" {
		return base
	}
	tail := []rune(rest)
	if len(tail) > previewTail {
		tail = tail[len(tail)-previewTail:]
	}
	return base + "/…/" + string(tail)
}
