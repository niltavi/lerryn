package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCStateTTL is how long a login started with OIDC.Begin can be finished.
const OIDCStateTTL = 10 * time.Minute

// DefaultUsernameClaim is the ID token claim used as the username.
const DefaultUsernameClaim = "preferred_username"

// oidcHTTPTimeout bounds every request to the provider: discovery, keys and
// the token exchange.
const oidcHTTPTimeout = 10 * time.Second

// Errors from finishing an OIDC login. Both mean the login is refused; they
// differ only in what the server logs.
var (
	// ErrOIDCState means the state cookie is missing, tampered with or
	// expired, or the state in the callback does not match it. This is
	// what a forged or replayed callback looks like.
	ErrOIDCState = errors.New("oidc: state does not match the login in progress")
	// ErrOIDCToken means the provider did not hand out a valid ID token
	// for this login: the code exchange failed, the signature, issuer,
	// audience or expiry is wrong, or the nonce does not match.
	ErrOIDCToken = errors.New("oidc: no valid id token")
)

// OIDCConfig configures the single OIDC provider (tech design 12.1).
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is <server.public_url>/api/v1/auth/oidc/callback.
	RedirectURL string
	// UsernameClaim names the claim used as the username; empty means
	// DefaultUsernameClaim.
	UsernameClaim string
	// HTTPClient talks to the provider; nil uses a client with a timeout.
	HTTPClient *http.Client
}

// OIDC runs the authorization code flow with PKCE against one provider.
// It keeps no server-side state: what the callback needs (state, nonce and
// the PKCE verifier) travels in a cookie signed with a key derived from the
// client secret, so any server replica can finish a login another started.
type OIDC struct {
	cfg      OIDCConfig
	client   *http.Client
	stateKey []byte

	mu       sync.Mutex
	provider *oidc.Provider
}

// NewOIDC returns the OIDC client. It does not contact the provider: the
// discovery document is fetched on first use, so a provider that is down
// at startup does not stop the server.
func NewOIDC(cfg OIDCConfig) (*OIDC, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, errors.New("oidc: issuer, client ID and redirect URL are required")
	}
	if cfg.UsernameClaim == "" {
		cfg.UsernameClaim = DefaultUsernameClaim
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: oidcHTTPTimeout}
	}
	var key []byte
	if cfg.ClientSecret != "" {
		// Every replica shares the client secret, so every replica derives
		// the same key.
		mac := hmac.New(sha256.New, []byte(cfg.ClientSecret))
		mac.Write([]byte("lerryn oidc state v1"))
		key = mac.Sum(nil)
	} else {
		// A public client has no shared secret; a random key still works
		// for a single replica.
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("oidc: generate state key: %w", err)
		}
	}
	return &OIDC{cfg: cfg, client: client, stateKey: key}, nil
}

// Issuer returns the configured issuer.
func (o *OIDC) Issuer() string { return o.cfg.Issuer }

// ClientID returns the configured client ID.
func (o *OIDC) ClientID() string { return o.cfg.ClientID }

// UsernameClaim returns the claim used as the username.
func (o *OIDC) UsernameClaim() string { return o.cfg.UsernameClaim }

// RedirectURL returns the callback address registered with the provider.
func (o *OIDC) RedirectURL() string { return o.cfg.RedirectURL }

// OIDCClaims are the facts about a user taken from a verified ID token.
type OIDCClaims struct {
	Issuer  string
	Subject string
	// Username is the configured username claim, possibly empty.
	Username string
	Email    string
	Name     string
}

// oidcState is the content of the state cookie.
type oidcState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r"`
	Expires  int64  `json:"e"`
}

// Begin starts a login. It returns the provider's authorization URL to
// redirect to and the value of the state cookie, which the callback must
// present to Finish.
func (o *OIDC) Begin(ctx context.Context, returnTo string) (authURL, cookie string, err error) {
	cfg, _, err := o.oauth(ctx)
	if err != nil {
		return "", "", err
	}
	st := oidcState{
		State:    rand.Text(),
		Nonce:    rand.Text(),
		Verifier: oauth2.GenerateVerifier(),
		ReturnTo: returnTo,
		Expires:  time.Now().Add(OIDCStateTTL).Unix(),
	}
	cookie, err = o.seal(st)
	if err != nil {
		return "", "", err
	}
	authURL = cfg.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier))
	return authURL, cookie, nil
}

