package provider

import (
	"strings"
	"testing"
)

func TestProxyConfigurationErrorsNeverExposeCredentials(t *testing.T) {
	const (
		username = "alice"
		password = "pr0xY-secret!@%"
		host     = "proxy.internal.example"
	)
	tests := []struct {
		name  string
		proxy string
	}{
		{name: "missing scheme", proxy: "://" + username + ":" + password + "@" + host},
		{name: "malformed password escape", proxy: "http://" + username + ":%zz@" + host},
		{name: "missing host", proxy: "http://" + username + ":" + password + "@"},
		{name: "empty hostname with port", proxy: "http://" + username + ":" + password + "@:8080"},
		{name: "unsupported scheme", proxy: "ftp://" + username + ":" + password + "@" + host + ":21"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewClient(Config{Proxy: &test.proxy})
			if err == nil {
				t.Fatal("malformed proxy was accepted")
			}
			message := err.Error()
			for _, sensitive := range []string{test.proxy, username, password, host, username + ":" + password} {
				if strings.Contains(message, sensitive) {
					t.Fatalf("proxy error exposes %q: %s", sensitive, message)
				}
			}
		})
	}
}
