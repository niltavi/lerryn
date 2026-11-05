// Package oidctest runs a minimal OpenID Connect provider on a local test
// server, so the OIDC login can be tested end to end without any external
// service. It implements discovery, a JSON Web Key Set, and the token
// endpoint of the authorization code flow with PKCE (S256). The
// authorization step is a function call: Authorize plays the user who signs
// in and approves, and returns the callback URL the browser would follow.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// User is who signs in at the provider.
type User struct {
	Subject           string
	PreferredUsername string
	Email             string
	Name              string
}

// Tamper makes the provider misbehave in one way, to test that the client
// refuses the result.
type Tamper struct {
	// WrongNonce puts a different nonce into the ID token.
	WrongNonce bool
	// WrongAudience issues the ID token for another client.
	WrongAudience bool
	// WrongIssuer names another issuer in the ID token.
	WrongIssuer bool
	// Expired issues an ID token that has already expired.
	Expired bool
	// ForeignKey signs the ID token with a key that is not in the JWKS.
	ForeignKey bool
	// NoIDToken leaves the ID token out of the token response.
	NoIDToken bool
}

// Provider is a running fake provider.
type Provider struct {
	ClientID     string
	ClientSecret string

	t       testing.TB
	srv     *httptest.Server
	key     *rsa.PrivateKey
	foreign *rsa.PrivateKey

	mu     sync.Mutex
	codes  map[string]grant
	tamper Tamper
}

type grant struct {
	user            User
	clientID        string
	redirectURI     string
	challenge       string
	challengeMethod string
	nonce           string
	expires         time.Time
}

const keyID = "test-key"

// New starts a provider that knows one confidential client. It stops when
// the test ends.
func New(t testing.TB) *Provider {
	t.Helper()
	p := &Provider{
		ClientID:     "lerryn-test",
		ClientSecret: "test-client-secret",
		t:            t,
		key:          newKey(t),
		foreign:      newKey(t),
		codes:        map[string]grant{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /keys", p.keys)
	mux.HandleFunc("POST /token", p.token)
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

// Issuer is the provider's issuer URL.
func (p *Provider) Issuer() string { return p.srv.URL }

// SetTamper changes how the next ID tokens are issued.
func (p *Provider) SetTamper(t Tamper) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tamper = t
}

// Authorize plays the user u signing in at authURL, the provider's
// authorization URL the client redirected to. It checks the request the
// way a provider would and returns the callback URL with the code and
// state.
func (p *Provider) Authorize(authURL string, u User) string {
	p.t.Helper()
	parsed, err := url.Parse(authURL)
	if err != nil {
		p.t.Fatalf("parse authorization URL: %v", err)
	}
	q := parsed.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != p.ClientID {
		p.t.Fatalf("authorization request: response_type=%q client_id=%q", q.Get("response_type"), q.Get("client_id"))
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		p.t.Fatalf("authorization request without a PKCE S256 challenge: %s", authURL)
	}
	if q.Get("state") == "" || q.Get("nonce") == "" {
		p.t.Fatalf("authorization request without state or nonce: %s", authURL)
	}
	code := rand.Text()
	p.mu.Lock()
	p.codes[code] = grant{
		user:            u,
		clientID:        q.Get("client_id"),
		redirectURI:     q.Get("redirect_uri"),
		challenge:       q.Get("code_challenge"),
		challengeMethod: q.Get("code_challenge_method"),
		nonce:           q.Get("nonce"),
		expires:         time.Now().Add(time.Minute),
	}
	p.mu.Unlock()
	cb, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		p.t.Fatalf("parse redirect_uri: %v", err)
	}
	cq := cb.Query()
	cq.Set("code", code)
	cq.Set("state", q.Get("state"))
	cb.RawQuery = cq.Encode()
	return cb.String()
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p.Issuer(),
		"authorization_endpoint":                p.Issuer() + "/authorize",
		"token_endpoint":                        p.Issuer() + "/token",
		"jwks_uri":                              p.Issuer() + "/keys",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *Provider) keys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &p.key.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig"},
	}})
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, "invalid_request")
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.ClientID || secret != p.ClientSecret {
		tokenError(w, "invalid_client")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		tokenError(w, "unsupported_grant_type")
		return
	}
	p.mu.Lock()
	code := r.PostForm.Get("code")
	g, found := p.codes[code]
	delete(p.codes, code) // codes are single use
	tamper := p.tamper
	p.mu.Unlock()
	if !found || time.Now().After(g.expires) || g.clientID != id || g.redirectURI != r.PostForm.Get("redirect_uri") {
		tokenError(w, "invalid_grant")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if g.challengeMethod != "S256" || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		tokenError(w, "invalid_grant")
		return
	}

	now := time.Now()
	claims := map[string]any{
		"iss":   p.Issuer(),
		"sub":   g.user.Subject,
		"aud":   p.ClientID,
		"iat":   now.Unix(),
		"exp":   now.Add(5 * time.Minute).Unix(),
		"nonce": g.nonce,
	}
	if g.user.PreferredUsername != "" {
		claims["preferred_username"] = g.user.PreferredUsername
	}
	if g.user.Email != "" {
		claims["email"] = g.user.Email
	}
	if g.user.Name != "" {
		claims["name"] = g.user.Name
	}
	key := p.key
	switch {
	case tamper.WrongNonce:
		claims["nonce"] = "not-the-nonce"
	case tamper.WrongAudience:
		claims["aud"] = "another-client"
	case tamper.WrongIssuer:
		claims["iss"] = "https://issuer.invalid"
	case tamper.Expired:
		claims["iat"] = now.Add(-time.Hour).Unix()
		claims["exp"] = now.Add(-30 * time.Minute).Unix()
	case tamper.ForeignKey:
		key = p.foreign
	}
	resp := map[string]any{"access_token": rand.Text(), "token_type": "Bearer", "expires_in": 300}
	if !tamper.NoIDToken {
		resp["id_token"] = p.sign(key, claims)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (p *Provider) sign(key *rsa.PrivateKey, claims map[string]any) string {
	p.t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID))
	if err != nil {
		p.t.Fatalf("new signer: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		p.t.Fatalf("marshal claims: %v", err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		p.t.Fatalf("sign id token: %v", err)
	}
	s, err := jws.CompactSerialize()
	if err != nil {
		p.t.Fatalf("serialize id token: %v", err)
	}
	return s
}

func newKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

func tokenError(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
