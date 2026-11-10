package main

import "testing"

func TestTrustForwardedForParsesBooleans(t *testing.T) {
	env := func(value string) func(string) string {
		return func(key string) string {
			if key == "NEXTERM_TRUST_FORWARDED_FOR" {
				return value
			}
			return ""
		}
	}
	for value, want := range map[string]bool{"": false, "1": true, "true": true, "TRUE": true, " 1 ": true, "0": false, "false": false} {
		got, err := trustForwardedFor(env(value))
		if err != nil || got != want {
			t.Fatalf("NEXTERM_TRUST_FORWARDED_FOR=%q = %v, %v; want %v", value, got, err, want)
		}
	}
	for _, value := range []string{"yes", "on", "2"} {
		if _, err := trustForwardedFor(env(value)); err == nil {
			t.Fatalf("NEXTERM_TRUST_FORWARDED_FOR=%q was accepted", value)
		}
	}
}
