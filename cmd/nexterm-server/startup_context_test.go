package main

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	core "github.com/Hello-CTF/NexTerm/internal/app"
)

func TestStartupContextForAuth(t *testing.T) {
	for _, test := range []struct {
		name   string
		auth   string
		listen string
		shared bool
	}{
		{name: "off", auth: core.AuthOff, listen: "0.0.0.0:8080", shared: true},
		{name: "loopback IPv4", auth: core.AuthLoopback, listen: "127.0.0.1:0", shared: true},
		{name: "loopback localhost", auth: core.AuthLoopback, listen: "localhost:8080", shared: true},
		{name: "loopback IPv6", auth: core.AuthLoopback, listen: "[::1]:8080", shared: true},
		{name: "loopback non-loopback", auth: core.AuthLoopback, listen: "0.0.0.0:8080"},
		{name: "loopback wildcard", auth: core.AuthLoopback, listen: ":8080"},
		{name: "on loopback listener", auth: core.AuthOn, listen: "127.0.0.1:0"},
		{name: "platform", auth: core.AuthPlatform, listen: "127.0.0.1:0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := startupContextForAuth(context.Background(), test.auth, test.listen)
			if owner.OpenAccess(got) != test.shared {
				t.Fatalf("Open access = %v, want %v", owner.OpenAccess(got), test.shared)
			}
		})
	}
}
