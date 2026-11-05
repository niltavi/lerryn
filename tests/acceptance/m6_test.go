//go:build integration

package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The M6 acceptance scenarios (implementation plan, M6 运维与通知).

// tokenTask prints its credential, which the runner redacts, and its
// length, which it does not.
const tokenTask = "import os\nkey = os.environ['API_TOKEN']\nprint('token', key, len(key), flush=True)\n" // #nosec G101 -- task code, not a credential

const a15Secret = "a15-secret-survives-restore" // #nosec G101 -- a test value

// A15: a SQLite deployment is backed up while it runs and restored on a
// new instance, with the master key brought over separately. Accounts,
// tokens, the task, its triggers, project access and the credential
// grant all keep working there; nobody fills the credential again.
func TestA15BackupAndRestore(t *testing.T) {
	t.Parallel()
	old := newServer(t)
	oldHost := &credServer{t: t, base: old.base, dir: old.dir}
	alice, bob, carol := oldHost.signUp("alice"), oldHost.signUp("bob"), oldHost.signUp("carol")

	// A team project where bob may only look.
	var project struct {
		ID string `json:"id"`
	}
	alice.api(http.MethodPost, "/api/v1/projects", map[string]any{"name": "ops"}, http.StatusCreated, &project)
	var bobMe struct {
		ID string `json:"id"`
	}
	bob.api(http.MethodGet, "/api/v1/me", nil, http.StatusOK, &bobMe)
	alice.api(http.MethodPut, "/api/v1/projects/"+project.ID+"/members/"+bobMe.ID, map[string]any{"role": "viewer"}, http.StatusOK, nil)
	alice.project = project.ID

	tk := alice.applyTask("nightly", tokenTask, nil)
	req := alice.requestCredential(tk.ID)
	alice.fill(req.URL, map[string]string{"API_TOKEN": a15Secret})
	want := fmt.Sprintf("token [REDACTED] %d", len(a15Secret))
	// The task reports no stages, so the run shows none (A04: no made-up
	// nodes).
	if r, body := alice.waitRun(alice.startRun(tk.ID).ID, true); r.Status != "completed" || !strings.Contains(body, `"stages":[]`) {
		t.Fatalf("run before the backup = %s", body)
	}
	var set triggerSet
	alice.api(http.MethodPut, "/api/v1/tasks/"+tk.ID+"/triggers", map[string]any{"triggers": []any{
		map[string]any{"name": "every-minute", "type": "cron", "schedule": "* * * * *", "timezone": "Asia/Shanghai"},
		map[string]any{"name": "push", "type": "webhook"},
	}}, http.StatusOK, &set)
	alice.api(http.MethodPost, "/api/v1/tasks/"+tk.ID+"/activation", map[string]any{"active": true}, http.StatusOK, nil)
	var cronID, hookID, hookToken string
	for _, tr := range set.Triggers {
		if tr.Webhook != nil {
			hookID, hookToken = tr.ID, *tr.Webhook.Token
		} else {
			cronID = tr.ID
		}
	}

	// Back up while the server runs.
	archive := filepath.Join(t.TempDir(), "lerryn-backup.tar.gz")
	backup := exec.Command(binary, "backup", "--output", archive) // #nosec G204 -- the binary this package built
	backup.Env = old.env
	if out, err := backup.CombinedOutput(); err != nil {
		t.Fatalf("lerryn backup: %v\n%s", err, out)
	}
	// The archive holds ciphertext only, and not the master key.
	entries, content := readArchive(t, archive)
	if bytes.Contains(content, []byte(a15Secret)) {
		t.Fatal("the backup contains the credential in plaintext")
	}
	for _, name := range entries {
		if strings.Contains(name, "master.key") {
			t.Fatalf("the backup contains the master key: %v", entries)
		}
	}
	old.stop()

	// A new instance: empty directories, another address, the master key
	// copied from where the operator keeps it.
	fresh := prepareServer(t)
	key, err := os.ReadFile(filepath.Join(old.dir, "master.key")) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fresh.dir, "master.key"), key, 0o600); err != nil { // #nosec G703 -- a temp dir this test created
		t.Fatal(err)
	}
	restore := exec.Command(binary, "restore", "--input", archive) // #nosec G204 -- the binary this package built
	restore.Env = fresh.env
	if out, err := restore.CombinedOutput(); err != nil {
		t.Fatalf("lerryn restore: %v\n%s", err, out)
	}
	restoredAt := time.Now()
	fresh.start()
	newHost := &credServer{t: t, base: fresh.base, dir: fresh.dir}
	for _, u := range []*user{alice, bob, carol} {
		u.s = newHost
	}

	// Accounts and access tokens carry over.
	if status, _ := fresh.request(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "bob", "password": "correct horse"}); status != http.StatusOK {
		t.Fatalf("bob's login on the restored instance = %d", status)
	}
	var got struct {
		State         string `json:"state"`
		ActiveVersion *int   `json:"active_version"`
	}
	alice.api(http.MethodGet, "/api/v1/tasks/"+tk.ID, nil, http.StatusOK, &got)
	if got.State != "active" || got.ActiveVersion == nil || *got.ActiveVersion != 1 {
		t.Fatalf("restored task = %+v", got)
	}
	// Project access: bob still reads but cannot change, carol still sees
	// nothing.
	bob.api(http.MethodGet, "/api/v1/tasks/"+tk.ID, nil, http.StatusOK, nil)
	body := bob.api(http.MethodPost, "/api/v1/tasks/"+tk.ID+"/runs", map[string]any{"test": true}, http.StatusForbidden, nil)
	if !strings.Contains(body, "permission_denied") {
		t.Fatalf("bob starts a run as viewer = %s", body)
	}
	carol.api(http.MethodGet, "/api/v1/tasks/"+tk.ID, nil, http.StatusNotFound, nil)

	// Triggers: the same IDs, the schedule armed, the webhook token valid.
	var restored triggerSet
	alice.api(http.MethodGet, "/api/v1/tasks/"+tk.ID+"/triggers", nil, http.StatusOK, &restored)
	if len(restored.Triggers) != 2 || !restored.TaskActive {
		t.Fatalf("restored triggers = %+v", restored)
	}
	for _, tr := range restored.Triggers {
		if tr.ID == cronID && tr.NextFireAt == nil {
			t.Fatalf("the cron trigger has no next fire: %+v", tr)
		}
	}
	fresh.token = ""
	var accepted struct {
		RunID string `json:"run_id"`
	}
	fresh.call(http.MethodPost, "/hooks/"+hookID, []byte(`{}`), http.StatusAccepted, &accepted, "Authorization", "Bearer "+hookToken)

	// The credential grant works with the restored ciphertext and the
	// separately kept key; the task and the webhook run use it without a
	// new fill.
	for _, id := range []string{alice.startRun(tk.ID).ID, accepted.RunID} {
		r, body := alice.waitRun(id, true)
		_, lines := alice.runLogs(id)
		if r.Status != "completed" || !contains(lines, want) {
			t.Fatalf("run on the restored instance = %s\nlogs: %q", body, lines)
		}
	}

	// The schedule fires on the new instance on its own.
	fresh.token = alice.token
	waitFor(t, 150*time.Second, "a scheduled run on the restored instance", func() bool {
		for _, r := range listTaskRuns(t, fresh, tk.ID) {
			if r.Trigger.Kind == "cron" && r.Status == "completed" && r.Trigger.ScheduledAt != nil && r.Trigger.ScheduledAt.After(restoredAt) {
				return true
			}
		}
		return false
	})
}

