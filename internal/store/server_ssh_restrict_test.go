package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestServerSSHRestrictionMatchesConnectorKeyMatching(t *testing.T) {
	db := testStore(t)
	owner := ipc.WithUserID(context.Background(), "user-a")
	host := "example.com"
	cases := map[string]string{
		"mixed case proxy":   `{"ProxyCommand":"touch x"}`,
		"upper case proxy":   `{"PROXYCOMMAND":"touch x"}`,
		"mixed case key":     `{"KeyPath":"/etc/hostname"}`,
		"mixed case forward": `{"ForwardAgent":true}`,
		"mixed case agent":   `{"AGENTSOCKET":"/tmp/a.sock"}`,
		"mixed case auth":    `{"AuthKind":"agent"}`,
		"mixed case cert":    `{"CertPath":"/etc/c.pub"}`,
		"duplicate last ok":  `{"proxyCommand":"touch x","proxycommand":""}`,
		"duplicate first ok": `{"proxyCommand":"","PROXYCOMMAND":"touch x"}`,
		"kelvin sign":        "{\"Keypath\":\"/etc/hostname\"}",
	}
	for name, options := range cases {
		_, err := db.AssetCreate(owner, AssetInput{Kind: "ssh", Name: name, Host: &host, OptionsJSON: options})
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeUnsupported {
			t.Errorf("%s: err = %v, want unsupported", name, err)
		}
		if _, err := db.AssetCreate(context.Background(), AssetInput{Kind: "ssh", Name: "d-" + name, Host: &host, OptionsJSON: options}); err != nil {
			t.Errorf("desktop %s: %v", name, err)
		}
	}
	if _, err := db.AssetCreate(owner, AssetInput{Kind: "ssh", Name: "ok", Host: &host, OptionsJSON: `{"Proxy":"socks5://p:1080","forwardAgent":false,"ProxyCommand":""}`}); err != nil {
		t.Fatalf("harmless options: %v", err)
	}
}

func TestServerSSHAssetWarning(t *testing.T) {
	owner := ipc.WithUserID(context.Background(), "user-a")
	agent := "agent"
	restricted := AssetRow{Kind: "ssh", Name: "jump-box", OptionsJSON: `{"proxyCommand":"nc %h"}`}
	if got := ServerSSHAssetWarning(owner, restricted); !strings.Contains(got, "jump-box") || !strings.Contains(got, "proxyCommand") {
		t.Fatalf("warning = %q", got)
	}
	if got := ServerSSHAssetWarning(owner, AssetRow{Kind: "ssh", Name: "a", AuthKind: &agent, OptionsJSON: "{}"}); got == "" {
		t.Fatal("authKind column agent produced no warning")
	}
	if got := ServerSSHAssetWarning(owner, AssetRow{Kind: "ssh", Name: "ok", OptionsJSON: `{"proxy":"socks5://p:1"}`}); got != "" {
		t.Fatalf("clean asset warning = %q", got)
	}
	if got := ServerSSHAssetWarning(context.Background(), restricted); got != "" {
		t.Fatalf("desktop warning = %q", got)
	}
	if got := ServerSSHAssetWarning(owner, AssetRow{Kind: "note", Name: "n", OptionsJSON: `{"proxyCommand":"x"}`}); got != "" {
		t.Fatalf("non-ssh warning = %q", got)
	}
}
