package main

import "testing"

func TestSecureServerURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://lerryn.example.com", true},
		{"HTTPS://lerryn.example.com", true},
		{"http://lerryn.example.com", false},
		{"HTTP://lerryn.example.com", false},
		{"Http://caddy:80", false},
		{"ws://lerryn.example.com", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := secureServerURL(tt.url); got != tt.want {
			t.Errorf("secureServerURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}