// readArchive returns the entry names of a tar.gz and all of its content.
func readArchive(t *testing.T, path string) ([]string, []byte) {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var (
		names []string
		all   bytes.Buffer
	)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names, all.Bytes()
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if _, err := io.Copy(&all, io.LimitReader(tr, 256<<20)); err != nil {
			t.Fatal(err)
		}
	}
}

// notice is a request the notification receiver got.
type notice struct {
	header http.Header
	body   []byte
	at     time.Time
}

type receiver struct {
	mu      sync.Mutex
	notices []notice
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	r.mu.Lock()
	r.notices = append(r.notices, notice{header: req.Header.Clone(), body: body, at: time.Now()})
	r.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (r *receiver) received() []notice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notice(nil), r.notices...)
}

const a16Secret = "a16-secret-not-in-notifications" // #nosec G101 -- a test value

// A16: a task fails on purpose; a local receiver gets one signed
// notification with the actual status, a short reason and a link to the
// run, and no credential value. Test runs do not notify, and further
// failures of the same task within the merge window fold into one
// waiting notification instead of flooding the channel.
func TestA16FailureNotification(t *testing.T) {
	t.Parallel()
	s := prepareServer(t)
	// The receiver listens on loopback, which notifications may only reach
	// when the deployment allows it.
	s.env = append(s.env, "LERRYN_NOTIFY_ALLOWED_NETWORKS=127.0.0.0/8")
	s.start()
	recv := &receiver{}
	hook := httptest.NewServer(recv)
	t.Cleanup(hook.Close)

	host := &credServer{t: t, base: s.base, dir: s.dir}
	alice := host.signUp("alice")
	tk := alice.applyTask("price-sync", `import os, sys
key = os.environ["API_TOKEN"]
print("calling the price API with", key, flush=True)
raise RuntimeError("the price API rejected token " + key)
`, map[string]any{"concurrency": "forbid"})
	req := alice.requestCredential(tk.ID)
	alice.fill(req.URL, map[string]string{"API_TOKEN": a16Secret})

	var created struct {
		Channel struct {
			ID         string `json:"id"`
			URLPreview string `json:"url_preview"`
		} `json:"channel"`
		SigningSecret string `json:"signing_secret"`
	}
	target := hook.URL + "/lerryn/notify-path-7f3a"
	alice.api(http.MethodPost, "/api/v1/notification-channels", map[string]any{
		"name": "运维群", "kind": "webhook", "url": target,
		"subscriptions": []any{map[string]any{"project_id": alice.project, "task_id": tk.ID, "events": []string{"run_failed"}}},
	}, http.StatusCreated, &created)
	if created.SigningSecret == "" || created.Channel.URLPreview == target || strings.Contains(created.Channel.URLPreview, "notify-path") {
		t.Fatalf("created channel = %+v", created.Channel)
	}

	start := func(test bool) string {
		var r run
		alice.api(http.MethodPost, "/api/v1/tasks/"+tk.ID+"/runs", map[string]any{"test": test}, http.StatusAccepted, &r)
		if got, body := alice.waitRun(r.ID, true); got.Status != "failed" {
			t.Fatalf("run = %s", body)
		}
		return r.ID
	}
	// A failing test run does not notify; a failing real run does.
	start(true)
	first := start(false)
	waitFor(t, 30*time.Second, "a notification", func() bool { return len(recv.received()) > 0 })

	n := recv.received()[0]
	var payload struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Task struct {
			Name string `json:"name"`
		} `json:"task"`
		Run struct {
			ID        string  `json:"id"`
			Status    string  `json:"status"`
			ErrorCode string  `json:"error_code"`
			Reason    *string `json:"reason"`
			Link      string  `json:"link"`
		} `json:"run"`
		MergedCount int    `json:"merged_count"`
		Link        string `json:"link"`
	}
	if err := json.Unmarshal(n.body, &payload); err != nil {
		t.Fatalf("payload %s: %v", n.body, err)
	}
	if payload.Type != "run_failed" || payload.Task.Name != "price-sync" || payload.Run.ID != first || payload.Run.Status != "failed" ||
		payload.Run.ErrorCode != "script_error" || payload.Run.Reason == nil || *payload.Run.Reason == "" ||
		len([]rune(*payload.Run.Reason)) > 200 || payload.Run.Link != s.base+"/runs/"+first || payload.MergedCount != 1 {
		t.Fatalf("payload = %s", n.body)
	}
	if n.header.Get("X-Lerryn-Event") != "run_failed" || n.header.Get("X-Lerryn-Delivery") != payload.ID {
		t.Fatalf("headers = %v", n.header)
	}
	if !validSignature(n.header.Get("X-Lerryn-Signature"), created.SigningSecret, n.body) {
		t.Fatalf("signature %q does not verify", n.header.Get("X-Lerryn-Signature"))
	}
	if bytes.Contains(n.body, []byte(a16Secret)) || bytes.Contains(n.body, []byte("a16-secret")) {
		t.Fatalf("the notification contains the credential: %s", n.body)
	}

	// Two more failures right away: one waiting notification for both,
	// nothing more sent now.
	start(false)
	third := start(false)
	type delivery struct {
		Status      string  `json:"status"`
		RunID       *string `json:"run_id"`
		MergedCount int     `json:"merged_count"`
		Attempts    int     `json:"attempts"`
		HTTPStatus  *int    `json:"http_status"`
	}
	var deliveries []delivery
	waitFor(t, 30*time.Second, "the merged notification", func() bool {
		var page struct {
			Items []delivery `json:"items"`
		}
		alice.api(http.MethodGet, "/api/v1/notification-channels/"+created.Channel.ID+"/deliveries", nil, http.StatusOK, &page)
		deliveries = page.Items
		return len(deliveries) == 2 && deliveries[0].MergedCount == 2
	})
	waiting, sent := deliveries[0], deliveries[1]
	if waiting.Status != "pending" || waiting.Attempts != 0 || waiting.RunID == nil || *waiting.RunID != third {
		t.Fatalf("merged delivery = %+v", waiting)
	}
	if sent.Status != "delivered" || sent.Attempts != 1 || sent.HTTPStatus == nil || *sent.HTTPStatus != http.StatusOK ||
		sent.RunID == nil || *sent.RunID != first {
		t.Fatalf("first delivery = %+v", sent)
	}
	// A dispatch round or two later still only the first notification.
	time.Sleep(12 * time.Second)
	if got := recv.received(); len(got) != 1 {
		t.Fatalf("the receiver got %d notifications, want 1", len(got))
	}
}

// validSignature checks X-Lerryn-Signature: t=<unix>,v1=<hex HMAC-SHA256
// of "<t>.<body>">, within five minutes.
func validSignature(header, secret string, body []byte) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	var unix int64
	if _, err := fmt.Sscan(ts, &unix); err != nil || time.Since(time.Unix(unix, 0)).Abs() > 5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want, err := hex.DecodeString(sig)
	return err == nil && hmac.Equal(mac.Sum(nil), want)
}
