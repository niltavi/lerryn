package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/niltavi/lerryn/api"
	"github.com/niltavi/lerryn/server/internal/mcp"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// bearerTransport adds an access token to every request, as an MCP client
// configured with an Authorization header does.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// newMCPServer starts a server with tools and accounts and returns its URL
// and an access token with every scope.
func newMCPServer(t *testing.T) (string, string) {
	t.Helper()
	ctx := t.Context()
	tools, err := mcp.Load(ctx, api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	accounts := service.NewAccounts(storetest.Open(t), nil)
	_, sess, err := accounts.Register(ctx, service.RegisterInput{Username: "agent-owner", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := accounts.Authenticate(ctx, sess.Token)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := accounts.CreateToken(ctx, p, service.CreateTokenInput{Name: "agent", Scopes: []string{"read", "write", "execute"}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(discardLogger()).WithAccounts(accounts).WithMCP(tools).Handler())
	t.Cleanup(srv.Close)
	return srv.URL, token
}

func TestMCPCallsTheAPIAsTheTokenOwner(t *testing.T) {
	url, token := newMCPServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "lerryn-test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:   url + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token: token}},
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "discover"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("discover returned a tool error: %+v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Principal *struct {
			Username   string `json:"username"`
			AuthMethod string `json:"auth_method"`
		} `json:"principal"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Principal == nil || got.Principal.Username != "agent-owner" || got.Principal.AuthMethod != "access_token" {
		t.Fatalf("principal = %+v; the tool call must run as the token's owner", got.Principal)
	}
}

func TestMCPRequiresAnAccessToken(t *testing.T) {
	url, _ := newMCPServer(t)
	resp, err := http.Post(url+"/mcp", "application/json", nil) // #nosec G107 -- local test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("status = %d, WWW-Authenticate = %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

func TestMCPNotMountedWithoutTools(t *testing.T) {
	if rec := serve(t, New(discardLogger()), "/mcp"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when no tools are configured", rec.Code)
	}
}
