package production

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/mount"
)

// The builder derives the multi-user restriction from the same shared flag for the SSH connector and the
// mount service, so a server assembly cannot end up with one restricted and the other not.
func TestProductionBuilderMultiUserRestrictionFollowsAssembly(t *testing.T) {
	build := func(t *testing.T, ctx context.Context, desktop bool) *Production {
		t.Helper()
		application, err := NewProduction(ctx, ProductionConfig{
			Config:  Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Streams: ipc.StreamFactoryFuncs{}},
			DataDir: t.TempDir(),
			Desktop: desktop,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
		return application
	}
	mountRestricted := func(p *Production) bool {
		reason := p.Services.Mount.Capability()
		return reason != nil && *reason == mount.MultiUserUnavailableReason
	}
	cases := map[string]struct {
		ctx       context.Context
		desktop   bool
		multiUser bool
	}{
		"server":      {ctx: context.Background(), multiUser: true},
		"open server": {ctx: owner.WithOpenAccess(context.Background())},
		"desktop":     {ctx: context.Background(), desktop: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := build(t, tc.ctx, tc.desktop)
			if p.Services.sshConnector == nil {
				t.Fatal("ssh connector missing")
			}
			if got := p.Services.sshConnector.multiUser; got != tc.multiUser {
				t.Fatalf("connector multiUser = %v, want %v", got, tc.multiUser)
			}
			if got := mountRestricted(p); got != tc.multiUser {
				t.Fatalf("mount restricted = %v, want %v", got, tc.multiUser)
			}
		})
	}
}