// Finish completes a login from the callback's state and code and the
// state cookie. It returns the verified claims and the console path to go
// back to.
func (o *OIDC) Finish(ctx context.Context, cookie, state, code string) (OIDCClaims, string, error) {
	st, err := o.open(cookie)
	if err != nil {
		return OIDCClaims{}, "", err
	}
	if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(st.State)) != 1 {
		return OIDCClaims{}, "", ErrOIDCState
	}
	if code == "" {
		return OIDCClaims{}, "", fmt.Errorf("%w: no authorization code", ErrOIDCToken)
	}
	cfg, verifier, err := o.oauth(ctx)
	if err != nil {
		return OIDCClaims{}, "", err
	}
	ctx = oidc.ClientContext(ctx, o.client)
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return OIDCClaims{}, "", fmt.Errorf("%w: exchange code: %w", ErrOIDCToken, err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return OIDCClaims{}, "", fmt.Errorf("%w: token response has no id_token", ErrOIDCToken)
	}
	idToken, err := verifier.Verify(ctx, raw)
	if err != nil {
		return OIDCClaims{}, "", fmt.Errorf("%w: %w", ErrOIDCToken, err)
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(st.Nonce)) != 1 {
		return OIDCClaims{}, "", fmt.Errorf("%w: nonce does not match", ErrOIDCToken)
	}
	var all map[string]any
	if err := idToken.Claims(&all); err != nil {
		return OIDCClaims{}, "", fmt.Errorf("%w: decode claims: %w", ErrOIDCToken, err)
	}
	str := func(name string) string {
		v, _ := all[name].(string)
		return strings.TrimSpace(v)
	}
	return OIDCClaims{
		Issuer:   idToken.Issuer,
		Subject:  idToken.Subject,
		Username: str(o.cfg.UsernameClaim),
		Email:    str("email"),
		Name:     str("name"),
	}, st.ReturnTo, nil
}

// oauth returns the OAuth 2 configuration and the ID token verifier,
// fetching the provider's discovery document on first use.
func (o *OIDC) oauth(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider == nil {
		// The provider keeps the context to fetch signing keys later, so it
		// must outlive this request.
		pctx := oidc.ClientContext(context.WithoutCancel(ctx), o.client)
		p, err := oidc.NewProvider(pctx, o.cfg.Issuer)
		if err != nil {
			return nil, nil, fmt.Errorf("oidc: discover provider %s: %w", o.cfg.Issuer, err)
		}
		o.provider = p
	}
	cfg := &oauth2.Config{
		ClientID:     o.cfg.ClientID,
		ClientSecret: o.cfg.ClientSecret,
		Endpoint:     o.provider.Endpoint(),
		RedirectURL:  o.cfg.RedirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	return cfg, o.provider.Verifier(&oidc.Config{ClientID: o.cfg.ClientID}), nil
}

var stateEncoding = base64.RawURLEncoding

// seal encodes st as payload.signature.
func (o *OIDC) seal(st oidcState) (string, error) {
	payload, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	p := stateEncoding.EncodeToString(payload)
	return p + "." + stateEncoding.EncodeToString(o.sign(p)), nil
}

// open checks the signature and expiry of a state cookie.
func (o *OIDC) open(cookie string) (oidcState, error) {
	p, sig, ok := strings.Cut(cookie, ".")
	if !ok {
		return oidcState{}, ErrOIDCState
	}
	got, err := stateEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, o.sign(p)) {
		return oidcState{}, ErrOIDCState
	}
	payload, err := stateEncoding.DecodeString(p)
	if err != nil {
		return oidcState{}, ErrOIDCState
	}
	var st oidcState
	if err := json.Unmarshal(payload, &st); err != nil || st.State == "" || st.Nonce == "" || st.Verifier == "" {
		return oidcState{}, ErrOIDCState
	}
	if time.Now().Unix() >= st.Expires {
		return oidcState{}, ErrOIDCState
	}
	return st, nil
}

func (o *OIDC) sign(payload string) []byte {
	mac := hmac.New(sha256.New, o.stateKey)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}
