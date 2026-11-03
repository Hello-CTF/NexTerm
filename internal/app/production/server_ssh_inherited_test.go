package production

import (
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestMultiUserConnectorNamesGroupForInheritedAuthRestriction(t *testing.T) {
	database := newKnownHostOwnerTestDB(t)
	keyPath := "/etc/hostname"
	for name, defaults := range map[string]store.GroupDefaults{
		"agent":   {AuthKind: "agent"},
		"keyPath": {AuthKind: "key", KeyPath: keyPath},
	} {
		// Written without an owner to bypass the write-path validation, like a legacy row.
		group, err := database.GroupCreate(t.Context(), store.GroupInput{Name: "prod-" + name, Defaults: &defaults})
		if err != nil {
			t.Fatal(err)
		}
		row := legacySSHAsset(t, database, "inherit-"+name, `{}`, nil, nil, &group.ID)
		owner := ipc.WithUserID(t.Context(), "user-a")
		err = connectLegacy(t, &productionConnector{database: database}, owner, row, database)
		requireUnsupported(t, err, name)
		if !strings.Contains(err.Error(), "「prod-"+name+"」") {
			t.Fatalf("%s: error does not name the group: %v", name, err)
		}
	}

	// A restriction on the asset itself is not attributed to the group.
	group, err := database.GroupCreate(t.Context(), store.GroupInput{Name: "prod-own", Defaults: &store.GroupDefaults{AuthKind: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	row := legacySSHAsset(t, database, "own-proxy", `{"proxyCommand":"nc %h %p"}`, nil, nil, &group.ID)
	owner := ipc.WithUserID(t.Context(), "user-a")
	err = connectLegacy(t, &productionConnector{database: database}, owner, row, database)
	requireUnsupported(t, err, "own proxyCommand")
	if strings.Contains(err.Error(), "prod-own") {
		t.Fatalf("own restriction attributed to group: %v", err)
	}
}

func TestMultiUserConnectorRejectsCaseVariantRestrictedOptions(t *testing.T) {
	database := newKnownHostOwnerTestDB(t)
	password := "password"
	owner := ipc.WithUserID(t.Context(), "user-a")
	for _, options := range []string{
		`{"ProxyCommand":"id"}`, `{"PROXYCOMMAND":"id"}`, `{"ForwardAgent":true}`, `{"KeyPath":"/etc/hostname"}`,
	} {
		row := legacySSHAsset(t, database, "case-"+options, options, &password, nil, nil)
		requireUnsupported(t, connectLegacy(t, &productionConnector{database: database}, owner, row, database), options)
	}
}
