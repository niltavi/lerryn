package runnerhttp_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// The credential fetch is authorized by the run assignment: only the
// runner holding the run's lease gets the values, only while the run is
// preparing, and only for that run. A credential the run cannot have
// comes back as a *runproto.CredentialError, not a retryable failure.
func TestSecretsOverHTTP(t *testing.T) {
	e := newEnv(t)
	key, _ := secrets.GenerateKey()
	kr, _ := secrets.NewLocalKeyring(key)
	creds := service.NewCredentials(e.db, service.NewAuthorizer(e.db), kr, nil)
	e.coord.UseCredentials(creds)

	m := service.Manifest{
		Runtime:     service.RuntimeSpec{Language: "python", Version: "3.12"},
		Entrypoint:  []string{"python", "main.py"},
		Workflow:    []service.WorkflowStep{{Title: "Use credential", Description: "Read the credential from the mounted file."}},
		Concurrency: service.ConcurrencyAllow,
		Credentials: []service.CredentialSpec{{Name: "LLM_API_KEY", As: "file"}},
	}
	task, err := e.tasks.Apply(e.ctx, e.user, service.ApplyTaskInput{
		ProjectID: e.user.PersonalProjectID, Name: "with-secret", Manifest: m,
		Files: []service.FileInput{{Path: "main.py", Content: []byte("print('hi')\n")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := creds.CreateRequest(e.ctx, e.user, service.CreateRequestInput{
		TaskID: task.Task.Task.ID, Name: "LLM_API_KEY", Purpose: "调用模型",
		Fields: []store.CredentialField{{Name: "LLM_API_KEY", Secret: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := creds.Fill(e.ctx, e.user, req.Request.ID, map[string]string{"LLM_API_KEY": "sk-over-http"})
	if err != nil {
		t.Fatal(err)
	}

	owner := e.client(t.TempDir(), staticToken)
	other := e.client(t.TempDir(), staticToken)
	if err := owner.Register(e.ctx, runproto.Registration{ID: "owner", Name: "a", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	if err := other.Register(e.ctx, runproto.Registration{ID: "other", Name: "b", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	run, err := e.runs.Start(e.ctx, e.user, task.Task.Task.ID, service.StartRunInput{})
	if err != nil {
		t.Fatal(err)
	}
	runID := run.Run.ID
	if _, err := owner.Secrets(e.ctx, "owner", runID); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("secrets before claim: %v", err)
	}
	as, err := owner.Claim(e.ctx, "owner", 1)
	if err != nil || len(as) != 1 {
		t.Fatalf("claim = %v, %v", as, err)
	}
	if got := as[0].Manifest.Credentials; len(got) != 1 || got[0] != (runproto.CredentialRef{Name: "LLM_API_KEY", As: "file"}) {
		t.Fatalf("assignment credentials = %+v", got)
	}
	// The runner ID in the call is ignored; the credential decides.
	if _, err := other.Secrets(e.ctx, "owner", runID); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("another runner fetches the secrets: %v", err)
	}
	got, err := owner.Secrets(e.ctx, "owner", runID)
	if err != nil || len(got) != 1 || got[0].As != "file" || got[0].Fields[0] != (runproto.SecretField{Name: "LLM_API_KEY", Value: "sk-over-http", Sensitive: true}) {
		t.Fatalf("secrets = %+v, %v", got, err)
	}

	// Without a runner credential there is nothing to fetch.
	httpReq, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/runner/v1/runs/"+runID.String()+"/secrets", strings.NewReader("{}"))
	httpReq.Header.Set("Lerryn-Runner-Protocol", "1")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated secrets call = %d", resp.StatusCode)
	}

	// After revocation the same runner gets a credential error.
	if _, err := creds.Revoke(e.ctx, e.user, cred.ID); err != nil {
		t.Fatal(err)
	}
	_, err = owner.Secrets(e.ctx, "owner", runID)
	var ce *runproto.CredentialError
	if !errors.As(err, &ce) || ce.Code != runproto.CodeCredentialUnavailable || errors.Is(err, runproto.ErrRejected) {
		t.Fatalf("secrets after revoke: %v", err)
	}
}
