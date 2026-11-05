package notify

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestInternalAddresses(t *testing.T) {
	tests := []struct {
		ip       string
		internal bool
	}{
		{"127.0.0.1", true},
		{"127.8.9.10", true},
		{"::1", true},
		{"10.1.2.3", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true}, // cloud metadata
		{"fe80::1", true},
		{"fc00::1", true},
		{"fd12:3456::1", true},
		{"0.0.0.0", true},
		{"::", true},
		{"100.64.0.1", true},
		{"198.18.0.1", true},
		{"224.0.0.1", true},
		{"255.255.255.255", true},
		{"::ffff:127.0.0.1", true},
		{"::ffff:10.0.0.1", true},
		{"64:ff9b::a00:1", true},
		{"2002:a00:1::", true},
		{"::127.0.0.1", true},
		{"::10.0.0.1", true},
		{"::8.8.8.8", true},
		{"::ffff:0:127.0.0.1", true},
		{"::ffff:0:a00:1", true},
		{"2001:0:4136:e378:8000:63bf:3fff:fdd2", true},
		{"2001::1", true},
		{"2001:4860:4860::8888", false},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"172.32.0.1", false},
		{"2606:4700:4700::1111", false},
		{"::ffff:8.8.8.8", false},
	}
	for _, tt := range tests {
		if got := Internal(netip.MustParseAddr(tt.ip)); got != tt.internal {
			t.Errorf("Internal(%s) = %v, want %v", tt.ip, got, tt.internal)
		}
	}
}

func TestGuardAllowlist(t *testing.T) {
	g, err := NewGuard([]string{"10.20.0.0/16", " 127.0.0.1/32 "})
	if err != nil {
		t.Fatal(err)
	}
	for ip, want := range map[string]bool{
		"10.20.3.4":        true,
		"10.21.0.1":        false,
		"127.0.0.1":        true,
		"127.0.0.2":        false,
		"::ffff:127.0.0.1": true,
		"8.8.8.8":          true,
	} {
		if got := g.Allowed(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", ip, got, want)
		}
	}
	if _, err := NewGuard([]string{"10.0.0.0"}); err == nil {
		t.Error("NewGuard with an address instead of a network: want an error")
	}
}

func TestCheckURL(t *testing.T) {
	g, _ := NewGuard(nil)
	tests := []struct {
		url  string
		want error
	}{
		{"https://open.feishu.cn/open-apis/bot/v2/hook/x", nil},
		{"http://hooks.example.com/in", nil},
		{"https://203.0.114.1/in", nil},
		{"ftp://hooks.example.com/in", ErrInvalidURL},
		{"hooks.example.com/in", ErrInvalidURL},
		{"https:///in", ErrInvalidURL},
		{"mailto:ops@example.com", ErrInvalidURL},
		{"http://127.0.0.1:8080/hook", ErrBlockedAddress},
		{"http://[::1]/hook", ErrBlockedAddress},
		{"http://localhost/hook", ErrBlockedAddress},
		{"http://LOCALHOST./hook", ErrBlockedAddress},
		{"http://api.localhost/hook", ErrBlockedAddress},
		{"http://169.254.169.254/latest/meta-data", ErrBlockedAddress},
		{"http://10.0.0.5/hook", ErrBlockedAddress},
	}
	for _, tt := range tests {
		if err := g.CheckURL(tt.url); !errors.Is(err, tt.want) {
			t.Errorf("CheckURL(%q) = %v, want %v", tt.url, err, tt.want)
		}
	}
	allow, _ := NewGuard([]string{"127.0.0.0/8"})
	if err := allow.CheckURL("http://localhost:9000/hook"); err != nil {
		t.Errorf("allowed loopback: %v", err)
	}
}

// The guard runs on the address a connection is made to, so a hostname
// that resolves to loopback is refused although the URL looks public.
func TestSenderRefusesInternalAddresses(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++ }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	g, _ := NewGuard(nil)
	s := NewSender(g, time.Second)
	for _, target := range []string{srv.URL + "/hook", "http://localhost:" + port + "/hook"} {
		status, err := s.Send(context.Background(), KindWebhook, target, Request{Body: []byte("{}"), Header: http.Header{}})
		if !errors.Is(err, ErrBlockedAddress) || status != 0 {
			t.Errorf("send to %s: status %d, err %v; want ErrBlockedAddress", target, status, err)
		}
		if got := Describe(err); got != "目标地址指向内网或本机，部署没有允许这类地址" {
			t.Errorf("Describe = %q", got)
		}
		if strings.Contains(err.Error(), "/hook") {
			t.Errorf("error %q quotes the target URL", err)
		}
	}
	if hits != 0 {
		t.Fatalf("receiver got %d requests", hits)
	}
}

func TestSenderOutcomes(t *testing.T) {
	var got *http.Request
	var gotBody string
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, gotBody = r, string(b)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/fail", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	mux.HandleFunc("/feishu-ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"success","data":{}}`)
	})
	mux.HandleFunc("/feishu-legacy-ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"StatusCode":0,"StatusMessage":"success"}`)
	})
	mux.HandleFunc("/feishu-error", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":19024,"msg":"Key Words Not Found"}`)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	g, _ := NewGuard([]string{"127.0.0.0/8", "::1/128"})
	s := NewSender(g, 300*time.Millisecond)

	tests := []struct {
		name, kind, path string
		status           int
		describe         string
	}{
		{"delivered", KindWebhook, "/ok", 204, ""},
		{"server error", KindWebhook, "/fail", 502, "对方返回 HTTP 502"},
		{"redirect not followed", KindWebhook, "/redirect", 302, "对方返回 HTTP 302 重定向，通知不跟随重定向"},
		{"feishu success", KindFeishu, "/feishu-ok", 200, ""},
		{"feishu legacy success", KindFeishu, "/feishu-legacy-ok", 200, ""},
		{"feishu error in a 200", KindFeishu, "/feishu-error", 200, "对方返回错误：19024 Key Words Not Found"},
		{"slack ignores the body", KindSlack, "/feishu-error", 200, ""},
		{"timeout", KindWebhook, "/slow", 0, "10 秒内没有响应"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			h.Set("X-Test", "1")
			status, err := s.Send(context.Background(), tt.kind, srv.URL+tt.path, Request{Body: []byte(`{"a":1}`), Header: h})
			if status != tt.status {
				t.Errorf("status = %d, want %d", status, tt.status)
			}
			if d := Describe(err); d != tt.describe {
				t.Errorf("Describe(%v) = %q, want %q", err, d, tt.describe)
			}
		})
	}
	if got == nil || got.Method != http.MethodPost || got.Header.Get("X-Test") != "1" || gotBody != `{"a":1}` {
		t.Fatalf("request = %+v, body %q", got, gotBody)
	}

	// A closed port is refused.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	_, err := s.Send(context.Background(), KindWebhook, "http://"+addr+"/", Request{Body: []byte("{}"), Header: http.Header{}})
	if d := Describe(err); d != "连接被拒绝" {
		t.Errorf("closed port: Describe(%v) = %q", err, d)
	}
}
