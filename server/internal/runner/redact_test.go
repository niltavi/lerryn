package runner

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

const secretValue = "sk-live-9f8e7d6c5b4a" // #nosec G101 -- a test value

func TestRedactorForms(t *testing.T) {
	multi := "-----BEGIN KEY-----\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX\n-----END KEY-----"
	unicode := "密钥-ü-🔑-value"
	r := newRedactor([]string{secretValue, "p@ss w/rd&x=1", multi, unicode, "abc"})
	basic := base64.StdEncoding.EncodeToString([]byte("bot:" + secretValue))
	jsonGo, _ := json.Marshal(map[string]string{"k": "p@ss w/rd&x=1"})
	tests := []struct {
		name, in string
		leak     string
	}{
		{"raw", "token=" + secretValue + " ok", secretValue},
		{"twice", secretValue + secretValue, secretValue},
		{"base64", "b64 " + base64.StdEncoding.EncodeToString([]byte(secretValue)), base64.StdEncoding.EncodeToString([]byte(secretValue))},
		{"base64url", base64.URLEncoding.EncodeToString([]byte(secretValue)), base64.URLEncoding.EncodeToString([]byte(secretValue))},
		{"basic auth", "Authorization: Basic " + basic, basic[8:20]},
		{"query escaped", "?q=" + url.QueryEscape("p@ss w/rd&x=1"), url.QueryEscape("p@ss w/rd&x=1")},
		{"path escaped", "/" + url.PathEscape("p@ss w/rd&x=1"), url.PathEscape("p@ss w/rd&x=1")},
		{"python quote", "url p%40ss%20w/rd%26x%3D1", "p%40ss%20w/rd"},
		{"js encodeURIComponent lower hex", "q=p%40ss%20w%2frd%26x%3d1", "p%40ss%20w%2frd"},
		{"json", string(jsonGo), "w/rd"},
		{"json unicode escaped", `{"k":"密钥-ü-🔑-value"}`, `密钥`},
		{"raw unicode", "v=" + unicode, unicode},
		{"line of a multi-line value", "MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX", "MIIBOgIBAAJB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.redact(tt.in)
			if strings.Contains(got, tt.leak) || !strings.Contains(got, redactedMark) {
				t.Fatalf("redact(%q) = %q", tt.in, got)
			}
		})
	}
	// Values shorter than four bytes are left alone: they would match
	// ordinary text.
	if got := r.redact("abc def"); got != "abc def" {
		t.Fatalf("short value redacted: %q", got)
	}
	if newRedactor([]string{"", "ab"}) != nil {
		t.Fatal("redactor for values that are all too short")
	}
	var none *redactor
	if none.redact("x") != "x" || none.redactCut("x") != "x" {
		t.Fatal("nil redactor changes text")
	}
}

// A value one pattern contains inside another is replaced as a whole.
func TestRedactorLongestMatch(t *testing.T) {
	r := newRedactor([]string{"token-abc", "token-abcdef"})
	if got := r.redact("x token-abcdef y"); got != "x "+redactedMark+" y" {
		t.Fatalf("got %q", got)
	}
}

func TestRedactCutMasksPartialValue(t *testing.T) {
	r := newRedactor([]string{secretValue})
	for n := 1; n < len(secretValue); n++ {
		line := "prefix " + secretValue[:n]
		got := r.redactCut(line)
		if strings.Contains(got, secretValue[:n]) && n > 1 {
			t.Fatalf("cut at %d: %q", n, got)
		}
		if !strings.HasPrefix(got, "prefix ") {
			t.Fatalf("cut at %d lost the rest: %q", n, got)
		}
	}
}

func TestRedactJSON(t *testing.T) {
	r := newRedactor([]string{secretValue, "424242424242"})
	raw := json.RawMessage(fmt.Sprintf(`{"a":"x %s y","%s":[1,424242424242,{"b":"ok"}]}`, secretValue, secretValue))
	got := r.redactJSON(raw)
	if strings.Contains(string(got), secretValue) || strings.Contains(string(got), "424242424242") || !json.Valid(got) {
		t.Fatalf("redactJSON = %s", got)
	}
}

