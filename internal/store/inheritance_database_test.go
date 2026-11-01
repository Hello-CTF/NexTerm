package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type inheritanceFixture struct {
	t   *testing.T
	ctx context.Context
	db  *store.Store
}

func newInheritanceFixture(t *testing.T) *inheritanceFixture {
	return &inheritanceFixture{t: t, ctx: context.Background(), db: dbtest.NewFixture(t).OpenStore(t)}
}

func (f *inheritanceFixture) credential(ctx context.Context, name, kind string) string {
	f.t.Helper()
	id, err := f.db.CredentialPut(ctx, store.CredentialInput{Name: name, Kind: kind, Nonce: []byte("nonce"), Blob: []byte("blob"), KEKHint: "test"})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *inheritanceFixture) group(ctx context.Context, name string, parent *string, defaults *store.GroupDefaults) store.AssetGroupRow {
	f.t.Helper()
	row, err := f.db.GroupCreate(ctx, store.GroupInput{Name: name, ParentID: parent, Defaults: defaults})
	if err != nil {
		f.t.Fatal(err)
	}
	return row
}

func (f *inheritanceFixture) asset(ctx context.Context, name, kind string, groupID *string, mutate func(*store.AssetInput)) store.AssetRow {
	f.t.Helper()
	host := name + ".example"
	input := store.AssetInput{GroupID: groupID, Kind: kind, Name: name, Host: &host, OptionsJSON: "{}"}
	if mutate != nil {
		mutate(&input)
	}
	row, err := f.db.AssetCreate(ctx, input)
	if err != nil {
		f.t.Fatal(err)
	}
	return row
}

func ptr(value string) *string { return &value }

