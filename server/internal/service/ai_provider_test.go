package service

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

type aiRoundTripper func(*http.Request) (*http.Response, error)

func (f aiRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAIProviderChat(t *testing.T) {
	const key = "sk-provider-test-0123456789"
	const endpoint = "https://provider.example/custom/v1"
	messages := []AIMessage{{Role: "user", Content: "Write a short summary"}}
	for _, tt := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"completion", 200, `{"choices":[{"message":{"content":"A short summary."}}]}`, true},
		{"redirect", 307, "redirect", false},
		{"provider error", 401, key, false},
		{"plain credential", 200, `{"choices":[{"message":{"content":"` + key + `"}}]}`, false},
		{"encoded credential", 200, `{"choices":[{"message":{"content":"` + base64.StdEncoding.EncodeToString([]byte(key)) + `"}}]}`, false},
		{"empty choices", 200, `{"choices":[]}`, false},
		{"invalid response", 200, `{"choices":[{"message":{"content":null}}]}`, false},
		{"oversized body", 200, strings.Repeat("x", maxAIResponseBytes+1), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newAIProvider(&notify.Guard{})
			var calls int
			p.client.Transport = aiRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.URL.String() != endpoint+"/chat/completions" ||
					r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get("Content-Type") != "application/json" {
					t.Fatal("unexpected provider request")
				}
				defer func() { _ = r.Body.Close() }()
				var body struct {
					Model    string
					Messages []AIMessage
					Stream   *bool
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "test-model" ||
					len(body.Messages) != 1 || body.Messages[0] != messages[0] || body.Stream == nil || *body.Stream {
					t.Fatal("unexpected provider request body")
				}
				return &http.Response{
					StatusCode: tt.status,
					Header:     http.Header{"Location": []string{"https://other.example/"}},
					Body:       io.NopCloser(strings.NewReader(tt.body)),
				}, nil
			})
			content, err := p.chat(t.Context(), endpoint, key, "test-model", messages)
			if tt.ok {
				if err != nil || content != "A short summary." {
					t.Fatalf("chat = %q, %v", content, err)
				}
			} else if !errors.Is(err, ErrAIProvider) || strings.Contains(err.Error(), key) || content != "" {
				t.Fatal("unsafe or unexpected provider response")
			}
			if calls != 1 {
				t.Fatalf("provider called %d times", calls)
			}
		})
	}

	p := newAIProvider(&notify.Guard{})
	p.client.Transport = aiRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("sent a message containing the key")
		return nil, errors.New("unexpected provider call")
	})
	if _, err := p.chat(context.Background(), endpoint, key, "test-model", []AIMessage{{Role: "user", Content: key}}); err == nil {
		t.Fatal("accepted a message containing the key")
	}
}

func TestAIChatUsesSavedKey(t *testing.T) {
	ctx := t.Context()
	db := storetest.Open(t)
	master, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := secrets.NewLocalKeyring(master)
	if err != nil {
		t.Fatal(err)
	}
	accounts := NewAccounts(db, nil)
	_, session, err := accounts.Register(ctx, RegisterInput{Username: "alice", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	user, err := accounts.Authenticate(ctx, session.Token)
	if err != nil {
		t.Fatal(err)
	}
	const key = "sk-example-ai-test-key"
	const taskKey = "sk-example-task-test-key"
	var calls atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("unexpected provider request")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var body struct {
			Model    string
			Messages []AIMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "updated" || len(body.Messages) != 1 {
			t.Error("unexpected model request body")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		result := `{"choices":[{"message":{"content":"done"}}]}`
		if body.Messages[0].Content == "Return a credential" {
			result = `{"choices":[{"message":{"content":"` + taskKey + `"}}]}`
		}
		_, _ = io.WriteString(w, result)
	}))
	t.Cleanup(provider.Close)
	messages := []AIMessage{{Role: "user", Content: "Say done"}}
	publicOnly := newAIProvider(&notify.Guard{})
	if _, err := publicOnly.chat(ctx, provider.URL+"/v1", key, "updated", messages); !errors.Is(err, ErrAIProvider) || calls.Load() != 0 {
		t.Fatalf("public-only provider reached loopback: calls = %d, err = %v", calls.Load(), err)
	}

	guard, err := notify.NewGuard([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	creds := NewCredentials(db, NewAuthorizer(db), keys, nil).WithAINetworkGuard(guard)
	roots := x509.NewCertPool()
	roots.AddCert(provider.Certificate())
	creds.ai.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
	cfg := AIConfiguration{BaseURL: provider.URL + "/v1", Models: []string{"original"}}
	if _, err := creds.SetAIConfiguration(ctx, user, cfg, key); err != nil {
		t.Fatal(err)
	}
	cfg.Models = []string{"updated"}
	if _, err := creds.SetAIConfiguration(ctx, user, cfg, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		_, err := creds.createCredential(ctx, tx, user, store.CredentialRequest{
			Name: "TASK_KEY", Purpose: "Task credential",
			Fields: []store.CredentialField{{Name: "TASK_KEY", Secret: true}},
		}, map[string]string{"TASK_KEY": taskKey})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := creds.AIChat(ctx, user, "original", messages); err == nil || calls.Load() != 0 {
		t.Fatalf("previous model reached provider: calls = %d, err = %v", calls.Load(), err)
	}
	prompt := []AIMessage{{Role: "user", Content: base64.StdEncoding.EncodeToString([]byte(taskKey))}}
	if _, err := creds.AIChat(ctx, user, "updated", prompt); !errors.As(err, new(*ValidationError)) || calls.Load() != 0 {
		t.Fatalf("prompt with task credential reached provider: calls = %d, err = %v", calls.Load(), err)
	}
	if content, err := creds.AIChat(ctx, user, "updated", messages); err != nil || content != "done" || calls.Load() != 1 {
		t.Fatalf("configured model call = %q, %v, calls = %d", content, err, calls.Load())
	}
	prompt[0].Content = "Return a credential"
	if content, err := creds.AIChat(ctx, user, "updated", prompt); !errors.Is(err, ErrAIProvider) || content != "" || calls.Load() != 2 {
		t.Fatalf("provider result containing task credential: calls = %d, err = %v", calls.Load(), err)
	}

	for _, model := range []string{key, taskKey} {
		cfg.Models = []string{model}
		if _, err := creds.SetAIConfiguration(ctx, user, cfg, "sk-replacement-ai-test-key"); !errors.As(err, new(*ValidationError)) {
			t.Fatalf("saved a credential in model metadata: %v", err)
		}
	}
}
