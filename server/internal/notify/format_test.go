package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func failedEvent() Event {
	code := "script_error"
	reason := "进程以退出码 1 结束"
	task := uuid.MustParse("01900000-0000-7000-8000-000000000002")
	return Event{
		ID:         uuid.MustParse("01900000-0000-7000-8000-000000000001"),
		Type:       EventRunFailed,
		OccurredAt: time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC),
		Project:    ProjectRef{ID: uuid.MustParse("01900000-0000-7000-8000-000000000003"), Name: "运营"},
		Task:       &TaskRef{ID: task, Name: "daily-<digest>", ProjectID: uuid.MustParse("01900000-0000-7000-8000-000000000003")},
		Run: &Run{
			ID: uuid.MustParse("01900000-0000-7000-8000-000000000004"), Status: "failed", ErrorCode: &code, Reason: &reason,
			Version: 3, Attempt: 1, Link: "https://lerryn.example.com/runs/01900000-0000-7000-8000-000000000004",
		},
		MergedCount: 3,
		Link:        "https://lerryn.example.com/runs/01900000-0000-7000-8000-000000000004",
	}
}

func TestBuildWebhookSignsTheBody(t *testing.T) {
	ev := failedEvent()
	now := time.Unix(1_800_000_000, 0)
	req, err := Build(KindWebhook, ev, "wl_whsec_test", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get(HeaderEvent); got != EventRunFailed {
		t.Errorf("%s = %q", HeaderEvent, got)
	}
	if got := req.Header.Get(HeaderDelivery); got != ev.ID.String() {
		t.Errorf("%s = %q, want the event ID", HeaderDelivery, got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	// The body is the event itself; every field of the contract is
	// present, null where unset.
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "type", "occurred_at", "project", "task", "run", "credential", "merged_count", "link"} {
		if _, ok := body[k]; !ok {
			t.Errorf("payload lacks %q: %s", k, req.Body)
		}
	}
	if body["credential"] != nil {
		t.Errorf("credential = %v, want null", body["credential"])
	}

	// The signature follows the contract: HMAC-SHA256 over "<t>.<body>".
	sig := req.Header.Get(HeaderSignature)
	if want := "t=1800000000,v1="; !strings.HasPrefix(sig, want) {
		t.Fatalf("signature %q does not start with %q", sig, want)
	}
	if want := Sign("wl_whsec_test", now, req.Body); sig != want {
		t.Fatalf("signature %q, want %q", sig, want)
	}
	if err := Verify("wl_whsec_test", sig, req.Body, now.Add(time.Minute)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := Build(KindWebhook, ev, "", now, nil); err == nil {
		t.Error("webhook without a secret: want an error")
	}
}

func TestSignatureVerification(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"id":"x"}`)
	sig := Sign("secret", now, body)
	tests := []struct {
		name, secret, header string
		body                 []byte
		at                   time.Time
		ok                   bool
	}{
		{"valid", "secret", sig, body, now, true},
		{"just inside the tolerance", "secret", sig, body, now.Add(SignatureTolerance), true},
		{"too old", "secret", sig, body, now.Add(SignatureTolerance + time.Second), false},
		{"from the future", "secret", sig, body, now.Add(-SignatureTolerance - time.Second), false},
		{"other secret", "other", sig, body, now, false},
		{"changed body", "secret", sig, []byte(`{"id":"y"}`), now, false},
		{"second v1 matches", "secret", sig + ",v1=00", body, now, true},
		{"no v1", "secret", "t=1800000000", body, now, false},
		{"no time", "secret", "v1=00", body, now, false},
		{"garbage", "secret", "nonsense", body, now, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Verify(tt.secret, tt.header, tt.body, tt.at)
			if (err == nil) != tt.ok {
				t.Fatalf("Verify = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestBuildFeishu(t *testing.T) {
	req, err := Build(KindFeishu, failedEvent(), "ignored", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get(HeaderSignature) != "" || req.Header.Get(HeaderEvent) != "" {
		t.Error("bot formats carry no Lerryn headers")
	}
	var got struct {
		MsgType string `json:"msg_type"`
		Content struct {
			Post struct {
				ZhCN struct {
					Title   string                `json:"title"`
					Content [][]map[string]string `json:"content"`
				} `json:"zh_cn"`
			} `json:"post"`
		} `json:"content"`
	}
	if err := json.Unmarshal(req.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.MsgType != "post" {
		t.Errorf("msg_type = %q", got.MsgType)
	}
	post := got.Content.Post.ZhCN
	if post.Title != "任务 daily-<digest> 执行失败" {
		t.Errorf("title = %q", post.Title)
	}
	var lines []string
	for _, line := range post.Content {
		if len(line) != 1 {
			t.Fatalf("line %v: want one node", line)
		}
		lines = append(lines, line[0]["text"])
	}
	want := []string{
		"项目：运营",
		"状态：执行失败",
		"错误类别：脚本执行失败（script_error）",
		"原因：进程以退出码 1 结束",
		"版本：第 3 版，第 1 次尝试",
		"这个任务连续失败了 3 次，合并为这一条通知，内容为最近一次运行。",
		"查看运行",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	link := post.Content[len(post.Content)-1][0]
	if link["tag"] != "a" || link["href"] != failedEvent().Run.Link {
		t.Errorf("link node = %v", link)
	}
}

func TestBuildSlack(t *testing.T) {
	req, err := Build(KindSlack, failedEvent(), "", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Text   string `json:"text"`
		Blocks []struct {
			Type string `json:"type"`
			Text struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"text"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(req.Body, &got); err != nil {
		t.Fatal(err)
	}
	want := "*任务 daily-&lt;digest&gt; 执行失败*\n项目：运营\n状态：执行失败\n错误类别：脚本执行失败（script_error）\n" +
		"原因：进程以退出码 1 结束\n版本：第 3 版，第 1 次尝试\n这个任务连续失败了 3 次，合并为这一条通知，内容为最近一次运行。\n" +
		"<https://lerryn.example.com/runs/01900000-0000-7000-8000-000000000004|查看运行>"
	if got.Text != want {
		t.Errorf("text:\n%s\nwant:\n%s", got.Text, want)
	}
	if len(got.Blocks) != 1 || got.Blocks[0].Type != "section" || got.Blocks[0].Text.Type != "mrkdwn" || got.Blocks[0].Text.Text != want {
		t.Errorf("blocks = %+v", got.Blocks)
	}
}

func TestComposeOtherEvents(t *testing.T) {
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	tests := []struct {
		name  string
		ev    Event
		loc   *time.Location
		title string
		field string
		note  string
	}{
		{
			name: "timed out, single failure",
			ev: Event{Type: EventRunTimedOut, Project: ProjectRef{Name: "p"}, Task: &TaskRef{Name: "sync"},
				Run: &Run{Status: "timed_out", Version: 1, Attempt: 2}, MergedCount: 1},
			title: "任务 sync 已超时", field: "版本：第 1 版，第 2 次尝试",
		},
		{
			name: "interrupted, unknown error code kept as is",
			ev: Event{Type: EventRunInterrupted, Project: ProjectRef{Name: "p"}, Task: &TaskRef{Name: "sync"},
				Run: &Run{Status: "interrupted", ErrorCode: new("brand_new"), Version: 1, Attempt: 1}, MergedCount: 1},
			title: "任务 sync 运行中断", field: "错误类别：brand_new（brand_new）",
		},
		{
			name: "credential expiring, in the owner's time zone",
			ev: Event{Type: EventCredentialExpiring, Project: ProjectRef{Name: "p"}, Task: &TaskRef{Name: "sync"},
				Credential: &Credential{Name: "LLM_API_KEY", ExpiresAt: time.Date(2026, 11, 10, 16, 30, 0, 0, time.UTC)}, MergedCount: 1},
			loc:   sh,
			title: "凭证 LLM_API_KEY 即将到期", field: "到期时间：2026-11-11 00:30 CST",
			note: "到期后，引用它的运行会在准备阶段失败。请在到期前更新。",
		},
		{
			name:  "test",
			ev:    Event{Type: EventTest, Project: ProjectRef{Name: "p"}, MergedCount: 1, Link: "https://w/"},
			title: "Lerryn 测试通知", note: "这个渠道可以正常收到 Lerryn 的通知。",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Compose(tt.ev, tt.loc)
			if m.Title != tt.title {
				t.Errorf("title = %q, want %q", m.Title, tt.title)
			}
			if tt.field != "" {
				found := false
				for _, f := range m.Fields {
					found = found || f.Label+"："+f.Value == tt.field
				}
				if !found {
					t.Errorf("fields %v lack %q", m.Fields, tt.field)
				}
			}
			if m.Note != tt.note {
				t.Errorf("note = %q, want %q", m.Note, tt.note)
			}
		})
	}
}

func TestReason(t *testing.T) {
	long := strings.Repeat("错", 250)
	tests := []struct {
		name, in, want string
	}{
		{"plain", "进程以退出码 1 结束", "进程以退出码 1 结束"},
		{"white space folded", "  line one\n\n\tline two\r\n", "line one line two"},
		{"control characters", "a\x00b\x1bc", "a b c"},
		{"access token", "auth failed for wl_pat_abcdefgh12345678", "auth failed for [已隐藏]"},
		{"bearer token", "401 with Authorization: Bearer sk-live-0123456789abcdef", "401 with Authorization: [已隐藏]"},
		{"short wl_ word is kept", "wl_pat_ab", "wl_pat_ab"},
		{"truncated to 200 characters", long, strings.Repeat("错", 199) + "…"},
		{"exactly 200 characters", long[:200*3], long[:200*3]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Reason(tt.in); got != tt.want {
				t.Errorf("Reason(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPreview(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://open.feishu.cn/open-apis/bot/v2/hook/0f1e2d3c-a91c", "https://open.feishu.cn/…/a91c"},
		{"https://hooks.slack.com/services/T000/B000/XXXXsecretXXXX", "https://hooks.slack.com/…/XXXX"},
		{"https://user:pass@hooks.example.com:8443/in?token=abcdef", "https://hooks.example.com:8443/…/cdef"},
		{"https://hooks.example.com", "https://hooks.example.com"},
		{"https://hooks.example.com/", "https://hooks.example.com"},
		{"not a url", "…"},
	}
	for _, tt := range tests {
		if got := Preview(tt.in); got != tt.want {
			t.Errorf("Preview(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNewSigningSecret(t *testing.T) {
	a, b := NewSigningSecret(), NewSigningSecret()
	if a == b || !strings.HasPrefix(a, SigningSecretPrefix) || len(a) != len(SigningSecretPrefix)+43 {
		t.Fatalf("secrets %q and %q", a, b)
	}
}