func optionString(t *testing.T, row store.AssetRow, key string) string {
	t.Helper()
	var options map[string]any
	if err := json.Unmarshal([]byte(row.OptionsJSON), &options); err != nil {
		t.Fatal(err)
	}
	value, _ := options[key].(string)
	return value
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestResolveAssetInheritanceDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	passwordCred := f.credential(ctx, "prod-password", "password")
	keyCred := f.credential(ctx, "prod-key", "private_key")
	jump := f.asset(ctx, "bastion", "ssh", nil, nil)

	root := f.group(ctx, "生产环境", nil, &store.GroupDefaults{Username: "deploy", AuthKind: "password", CredID: passwordCred, JumpAssetID: jump.ID})
	child := f.group(ctx, "web", &root.ID, nil)
	keyed := f.group(ctx, "keyed", &root.ID, &store.GroupDefaults{Username: "ops", AuthKind: "key", CredID: keyCred})

	t.Run("nearest ancestor supplies each field", func(t *testing.T) {
		row := f.asset(ctx, "web-1", "ssh", &child.ID, nil)
		resolved, err := f.db.ResolveAsset(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		if deref(resolved.Username) != "deploy" || deref(resolved.AuthKind) != "password" || deref(resolved.CredID) != passwordCred {
			t.Fatalf("resolved = %+v", resolved.AssetRow)
		}
		if optionString(t, resolved.AssetRow, "jumpAssetId") != jump.ID {
			t.Fatalf("jump not inherited: %s", resolved.OptionsJSON)
		}
		if resolved.InheritedGroup(store.InheritAuth) != "生产环境" {
			t.Fatalf("auth source = %q", resolved.InheritedGroup(store.InheritAuth))
		}
		if row.Username != nil || row.CredID != nil {
			t.Fatal("the stored row must not be modified")
		}
	})

	t.Run("nearer group wins", func(t *testing.T) {
		row := f.asset(ctx, "keyed-1", "ssh", &keyed.ID, nil)
		resolved, err := f.db.ResolveAsset(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		if deref(resolved.Username) != "ops" || deref(resolved.AuthKind) != "key" || deref(resolved.CredID) != keyCred {
			t.Fatalf("resolved = %+v", resolved.AssetRow)
		}
		// The jump route is not defined by the nearer group, so the ancestor's still applies.
		if optionString(t, resolved.AssetRow, "jumpAssetId") != jump.ID {
			t.Fatalf("jump = %s", resolved.OptionsJSON)
		}
	})

	t.Run("own values win", func(t *testing.T) {
		row := f.asset(ctx, "own-1", "ssh", &child.ID, func(input *store.AssetInput) {
			input.Username = ptr("alice")
			input.OptionsJSON = `{"jumpAssetId":"` + jump.ID + `-own"}`
		})
		resolved, err := f.db.ResolveAsset(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		if deref(resolved.Username) != "alice" {
			t.Fatalf("username = %q", deref(resolved.Username))
		}
		if optionString(t, resolved.AssetRow, "jumpAssetId") != jump.ID+"-own" {
			t.Fatalf("own jump replaced: %s", resolved.OptionsJSON)
		}
	})

	t.Run("authentication is one unit", func(t *testing.T) {
		row := f.asset(ctx, "unit-1", "ssh", &child.ID, func(input *store.AssetInput) { input.AuthKind = ptr("agent") })
		resolved, err := f.db.ResolveAsset(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		if deref(resolved.AuthKind) != "agent" || resolved.CredID != nil || resolved.KeyPath != nil {
			t.Fatalf("an asset with its own authKind inherited part of the unit: %+v", resolved.AssetRow)
		}
		// The other fields still inherit.
		if deref(resolved.Username) != "deploy" {
			t.Fatalf("username = %q", deref(resolved.Username))
		}
	})

	t.Run("no group defaults behaves as before", func(t *testing.T) {
		plain := f.group(ctx, "plain", nil, nil)
		row := f.asset(ctx, "plain-1", "ssh", &plain.ID, nil)
		resolved, err := f.db.ResolveAsset(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.AssetRow.OptionsJSON != row.OptionsJSON || resolved.Username != nil || len(resolved.Inherited) != 0 {
			t.Fatalf("resolved = %+v", resolved)
		}
	})

	t.Run("a group edit applies to the next resolution", func(t *testing.T) {
		row := f.asset(ctx, "edit-1", "ssh", &child.ID, nil)
		if _, err := f.db.GroupUpdate(ctx, root.ID, store.GroupPatch{Defaults: &store.GroupDefaults{Username: "renamed", AuthKind: "agent"}}); err != nil {
			t.Fatal(err)
		}
		resolved, err := f.db.AssetGetResolved(ctx, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if deref(resolved.Username) != "renamed" || deref(resolved.AuthKind) != "agent" || resolved.CredID != nil {
			t.Fatalf("resolved = %+v", resolved.AssetRow)
		}
		if optionString(t, resolved.AssetRow, "jumpAssetId") != "" {
			t.Fatalf("cleared jump still inherited: %s", resolved.OptionsJSON)
		}
	})
}

func TestResolveAssetKindsDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	passwordCred := f.credential(ctx, "pw", "password")
	keyCred := f.credential(ctx, "key", "private_key")
	jump := f.asset(ctx, "bastion", "ssh", nil, nil)
	pw := f.group(ctx, "pw", nil, &store.GroupDefaults{Username: "admin", AuthKind: "password", CredID: passwordCred, JumpAssetID: jump.ID})
	key := f.group(ctx, "key", nil, &store.GroupDefaults{Username: "admin", AuthKind: "key", CredID: keyCred})

	tests := []struct {
		name      string
		kind      string
		group     string
		wantUser  string
		wantCred  string
		wantJump  string
		wantVia   string
		wantAuth  string
		skipField string
	}{
		{name: "ssh password", kind: "ssh", group: pw.ID, wantUser: "admin", wantCred: passwordCred, wantJump: jump.ID, wantAuth: "password"},
		{name: "docker password", kind: "docker", group: pw.ID, wantUser: "admin", wantCred: passwordCred, wantJump: jump.ID, wantAuth: "password"},
		{name: "winrm password", kind: "winrm", group: pw.ID, wantUser: "admin", wantCred: passwordCred},
		{name: "rdp password and via", kind: "rdp", group: pw.ID, wantUser: "admin", wantCred: passwordCred, wantVia: jump.ID},
		{name: "vnc password and via", kind: "vnc", group: pw.ID, wantUser: "admin", wantCred: passwordCred, wantVia: jump.ID},
		{name: "rdp skips a private key", kind: "rdp", group: key.ID, wantUser: "admin", skipField: store.InheritAuth},
		{name: "winrm skips a private key", kind: "winrm", group: key.ID, wantUser: "admin", skipField: store.InheritAuth},
		{name: "local never inherits", kind: "local", group: pw.ID},
		{name: "device never inherits", kind: "device", group: pw.ID},
		{name: "database never inherits", kind: "mysql", group: pw.ID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := f.asset(ctx, strings.ReplaceAll(test.name, " ", "-"), test.kind, &test.group, nil)
			resolved, err := f.db.ResolveAsset(ctx, row)
			if err != nil {
				t.Fatal(err)
			}
			if deref(resolved.Username) != test.wantUser || deref(resolved.CredID) != test.wantCred || deref(resolved.AuthKind) != test.wantAuth {
				t.Fatalf("resolved user=%q cred=%q auth=%q", deref(resolved.Username), deref(resolved.CredID), deref(resolved.AuthKind))
			}
			if optionString(t, resolved.AssetRow, "jumpAssetId") != test.wantJump || optionString(t, resolved.AssetRow, "viaAssetId") != test.wantVia {
				t.Fatalf("options = %s", resolved.OptionsJSON)
			}
			if test.skipField != "" {
				found := false
				for _, item := range resolved.Inherited {
					if item.Field == test.skipField && !item.Applied && item.Reason != "" {
						found = true
					}
				}
				if !found {
					t.Fatalf("skip of %s not reported: %+v", test.skipField, resolved.Inherited)
				}
			}
		})
	}

	t.Run("an rdp asset with its own credential keeps it", func(t *testing.T) {
		own := f.credential(ctx, "own", "password")
		row := f.asset(ctx, "rdp-own", "rdp", &pw.ID, func(input *store.AssetInput) {
			input.CredID = &own
			input.AuthKind = ptr("password")
		})
		resolved, err := f.db.ResolveAsset(ctx, row)
		if err != nil || deref(resolved.CredID) != own {
			t.Fatalf("credential = %q, %v", deref(resolved.CredID), err)
		}
	})

	t.Run("an rdp asset with a stored form default authKind still inherits", func(t *testing.T) {
		row := f.asset(ctx, "rdp-form", "rdp", &pw.ID, func(input *store.AssetInput) { input.AuthKind = ptr("password") })
		resolved, err := f.db.ResolveAsset(ctx, row)
		if err != nil || deref(resolved.CredID) != passwordCred {
			t.Fatalf("credential = %q, %v", deref(resolved.CredID), err)
		}
	})
}

func TestResolveAssetSelfJumpAndMissingReferencesDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	cred := f.credential(ctx, "pw", "password")
	jump := f.asset(ctx, "bastion", "ssh", nil, nil)
	group := f.group(ctx, "g", nil, &store.GroupDefaults{Username: "u", JumpAssetID: jump.ID})
	if _, err := f.db.AssetUpdate(ctx, jump.ID, store.AssetPatch{GroupID: store.Value(group.ID)}); err != nil {
		t.Fatal(err)
	}

	t.Run("the jump asset does not jump through itself", func(t *testing.T) {
		row, err := f.db.AssetGet(ctx, jump.ID)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := f.db.ResolveAsset(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		if optionString(t, resolved.AssetRow, "jumpAssetId") != "" {
			t.Fatalf("self jump applied: %s", resolved.OptionsJSON)
		}
		skipped := false
		for _, item := range resolved.Inherited {
			if item.Field == store.InheritJump && !item.Applied && item.Reason != "" {
				skipped = true
			}
		}
		if !skipped {
			t.Fatalf("self jump skip not reported: %+v", resolved.Inherited)
		}
	})

	t.Run("a deleted jump asset is a visible error naming the group", func(t *testing.T) {
		other := f.asset(ctx, "other", "ssh", &group.ID, nil)
		target := f.asset(ctx, "gone", "ssh", nil, nil)
		g2 := f.group(ctx, "needs-jump", nil, &store.GroupDefaults{JumpAssetID: target.ID})
		member := f.asset(ctx, "member", "ssh", &g2.ID, nil)
		if err := f.db.AssetDelete(ctx, target.ID); err != nil {
			t.Fatal(err)
		}
		_, err := f.db.ResolveAsset(ctx, member)
		if err == nil || !strings.Contains(err.Error(), "needs-jump") {
			t.Fatalf("error = %v", err)
		}
		if _, err := f.db.ResolveAsset(ctx, other); err != nil {
			t.Fatalf("an unrelated group is unaffected: %v", err)
		}
	})

	t.Run("a missing credential is a visible error naming the group", func(t *testing.T) {
		g := f.group(ctx, "has-cred", nil, &store.GroupDefaults{AuthKind: "password", CredID: cred})
		member := f.asset(ctx, "cred-member", "ssh", &g.ID, nil)
		// A credential that vanished behind the store's back (the delete path clears references).
		if err := f.db.GORM().Exec("DELETE FROM credential WHERE id = ?", cred).Error; err != nil {
			t.Fatal(err)
		}
		_, err := f.db.ResolveAsset(ctx, member)
		if err == nil || !strings.Contains(err.Error(), "has-cred") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestResolveAssetOwnerSafetyDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctxA := ipc.WithUserID(context.Background(), "user-a")
	ctxB := ipc.WithUserID(context.Background(), "user-b")
	credB := f.credential(ctxB, "b-cred", "password")
	jumpB := f.asset(ctxB, "b-jump", "ssh", nil, nil)

	t.Run("saving a foreign credential or jump is refused", func(t *testing.T) {
		if _, err := f.db.GroupCreate(ctxA, store.GroupInput{Name: "x", Defaults: &store.GroupDefaults{AuthKind: "password", CredID: credB}}); err == nil {
			t.Fatal("a group accepted another owner's credential")
		}
		if _, err := f.db.GroupCreate(ctxA, store.GroupInput{Name: "y", Defaults: &store.GroupDefaults{JumpAssetID: jumpB.ID}}); err == nil {
			t.Fatal("a group accepted another owner's jump asset")
		}
	})

	t.Run("resolution refuses a reference that changed owner", func(t *testing.T) {
		credA := f.credential(ctxA, "a-cred", "password")
		group, err := f.db.GroupCreate(ctxA, store.GroupInput{Name: "a", Defaults: &store.GroupDefaults{AuthKind: "password", CredID: credA}})
		if err != nil {
			t.Fatal(err)
		}
		member := f.asset(ctxA, "a-member", "ssh", &group.ID, nil)
		// Corrupt state, as sync of a foreign reference could produce: the stored reference now names B's credential.
		if err := f.db.GORM().Exec("UPDATE asset_group SET default_cred_id = ? WHERE id = ?", credB, group.ID).Error; err != nil {
			t.Fatal(err)
		}
		resolved, err := f.db.ResolveAsset(ctxA, member)
		if err == nil {
			t.Fatalf("another owner's credential resolved: %+v", resolved.AssetRow)
		}
		var appErr *ipc.Error
		if !asIPCError(err, &appErr) || appErr.Code != ipc.CodeForbidden {
			t.Fatalf("error = %v", err)
		}
		if err := f.db.GORM().Exec("UPDATE asset_group SET default_cred_id = NULL, default_jump_asset_id = ? WHERE id = ?", jumpB.ID, group.ID).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.ResolveAsset(ctxA, member); err == nil {
			t.Fatal("another owner's jump asset resolved")
		}
	})
}

func asIPCError(err error, target **ipc.Error) bool {
	for err != nil {
		if appErr, ok := err.(*ipc.Error); ok {
			*target = appErr
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

func TestGroupChainCycleAndDepthDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	a := f.group(ctx, "a", nil, &store.GroupDefaults{Username: "u"})
	b := f.group(ctx, "b", &a.ID, nil)
	member := f.asset(ctx, "m", "ssh", &b.ID, nil)

	t.Run("a parent cycle terminates with an error", func(t *testing.T) {
		if err := f.db.GORM().Exec("UPDATE asset_group SET parent_id = ? WHERE id = ?", b.ID, a.ID).Error; err != nil {
			t.Fatal(err)
		}
		// Only values nobody supplies walk the whole chain; the username is found on a before the loop closes.
		other := f.asset(ctx, "n", "ssh", &b.ID, func(input *store.AssetInput) { input.Username = ptr("own") })
		if _, err := f.db.ResolveAsset(ctx, other); err == nil || !strings.Contains(err.Error(), "循环") {
			t.Fatalf("error = %v", err)
		}
		if _, err := f.db.ResolveAsset(ctx, member); err == nil {
			t.Fatal("cycle resolved")
		}
		if err := f.db.GORM().Exec("UPDATE asset_group SET parent_id = NULL WHERE id = ?", a.ID).Error; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("depth is bounded", func(t *testing.T) {
		parent := a.ID
		var leaf store.AssetGroupRow
		for i := 0; i < store.MaxGroupDepth+2; i++ {
			leaf = f.group(ctx, "level", &parent, nil)
			parent = leaf.ID
		}
		row := f.asset(ctx, "deep", "ssh", &leaf.ID, nil)
		if _, err := f.db.ResolveAsset(ctx, row); err == nil || !strings.Contains(err.Error(), "嵌套") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestGroupDefaultsValidationDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	keyCred := f.credential(ctx, "key", "private_key")
	pwCred := f.credential(ctx, "pw", "password")
	rdp := f.asset(ctx, "desktop", "rdp", nil, nil)
	invalid := []struct {
		name     string
		defaults store.GroupDefaults
	}{
		{"unknown auth kind", store.GroupDefaults{AuthKind: "telepathy"}},
		{"password without credential", store.GroupDefaults{AuthKind: "password"}},
		{"password with a private key", store.GroupDefaults{AuthKind: "password", CredID: keyCred}},
		{"key without material", store.GroupDefaults{AuthKind: "key"}},
		{"key with a password credential", store.GroupDefaults{AuthKind: "key", CredID: pwCred}},
		{"credential without a method", store.GroupDefaults{CredID: pwCred}},
		{"missing credential", store.GroupDefaults{AuthKind: "password", CredID: "missing"}},
		{"missing jump", store.GroupDefaults{JumpAssetID: "missing"}},
		{"jump of the wrong kind", store.GroupDefaults{JumpAssetID: rdp.ID}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := f.db.GroupCreate(ctx, store.GroupInput{Name: test.name, Defaults: &test.defaults}); err == nil {
				t.Fatal("invalid defaults were saved")
			}
		})
	}
	if _, err := f.db.GroupCreate(ctx, store.GroupInput{Name: "ok", Defaults: &store.GroupDefaults{AuthKind: "key", KeyPath: "~/.ssh/id", CredID: pwCred}}); err != nil {
		t.Fatalf("a key path with a passphrase credential: %v", err)
	}
}

func TestCredentialDeleteClearsGroupDefaultsDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	pwCred := f.credential(ctx, "pw", "password")
	passphrase := f.credential(ctx, "phrase", "passphrase")
	pwGroup := f.group(ctx, "pw", nil, &store.GroupDefaults{Username: "u", AuthKind: "password", CredID: pwCred})
	keyGroup := f.group(ctx, "key", nil, &store.GroupDefaults{AuthKind: "key", KeyPath: "~/.ssh/id", CredID: passphrase})

	usage, err := f.db.CredentialUsage(ctx, pwCred)
	if err != nil || len(usage) != 1 || usage[0].Kind != "group" || usage[0].Name != "pw" {
		t.Fatalf("usage = %+v, %v", usage, err)
	}
	if removed, err := f.db.CredentialDeleteIfUnreferenced(ctx, pwCred); err != nil || removed {
		t.Fatalf("a credential used by a group was removed by the unreferenced cleanup: %v, %v", removed, err)
	}
	before := pwGroup.UpdatedAt
	if err := f.db.CredentialDelete(ctx, pwCred); err != nil {
		t.Fatal(err)
	}
	if err := f.db.CredentialDelete(ctx, passphrase); err != nil {
		t.Fatal(err)
	}
	got, err := f.db.GroupGet(ctx, pwGroup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultCredID != nil || got.DefaultAuthKind != nil || deref(got.DefaultUsername) != "u" || got.UpdatedAt < before {
		t.Fatalf("password group after credential delete = %+v", got)
	}
	got, err = f.db.GroupGet(ctx, keyGroup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultCredID != nil || deref(got.DefaultAuthKind) != "key" || deref(got.DefaultKeyPath) != "~/.ssh/id" {
		t.Fatalf("key group keeps its key path after the passphrase credential is deleted: %+v", got)
	}
}

func TestInheritancePreviewDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	group := f.group(ctx, "生产环境", nil, &store.GroupDefaults{Username: "deploy", AuthKind: "agent"})
	fields, err := f.db.InheritancePreview(ctx, store.InheritanceDraft{Kind: "ssh", GroupID: &group.ID})
	if err != nil {
		t.Fatal(err)
	}
	applied := map[string]string{}
	for _, item := range fields {
		if item.Applied {
			applied[item.Field] = item.GroupName
		}
	}
	if applied[store.InheritUsername] != "生产环境" || applied[store.InheritAuth] != "生产环境" {
		t.Fatalf("fields = %+v", fields)
	}
	fields, err = f.db.InheritancePreview(ctx, store.InheritanceDraft{Kind: "ssh", GroupID: &group.ID, Username: "me", AuthKind: "password"})
	if err != nil || len(fields) != 0 {
		t.Fatalf("own values still previewed as inherited: %+v, %v", fields, err)
	}
	fields, err = f.db.InheritancePreview(ctx, store.InheritanceDraft{Kind: "mysql", GroupID: &group.ID})
	if err != nil || len(fields) != 1 || fields[0].Applied || fields[0].Reason == "" {
		t.Fatalf("database kind preview = %+v, %v", fields, err)
	}
}

func TestUnchangedAssetSaveKeepsRowAndRevisionDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	group := f.group(ctx, "g", nil, &store.GroupDefaults{Username: "u"})
	row := f.asset(ctx, "same", "ssh", &group.ID, nil)
	patch := store.AssetPatch{Name: &row.Name, Host: store.Value(deref(row.Host)), Note: &row.Note, Tags: &row.Tags, OptionsJSON: &row.OptionsJSON}
	got, err := f.db.AssetUpdate(ctx, row.ID, patch)
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdatedAt != row.UpdatedAt || got.Username != nil || got.AuthKind != nil {
		t.Fatalf("an unchanged save rewrote the row: before=%+v after=%+v", row, got)
	}
	changed, err := f.db.AssetUpdate(ctx, row.ID, store.AssetPatch{Username: store.Value("x")})
	if err != nil || changed.UpdatedAt <= row.UpdatedAt || deref(changed.Username) != "x" {
		t.Fatalf("a real change must still be written: %+v, %v", changed, err)
	}
}

func TestGroupUpdateDoesNotReAddClearedCredentialDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	cred := f.credential(ctx, "pw", "password")
	group := f.group(ctx, "g", nil, &store.GroupDefaults{Username: "u", AuthKind: "password", CredID: cred})
	// The editor read the group, then the credential was deleted; saving the stale defaults must not bring the
	// reference back.
	if err := f.db.CredentialDelete(ctx, cred); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.GroupUpdate(ctx, group.ID, store.GroupPatch{Defaults: &store.GroupDefaults{Username: "renamed"}}); err != nil {
		t.Fatal(err)
	}
	got, err := f.db.GroupGet(ctx, group.ID)
	if err != nil || got.DefaultCredID != nil || deref(got.DefaultUsername) != "renamed" {
		t.Fatalf("group = %+v, %v", got, err)
	}
	// Sync tombstone delete path clears the reference too.
	cred2 := f.credential(ctx, "pw2", "password")
	g2 := f.group(ctx, "g2", nil, &store.GroupDefaults{AuthKind: "password", CredID: cred2})
	if err := f.db.CredentialDeleteRow(ctx, cred2); err != nil {
		t.Fatal(err)
	}
	got, err = f.db.GroupGet(ctx, g2.ID)
	if err != nil || got.DefaultCredID != nil || got.DefaultAuthKind != nil {
		t.Fatalf("tombstone delete left the reference: %+v, %v", got, err)
	}
}

func TestBrokenGroupChainDoesNotAffectNonInheritingKindsDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	a := f.group(ctx, "a", nil, &store.GroupDefaults{Username: "u"})
	b := f.group(ctx, "b", &a.ID, nil)
	local := f.asset(ctx, "loc", "local", &b.ID, nil)
	mysql := f.asset(ctx, "db", "mysql", &b.ID, nil)
	ssh := f.asset(ctx, "ssh", "ssh", &b.ID, nil)
	if err := f.db.GORM().Exec("UPDATE asset_group SET parent_id = ? WHERE id = ?", b.ID, a.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range []store.AssetRow{local, mysql} {
		if _, err := f.db.ResolveAsset(ctx, row); err != nil {
			t.Fatalf("%s asset failed on a broken chain: %v", row.Kind, err)
		}
	}
	if _, err := f.db.ResolveAsset(ctx, ssh); err == nil {
		t.Fatal("an ssh asset resolved through a cyclic chain")
	}
}

func TestResolveAssetsListingMatchesSingleResolutionDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	g := f.group(ctx, "g", nil, &store.GroupDefaults{Username: "deploy"})
	a := f.asset(ctx, "a", "ssh", &g.ID, nil)
	b := f.asset(ctx, "b", "ssh", nil, nil)
	resolved := f.db.ResolveAssets(ctx, []store.AssetRow{a, b})
	if deref(resolved[0].Username) != "deploy" || resolved[1].Username != nil {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestPasswordlessKindsCanOptOutDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	cred := f.credential(ctx, "pw", "password")
	g := f.group(ctx, "g", nil, &store.GroupDefaults{AuthKind: "password", CredID: cred})
	for _, kind := range []string{"vnc", "rdp", "winrm"} {
		inherits := f.asset(ctx, kind+"-inherit", kind, &g.ID, nil)
		if r, err := f.db.ResolveAsset(ctx, inherits); err != nil || deref(r.CredID) != cred {
			t.Fatalf("%s inherit = %q, %v", kind, deref(r.CredID), err)
		}
		none := f.asset(ctx, kind+"-none", kind, &g.ID, func(input *store.AssetInput) { input.AuthKind = ptr(store.AuthKindNone) })
		if r, err := f.db.ResolveAsset(ctx, none); err != nil || r.CredID != nil {
			t.Fatalf("%s explicit none still inherited %q, %v", kind, deref(r.CredID), err)
		}
	}
	// A prompting rdp/vnc asset never reads the group credential, so an unusable default cannot fail it.
	prompt := f.asset(ctx, "prompt", "rdp", &g.ID, func(input *store.AssetInput) { input.OptionsJSON = `{"promptPassword":true}` })
	if err := f.db.GORM().Exec("DELETE FROM credential WHERE id = ?", cred).Error; err != nil {
		t.Fatal(err)
	}
	if r, err := f.db.ResolveAsset(ctx, prompt); err != nil || r.CredID != nil {
		t.Fatalf("prompting asset = %+v, %v", r.CredID, err)
	}
	plain := f.asset(ctx, "plain-vnc", "vnc", &g.ID, nil)
	if _, err := f.db.ResolveAsset(ctx, plain); err == nil {
		t.Fatal("an inheriting vnc asset should surface the broken default")
	}
}

func TestAssetCredentialRequiredDatabase(t *testing.T) {
	f := newInheritanceFixture(t)
	ctx := f.ctx
	cred := f.credential(ctx, "pw", "password")
	g := f.group(ctx, "g", nil, &store.GroupDefaults{AuthKind: "password", CredID: cred})
	inheriting := f.asset(ctx, "inheriting", "ssh", &g.ID, nil)
	agent := f.asset(ctx, "agent", "ssh", &g.ID, func(input *store.AssetInput) { input.AuthKind = ptr("agent") })
	plain := f.asset(ctx, "plain", "ssh", nil, nil)
	for id, want := range map[string]bool{inheriting.ID: true, agent.ID: false, plain.ID: false} {
		got, err := f.db.AssetCredentialRequired(ctx, id)
		if err != nil || got != want {
			t.Fatalf("asset %s required = %v, %v; want %v", id, got, err, want)
		}
	}
}
