package auth

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTrustedProxiesClientIP(t *testing.T) {
	proxies, err := ParseTrustedProxies([]string{"172.30.10.0/24", "10.1.2.3/8", "fd00::/8"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		proxies TrustedProxies
		remote  string
		xff     []string
		want    string
	}{
		{"no proxies configured ignores the header", TrustedProxies{}, "172.30.10.2:5000", []string{"198.51.100.7"}, "172.30.10.2"},
		{"untrusted peer cannot spoof", proxies, "203.0.113.9:5000", []string{"198.51.100.7"}, "203.0.113.9"},
		{"untrusted peer with a trusted-looking header", proxies, "203.0.113.9:5000", []string{"172.30.10.5"}, "203.0.113.9"},
		{"trusted peer without header", proxies, "172.30.10.2:5000", nil, "172.30.10.2"},
		{"trusted peer names the client", proxies, "172.30.10.2:5000", []string{"198.51.100.7"}, "198.51.100.7"},
		{"client-supplied entries left of the proxy's are ignored", proxies, "172.30.10.2:5000", []string{"1.1.1.1, 2.2.2.2, 198.51.100.7"}, "198.51.100.7"},
		{"chain of trusted proxies is skipped", proxies, "172.30.10.2:5000", []string{"6.6.6.6, 198.51.100.7, 10.9.9.9, 172.30.10.3"}, "198.51.100.7"},
		{"several header lines form one list", proxies, "172.30.10.2:5000", []string{"6.6.6.6", "198.51.100.7, 172.30.10.4"}, "198.51.100.7"},
		{"every hop trusted returns the left-most", proxies, "172.30.10.2:5000", []string{"10.0.0.1, 172.30.10.3"}, "10.0.0.1"},
		{"garbage from a proxy stops the walk", proxies, "172.30.10.2:5000", []string{"198.51.100.7, unknown"}, "172.30.10.2"},
		{"entry with a port", proxies, "172.30.10.2:5000", []string{"198.51.100.7:4711"}, "198.51.100.7"},
		{"ipv6 proxy and client", proxies, "[fd00::1]:5000", []string{"2001:db8::7"}, "2001:db8::7"},
		{"ipv4-mapped peer is trusted", proxies, "[::ffff:172.30.10.2]:5000", []string{"198.51.100.7"}, "198.51.100.7"},
		{"peer without port", proxies, "172.30.10.2", []string{"198.51.100.7"}, "198.51.100.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := tt.proxies.ClientIP(r); got != tt.want {
				t.Fatalf("ClientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseTrustedProxiesRejectsInvalid(t *testing.T) {
	for _, in := range []string{"10.0.0.1", "proxy", "10.0.0.0/40", ""} {
		if _, err := ParseTrustedProxies([]string{in}); err == nil {
			t.Errorf("ParseTrustedProxies(%q) accepted an invalid network", in)
		}
	}
}

func TestTrustedProxiesClientTruncatesUserAgent(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("User-Agent", strings.Repeat("a", 1000))
	c := TrustedProxies{}.Client(r)
	if len(c.UserAgent) != maxUserAgent || c.IP != "192.0.2.1" {
		t.Fatalf("client = %+v", c)
	}
}