// The pipeline redacts every line, also when a value is split across two
// writes, and a value cut by the line limit.
func TestPipelineRedactsSecrets(t *testing.T) {
	limits := defaultLogLimits()
	limits.lineBytes = 64
	p, _ := testPipeline(limits)
	p.useSecrets(newRedactor([]string{secretValue}))
	out, flush := p.output()
	half := len(secretValue) / 2
	_, _ = out.Stdout.Write([]byte("key=" + secretValue[:half]))
	_, _ = out.Stdout.Write([]byte(secretValue[half:] + "\n"))
	_, _ = out.Stderr.Write([]byte("err " + secretValue + "\n"))
	_, _ = out.Stdout.Write([]byte(strings.Repeat("x", 50) + secretValue + "\n"))
	flush()
	p.system("system line %s", secretValue)
	lines, _ := drain(t, p)
	if len(lines) != 4 {
		t.Fatalf("lines = %q", texts(lines))
	}
	for _, l := range lines {
		for n := 6; n <= len(secretValue); n++ {
			for i := 0; i+n <= len(secretValue); i++ {
				if strings.Contains(l.Text, secretValue[i:i+n]) {
					t.Fatalf("line %q leaks %q", l.Text, secretValue[i:i+n])
				}
			}
		}
	}
}

func TestRedactEvent(t *testing.T) {
	p, _ := testPipeline(defaultLogLimits())
	p.useSecrets(newRedactor([]string{secretValue}))
	ev := p.redactEvent(runproto.Event{
		Kind: runproto.EventResult, Message: "m " + secretValue,
		Result: &runproto.Result{Status: "ok", Summary: "s " + secretValue, Data: json.RawMessage(`{"k":"` + secretValue + `"}`)},
	})
	b, _ := json.Marshal(ev)
	if strings.Contains(string(b), secretValue) {
		t.Fatalf("event leaks: %s", b)
	}
	if p.redactText("e "+secretValue) != "e "+redactedMark {
		t.Fatal("redactText")
	}
}

