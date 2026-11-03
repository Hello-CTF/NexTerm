package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestImportBundleWarnsAboutServerRestrictedSSHOptions(t *testing.T) {
	restrictedID, cleanID := ids.New(), ids.New()
	bundle := SyncBundle{Protocol: BundleProtocolVersion, Assets: []SyncBundleAsset{
		{ID: restrictedID, Kind: "ssh", Name: "needs-proxy-command", OptionsJSON: `{"ProxyCommand":"nc %h %p"}`, CreatedAt: 1, UpdatedAt: 2},
		{ID: cleanID, Kind: "ssh", Name: "plain", OptionsJSON: `{"proxy":"socks5://p:1080"}`, CreatedAt: 1, UpdatedAt: 2},
	}}
	cases := []struct {
		name string
		ctx  context.Context
		warn bool
	}{
		{"owner", ipc.WithUserID(context.Background(), "user-a"), true},
		{"desktop", context.Background(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			instance := newTestInstance(t, true)
			report, err := instance.service.ImportBundle(tc.ctx, ImportBundleRequest{Bundle: bundle})
			if err != nil || report.AssetsCreated != 2 {
				t.Fatalf("report = %+v err = %v, want both assets imported", report, err)
			}
			var restrictedWarnings, cleanWarnings int
			for _, warning := range report.Warnings {
				if strings.Contains(warning, "needs-proxy-command") {
					restrictedWarnings++
				}
				if strings.Contains(warning, "plain") {
					cleanWarnings++
				}
			}
			if (restrictedWarnings > 0) != tc.warn || cleanWarnings != 0 {
				t.Fatalf("warnings = %v, want warn=%v for the restricted asset only", report.Warnings, tc.warn)
			}
			if tc.warn && !strings.Contains(strings.Join(report.Warnings, "\n"), "proxyCommand") {
				t.Fatalf("warning does not name the field: %v", report.Warnings)
			}
		})
	}
}

func TestApplyObjectsWarnsAboutServerRestrictedSSHOptions(t *testing.T) {
	apply := func(t *testing.T, ctx context.Context, options string) ApplyObjectResult {
		t.Helper()
		instance := newTestInstance(t, true)
		id := ids.New()
		result, err := instance.service.ApplyObjects(ctx, ApplyObjectsRequest{Objects: []ApplyObject{{
			ID: id, Kind: KindAsset, Payload: applyPayload(t, assetObject{
				ID: id, Kind: "ssh", Name: "synced-host", OptionsJSON: options, CreatedAt: 1, UpdatedAt: 5,
			}),
		}}})
		if err != nil {
			t.Fatal(err)
		}
		return result.Objects[0]
	}
	owner := ipc.WithUserID(context.Background(), "user-a")

	entry := apply(t, owner, `{"forwardAgent":true}`)
	requireApplyResult(t, entry, ApplyResultApplied)
	if !strings.Contains(entry.Warning, "synced-host") || !strings.Contains(entry.Warning, "forwardAgent") {
		t.Fatalf("owner warning = %q", entry.Warning)
	}
	entry = apply(t, owner, `{}`)
	requireApplyResult(t, entry, ApplyResultApplied)
	if entry.Warning != "" {
		t.Fatalf("clean asset warning = %q", entry.Warning)
	}
	entry = apply(t, context.Background(), `{"forwardAgent":true}`)
	requireApplyResult(t, entry, ApplyResultApplied)
	if entry.Warning != "" {
		t.Fatalf("desktop warning = %q", entry.Warning)
	}
}
