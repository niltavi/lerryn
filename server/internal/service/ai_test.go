package service_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestAIConfigurationLifecycle(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	alice, bob := f.user("alice"), f.user("bob")
	agent := f.agentOf(alice)
	cfg := service.AIConfiguration{BaseURL: "https://provider.example/v1/", Models: []string{"example/text"}}

	if _, err := creds.SetAIConfiguration(f.ctx, agent, cfg, testSecret); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("agent configured AI: %v", err)
	}
	if _, err := creds.SetAIConfiguration(f.ctx, alice, cfg, ""); err == nil {
		t.Fatal("first save accepted without an API key")
	}
	got, err := creds.SetAIConfiguration(f.ctx, alice, cfg, testSecret)
	if err != nil || got.BaseURL != "https://provider.example/v1" || !slices.Equal(got.Models, cfg.Models) {
		t.Fatalf("save = %+v, %v", got, err)
	}
	cfg.Models = []string{"example/updated"}
	if _, err := creds.SetAIConfiguration(f.ctx, alice, cfg, ""); err != nil {
		t.Fatalf("update without changing key: %v", err)
	}
	for _, raw := range []string{
		"https://another.example/v1",
		"https://127.0.0.1/v1",
		"https://user:pass@provider.example/v1",
		"https://provider.example/v1?key=" + testSecret,
		"http://provider.example/v1",
	} {
		changed := service.AIConfiguration{BaseURL: raw, Models: cfg.Models}
		key := testSecret
		if raw == "https://another.example/v1" {
			key = ""
		}
		if _, err := creds.SetAIConfiguration(f.ctx, alice, changed, key); err == nil {
			t.Fatal("unsafe or incomplete configuration was accepted")
		}
	}
	got, err = creds.AIConfiguration(f.ctx, alice)
	if err != nil || got.BaseURL != "https://provider.example/v1" || !slices.Equal(got.Models, cfg.Models) {
		t.Fatalf("configuration changed after failed save: %+v, %v", got, err)
	}
	if got, err := creds.AIConfiguration(f.ctx, bob); err != nil || got.BaseURL != "" || len(got.Models) != 0 {
		t.Fatalf("another user received configuration: %+v, %v", got, err)
	}
	if _, err := creds.AIChat(f.ctx, bob, cfg.Models[0], nil); !errors.Is(err, service.ErrAINotConfigured) {
		t.Fatalf("another user used configuration: %v", err)
	}
	var row store.Credential
	if err := f.db.NewSelect().Model(&row).Where("owner_user_id = ?", alice.UserID).Scan(f.ctx); err != nil {
		t.Fatal(err)
	}
	if items, _, err := creds.List(f.ctx, agent, "", nil, ""); err != nil || len(items) != 0 {
		t.Fatalf("AI credential surfaced in credential list: %v, %v", items, err)
	}
	if _, err := creds.Get(f.ctx, agent, row.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("AI credential is reachable by ID: %v", err)
	}
	if _, err := creds.Grant(f.ctx, alice, row.ID, store.NewID(), "API_KEY"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("AI credential grant: %v", err)
	}
	if err := creds.CheckPlaintext(f.ctx, agent, uuid.Nil, []service.ScanText{{Text: testSecret}}); !errors.As(err, new(*service.PlaintextCredentialError)) {
		t.Fatalf("task submission containing the AI key: %v", err)
	}
	if err := creds.CheckPlaintext(f.ctx, agent, uuid.Nil, []service.ScanText{{Text: got.BaseURL}, {Text: cfg.Models[0]}}); err != nil {
		t.Fatalf("task submission rejected public AI metadata: %v", err)
	}
	assertNoPlaintext(t, f)
	if err := creds.RemoveAIConfiguration(f.ctx, agent); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("agent removed configuration: %v", err)
	}
	for range 2 {
		if err := creds.RemoveAIConfiguration(f.ctx, alice); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := f.db.NewSelect().Model((*store.CredentialVersion)(nil)).Where("credential_id = ?", row.ID).Count(f.ctx); err != nil || n != 0 {
		t.Fatalf("removed key versions = %d, %v", n, err)
	}
	if _, err := creds.AIChat(f.ctx, alice, cfg.Models[0], nil); !errors.Is(err, service.ErrAINotConfigured) {
		t.Fatalf("removed configuration still usable: %v", err)
	}
}

func TestAIChatRequiresExecuteScope(t *testing.T) {
	f := newFixture(t)
	creds := f.credentials()
	user := f.user("alice")
	readOnly := *user
	readOnly.Scopes = []auth.Scope{auth.ScopeRead}
	readOnly.ReadOnly = true
	if _, err := creds.AIChat(f.ctx, &readOnly, "model", nil); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("read-only session can use AI: %v", err)
	}
}