func TestBuildSecrets(t *testing.T) {
	rs, err := buildSecrets([]runproto.Secret{
		{Name: "LLM", As: "env", Fields: []runproto.SecretField{{Name: "LLM_API_KEY", Value: secretValue, Sensitive: true}}},
		{Name: "CRM", As: "file", Fields: []runproto.SecretField{{Name: "USER", Value: "bot"}, {Name: "CERT", Value: "a\nb\n", Sensitive: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "LERRYN-SECRETS 1\nenv LLM LLM_API_KEY 20\n" + secretValue + "\nfile CRM USER 3\nbot\nfile CRM CERT 4\na\nb\n\nEND\n"
	if string(rs.payload) != want {
		t.Fatalf("payload = %q\nwant %q", rs.payload, want)
	}
	if len(rs.values) != 2 {
		t.Fatalf("redaction values = %d, want only the sensitive ones", len(rs.values))
	}
	rs.wipe()
	if rs.payload != nil {
		t.Fatal("payload kept")
	}

	for name, s := range map[string][]runproto.Secret{
		"env clash": {
			{Name: "A", As: "env", Fields: []runproto.SecretField{{Name: "TOKEN", Value: "1"}}},
			{Name: "B", As: "env", Fields: []runproto.SecretField{{Name: "TOKEN", Value: "2"}}},
		},
		"bad name":  {{Name: "a b", As: "env"}},
		"bad field": {{Name: "A", As: "env", Fields: []runproto.SecretField{{Name: "x y", Value: "1"}}}},
		"bad mode":  {{Name: "A", As: "stdin"}},
		"nul":       {{Name: "A", As: "env", Fields: []runproto.SecretField{{Name: "T", Value: "a\x00b"}}}},
		"too large": {{Name: "A", As: "env", Fields: []runproto.SecretField{{Name: "T", Value: strings.Repeat("x", maxSecretValue+1)}}}},
	} {
		_, err := buildSecrets(s)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "xxxx") || strings.Contains(err.Error(), "a\x00b") {
			t.Errorf("%s: error quotes the value: %v", name, err)
		}
	}
}

func credManifest() runproto.Manifest {
	m := pyManifest()
	m.Credentials = []runproto.CredentialRef{{Name: "LLM", As: "env"}}
	return m
}

// A run with credentials gets them on stdin, and the values are redacted
// from its logs, events and error message.
func TestRunWithSecrets(t *testing.T) {
	h := newHarness(t, nil)
	var stdin string
	h.ex.behave = func(c *fakeContainer) {
		if !c.isTask() {
			c.exit(0, false)
			return
		}
		stdin = string(c.stdin)
		c.stdout("using " + secretValue + "\n")
		c.event(`{"kind":"result","status":"ok","summary":"key ` + secretValue + `","data":{"k":"` + secretValue + `"}}`)
		c.exit(3, false)
	}
	a := newAssignment(credManifest())
	h.coord.secrets[a.RunID] = []runproto.Secret{{Name: "LLM", As: "env", Fields: []runproto.SecretField{{Name: "LLM_API_KEY", Value: secretValue, Sensitive: true}}}}
	h.coord.enqueue(a, []runproto.File{{Path: "main.py", Content: []byte("print(1)")}})
	h.start()
	f := h.coord.waitFinish(t, a.RunID, testTimeout)
	if f.ErrorCode != errScriptError {
		t.Fatalf("finish = %+v", f)
	}
	if !strings.Contains(stdin, "env LLM LLM_API_KEY 20\n"+secretValue+"\n") {
		t.Fatalf("stdin = %q", stdin)
	}
	for _, l := range h.coord.logTexts(a.RunID) {
		if strings.Contains(l, secretValue) {
			t.Fatalf("log leaks: %q", l)
		}
	}
	b, _ := json.Marshal(h.coord.runEvents(a.RunID))
	if strings.Contains(string(b), secretValue) || !strings.Contains(string(b), redactedMark) {
		t.Fatalf("events = %s", b)
	}
	specs := h.ex.startedSpecs()
	if specs[len(specs)-1].Stdin != nil && strings.Contains(string(specs[len(specs)-1].Stdin), secretValue) {
		t.Fatal("payload not wiped after start")
	}
	if i, j := h.log.index("secrets "), h.log.index("start "); i < 0 || j < 0 || i > j {
		t.Fatalf("secrets fetched at %d, container started at %d", i, j)
	}
}

// A credential the run cannot have fails it before the container starts.
func TestRunWithUnavailableSecrets(t *testing.T) {
	for _, code := range []string{runproto.CodeCredentialMissing, runproto.CodeCredentialUnavailable} {
		t.Run(code, func(t *testing.T) {
			h := newHarness(t, nil)
			h.ex.behave = func(c *fakeContainer) { c.exit(0, false) }
			a := newAssignment(credManifest())
			h.coord.secretsErr[a.RunID] = &runproto.CredentialError{Code: code, Message: "凭证 LLM 已被所有者撤销"}
			h.coord.enqueue(a, []runproto.File{{Path: "main.py", Content: []byte("print(1)")}})
			h.start()
			f := h.coord.waitFinish(t, a.RunID, testTimeout)
			if f.Status != runproto.StatusFailed || f.ErrorCode != code || f.ExitCode != nil || f.ErrorMessage != "凭证 LLM 已被所有者撤销" {
				t.Fatalf("finish = %+v", f)
			}
			for _, s := range h.ex.startedSpecs() {
				if s.RunID == a.RunID.String() {
					t.Fatal("container started")
				}
			}
			if n := strings.Count(strings.Join(h.log.snapshot(), "\n"), "secrets "); n != 1 {
				t.Fatalf("secrets fetched %d times; credential errors must not be retried", n)
			}
		})
	}
}

// Runs without credentials do not call Secrets.
func TestRunWithoutCredentialsSkipsSecrets(t *testing.T) {
	h := newHarness(t, nil)
	h.ex.behave = func(c *fakeContainer) { c.exit(0, false) }
	a := newAssignment(pyManifest())
	h.coord.enqueue(a, []runproto.File{{Path: "main.py", Content: []byte("print(1)")}})
	h.start()
	h.coord.waitFinish(t, a.RunID, testTimeout)
	if h.log.index("secrets ") >= 0 {
		t.Fatal("secrets fetched for a run without credentials")
	}
}
