package main

import "testing"

func TestLocalHTTPURL(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{name: "wildcard IPv4", addr: "0.0.0.0:8787", want: "http://127.0.0.1:8787"},
		{name: "wildcard IPv6", addr: "[::]:8787", want: "http://127.0.0.1:8787"},
		{name: "loopback", addr: "127.0.0.1:8787", want: "http://127.0.0.1:8787"},
		{name: "host name", addr: "localhost:8787", want: "http://localhost:8787"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := localHTTPURL(tt.addr); got != tt.want {
				t.Fatalf("localHTTPURL(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}
