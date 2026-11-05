package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadyzURL(t *testing.T) {
	tests := []struct {
		listen, want string
		ok           bool
	}{
		{":8080", "http://127.0.0.1:8080/readyz", true},
		{"0.0.0.0:9000", "http://127.0.0.1:9000/readyz", true},
		{"[::]:8080", "http://[::1]:8080/readyz", true},
		{"10.0.0.5:8080", "http://10.0.0.5:8080/readyz", true},
		{"localhost:8080", "http://localhost:8080/readyz", true},
		{"8080", "", false},
	}
	for _, tt := range tests {
		got, err := readyzURL(tt.listen)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("readyzURL(%q) = %q, %v; want %q", tt.listen, got, err, tt.want)
		}
	}
}

func TestHealthcheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()

	if _, err := runCmd(t, "healthcheck", "--url", srv.URL+"/readyz"); err != nil {
		t.Fatalf("healthy server: %v", err)
	}
	status = http.StatusServiceUnavailable
	if _, err := runCmd(t, "healthcheck", "--url", srv.URL+"/readyz"); err == nil {
		t.Fatal("unready server passed the healthcheck")
	}
	t.Setenv("LERRYN_SERVER_LISTEN", srv.Listener.Addr().String())
	status = http.StatusOK
	if _, err := runCmd(t, "healthcheck"); err != nil {
		t.Fatalf("healthcheck from server.listen: %v", err)
	}
}
