package production

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/sshconfig"
)

func sshImportJumpPreview(t *testing.T, configPath string, existing []sshconfig.ExistingAsset) map[string]sshconfig.HostPreview {
	t.Helper()
	result, err := sshconfig.Parse(configPath, sshconfig.DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	preview := sshconfig.PreviewSSHConfig(result, existing, nil, sshconfig.Limits{})
	blockUnmappableJumps(preview)
	hosts := map[string]sshconfig.HostPreview{}
	for _, host := range preview.Hosts {
		if _, ok := hosts[host.Alias]; !ok {
			hosts[host.Alias] = host
		}
	}
	return hosts
}

func TestBlockUnmappableJumps(t *testing.T) {
	t.Parallel()
	type want struct {
		action  sshconfig.PlanAction
		warning string
	}
	cases := []struct {
		name     string
		config   string
		fixture  string
		existing []string
		want     map[string]want
	}{
		{
			name:   "two hops through a new host without a jump",
			config: "Host bastion\nHost mid\nHost app\n  ProxyJump bastion,mid\n",
			want:   map[string]want{"app": {action: sshconfig.PlanAdd}, "mid": {action: sshconfig.PlanAdd}},
		},
		{
			name:   "middle hop already jumping through its predecessor",
			config: "Host bastion\nHost mid\n  ProxyJump bastion\nHost app\n  ProxyJump bastion,mid\n",
			want:   map[string]want{"app": {action: sshconfig.PlanAdd}},
		},
		{
			name:   "three hops",
			config: "Host h1\nHost h2\nHost h3\nHost app\n  ProxyJump user@h1:2222,h2,h3\n",
			want:   map[string]want{"app": {action: sshconfig.PlanAdd}},
		},
		{
			name:     "first hop may be an existing asset",
			config:   "Host mid\nHost app\n  ProxyJump bastion,mid\n",
			existing: []string{"bastion"},
			want:     map[string]want{"app": {action: sshconfig.PlanAdd}},
		},
		{
			name:     "middle hop is an existing asset",
			config:   "Host bastion\nHost app\n  ProxyJump bastion,mid\n",
			existing: []string{"mid"},
			want:     map[string]want{"app": {action: sshconfig.PlanBlockedJump, warning: "不是本次新增的主机"}},
		},
		{
			name:   "middle hop has a different jump",
			config: "Host bastion\nHost other\nHost mid\n  ProxyJump other\nHost app\n  ProxyJump bastion,mid\n",
			want:   map[string]want{"app": {action: sshconfig.PlanBlockedJump, warning: "自己的 ProxyJump"}, "mid": {action: sshconfig.PlanAdd}},
		},
		{
			name:   "two chains need different jumps for one middle hop",
			config: "Host b1\nHost b2\nHost mid\nHost a\n  ProxyJump b1,mid\nHost c\n  ProxyJump b2,mid\n",
			want:   map[string]want{"a": {action: sshconfig.PlanAdd}, "c": {action: sshconfig.PlanBlockedJump, warning: "另一台主机"}},
		},
		{
			name:   "host jumping through a blocked host is blocked",
			config: "Host bastion\nHost other\nHost mid\n  ProxyJump other\nHost app\n  ProxyJump bastion,mid\nHost web\n  ProxyJump app\n",
			want:   map[string]want{"app": {action: sshconfig.PlanBlockedJump}, "web": {action: sshconfig.PlanBlockedJump, warning: "不可导入"}},
		},
		{
			name:   "mapped chain would loop",
			config: "Host b\nHost c\n  ProxyJump b\nHost app\n  ProxyJump c,b\n",
			want:   map[string]want{"app": {action: sshconfig.PlanBlockedJump, warning: "循环"}, "b": {action: sshconfig.PlanAdd}, "c": {action: sshconfig.PlanAdd}},
		},
		{
			name:    "proxyjump fixture: db via app,bastion loops since app jumps through bastion",
			fixture: "proxyjump",
			want: map[string]want{
				"db":       {action: sshconfig.PlanBlockedJump, warning: "循环"},
				"app":      {action: sshconfig.PlanAdd},
				"bastion":  {action: sshconfig.PlanAdd},
				"dangling": {action: sshconfig.PlanBlockedJump},
			},
		},
		{
			name:     "jumpdup fixture keeps single-hop results",
			fixture:  "jumpdup",
			existing: []string{"known"},
			want: map[string]want{
				"Dup":           {action: sshconfig.PlanBlockedJump},
				"app":           {action: sshconfig.PlanBlockedJump},
				"user-of-known": {action: sshconfig.PlanAdd},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("..", "..", "..", "testdata", "sshconfig", tc.fixture)
			if tc.fixture == "" {
				path = filepath.Join(t.TempDir(), "config")
				if err := os.WriteFile(path, []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var existing []sshconfig.ExistingAsset
			for _, name := range tc.existing {
				existing = append(existing, sshconfig.ExistingAsset{ID: name + "-id", Name: name, Host: name + ".existing.example.com", Port: 22})
			}
			hosts := sshImportJumpPreview(t, path, existing)
			for alias, w := range tc.want {
				host, ok := hosts[alias]
				if !ok {
					t.Fatalf("host %q missing from preview", alias)
				}
				if host.Action != w.action {
					t.Errorf("%s action = %q, want %q (warnings %v)", alias, host.Action, w.action, host.Warnings)
				}
				if w.warning != "" && !strings.Contains(strings.Join(host.Warnings, " "), w.warning) {
					t.Errorf("%s warnings = %v, want one containing %q", alias, host.Warnings, w.warning)
				}
			}
		})
	}
}

func TestSSHImportMultiHopJumpChain(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dispatcher, database, _, _ := sshImportTestRig(t)
	configPath := filepath.Join(t.TempDir(), "config")
	content := "Host bastion\n  HostName bastion.example.com\n\n" +
		"Host mid\n  HostName mid.example.com\n\n" +
		"Host app\n  HostName app.example.com\n  ProxyJump bastion,mid\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	app := previewHost(t, preview, "app")
	if app.Action != "add" {
		t.Fatalf("app action = %q, want add (warnings %v)", app.Action, app.Warnings)
	}
	selection := func(aliases ...string) string {
		parts := make([]string, 0, len(aliases))
		for _, alias := range aliases {
			parts = append(parts, `{"id":"`+previewHost(t, preview, alias).ID+`","action":"import"}`)
		}
		return `{"args":{"source":"ssh-config","path":` + strconvQuote(configPath) + `,"hosts":[` + strings.Join(parts, ",") + `],"keys":[]}}`
	}

	result, response := sshImportApply(t, dispatcher, selection("app", "mid", "bastion"))
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsCreated != 3 || len(result.Warnings) != 0 {
		t.Fatalf("apply result = %+v", result)
	}
	ids := map[string]string{}
	for _, alias := range []string{"app", "mid", "bastion"} {
		ids[alias] = sshImportAssetIDByName(t, database, alias)
	}
	jumpOf := func(alias string) any {
		row, err := database.AssetGet(ctx, ids[alias])
		if err != nil {
			t.Fatal(err)
		}
		return assetOptions(t, row)["jumpAssetId"]
	}
	if got := jumpOf("app"); got != ids["mid"] {
		t.Errorf("app jumpAssetId = %v, want mid %s", got, ids["mid"])
	}
	if got := jumpOf("mid"); got != ids["bastion"] {
		t.Errorf("mid jumpAssetId = %v, want bastion %s", got, ids["bastion"])
	}
	if got := jumpOf("bastion"); got != nil {
		t.Errorf("bastion jumpAssetId = %v, want none", got)
	}
}

func TestSSHImportMultiHopSkippedMiddleHopLeavesNoPartialChain(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dispatcher, database, _, _ := sshImportTestRig(t)
	configPath := filepath.Join(t.TempDir(), "config")
	content := "Host bastion\n  HostName bastion.example.com\n\n" +
		"Host mid\n  HostName mid.example.com\n\n" +
		"Host app\n  HostName app.example.com\n  ProxyJump bastion,mid\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	body := `{"args":{"source":"ssh-config","path":` + strconvQuote(configPath) + `,"hosts":[` +
		`{"id":"` + previewHost(t, preview, "app").ID + `","action":"import"},` +
		`{"id":"` + previewHost(t, preview, "bastion").ID + `","action":"import"}],"keys":[]}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	if result.AssetsCreated != 2 || !strings.Contains(strings.Join(result.Warnings, " "), `"mid"`) {
		t.Fatalf("apply result = %+v, want a warning naming the skipped middle hop", result)
	}
	row, err := database.AssetGet(ctx, sshImportAssetIDByName(t, database, "app"))
	if err != nil {
		t.Fatal(err)
	}
	if jump, ok := assetOptions(t, row)["jumpAssetId"]; ok {
		t.Errorf("app jumpAssetId = %v, want unset when the chain cannot be completed", jump)
	}
}
