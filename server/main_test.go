package main

import "testing"

func TestResolveHost(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "0.0.0.0"},
		{"0.0.0.0", "0.0.0.0"},
		{"127.0.0.1", "127.0.0.1"},
		{"::1", "::1"},
		{" 10.0.0.5 ", "10.0.0.5"},
		// Zeabur 内部服务名：不能拿去 net.Listen，必须回退
		{"service-6acaffd2faa6e043a4b63c55", "0.0.0.0"},
		{"localhost", "0.0.0.0"},
		{"example.com", "0.0.0.0"},
	}
	for _, c := range cases {
		if got := resolveHost(c.in); got != c.want {
			t.Errorf("resolveHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
