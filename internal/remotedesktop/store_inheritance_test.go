package remotedesktop

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestStoreResolverAppliesGroupDefaults(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	owner := ipc.WithUserID(ctx, "owner-a")
	if _, err := database.CredentialPut(owner, store.CredentialInput{ID: "pw", Name: "rdp-password", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CredentialPut(owner, store.CredentialInput{ID: "key", Name: "ssh-key", Kind: "private_key", Nonce: []byte{1}, Blob: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	bastion, err := database.AssetCreate(owner, store.AssetInput{Kind: "ssh", Name: "bastion", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := database.GroupCreate(owner, store.GroupInput{Name: "办公室", Defaults: &store.GroupDefaults{
		Username: "Administrator", AuthKind: "password", CredID: "pw", JumpAssetID: bastion.ID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	host := "win.internal"
	row, err := database.AssetCreate(owner, store.AssetInput{GroupID: &group.ID, Kind: KindRDP, Name: "win", Host: &host, OptionsJSON: `{"domain":"CORP"}`})
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewStoreResolver(database, stubDecryptor{"from-group-credential"})

	asset, err := resolver.ResolveAsset(owner, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	options, err := ParseOptions(asset.OptionsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if asset.Username != "Administrator" || !asset.HasPassword || options.ViaAssetID != bastion.ID || options.Domain != "CORP" {
		t.Fatalf("asset = %+v, options = %+v", asset, options)
	}
	password, err := resolver.ResolvePassword(owner, row.ID)
	if err != nil || password != "from-group-credential" {
		t.Fatalf("password = %q, %v", password, err)
	}

	// An edit of the group applies to the next call; a private key default is skipped for remote desktop.
	if _, err := database.GroupUpdate(owner, group.ID, store.GroupPatch{Defaults: &store.GroupDefaults{Username: "ops", AuthKind: "key", CredID: "key"}}); err != nil {
		t.Fatal(err)
	}
	asset, err = resolver.ResolveAsset(owner, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if asset.Username != "ops" || asset.HasPassword {
		t.Fatalf("asset after edit = %+v", asset)
	}
	if password, err := resolver.ResolvePassword(owner, row.ID); err != nil || password != "" {
		t.Fatalf("a private key default released a password: %q, %v", password, err)
	}
}

func TestStoreResolverRefusesAnotherOwnersGroupCredential(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	owner := ipc.WithUserID(ctx, "owner-a")
	foreign := ipc.WithUserID(ctx, "owner-b")
	if _, err := database.CredentialPut(owner, store.CredentialInput{ID: "mine", Name: "mine", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CredentialPut(foreign, store.CredentialInput{ID: "theirs", Name: "theirs", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	group, err := database.GroupCreate(owner, store.GroupInput{Name: "g", Defaults: &store.GroupDefaults{AuthKind: "password", CredID: "mine"}})
	if err != nil {
		t.Fatal(err)
	}
	host := "win.internal"
	row, err := database.AssetCreate(owner, store.AssetInput{GroupID: &group.ID, Kind: KindRDP, Name: "win", Host: &host, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Exec("UPDATE asset_group SET default_cred_id = ? WHERE id = ?", "theirs", group.ID).Error; err != nil {
		t.Fatal(err)
	}
	resolver := NewStoreResolver(database, stubDecryptor{"must-not-be-released"})
	if _, err := resolver.ResolveAsset(owner, row.ID); err == nil || !strings.Contains(err.Error(), "g") {
		t.Fatalf("resolve err = %v", err)
	}
	if password, err := resolver.ResolvePassword(owner, row.ID); err == nil || password != "" {
		t.Fatalf("another owner's credential was released: %q, %v", password, err)
	}
}

func TestExplicitNoPasswordIsNotOverriddenByGroupPassword(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	owner := ipc.WithUserID(ctx, "owner-a")
	if _, err := database.CredentialPut(owner, store.CredentialInput{ID: "pw", Name: "group-pw", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	group, err := database.GroupCreate(owner, store.GroupInput{Name: "g", Defaults: &store.GroupDefaults{AuthKind: "password", CredID: "pw"}})
	if err != nil {
		t.Fatal(err)
	}
	host := "vnc.internal"
	none := store.AuthKindNone
	open, err := database.AssetCreate(owner, store.AssetInput{GroupID: &group.ID, Kind: KindVNC, Name: "inherits", Host: &host, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	noPassword, err := database.AssetCreate(owner, store.AssetInput{GroupID: &group.ID, Kind: KindVNC, Name: "none", Host: &host, AuthKind: &none, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	prompting, err := database.AssetCreate(owner, store.AssetInput{GroupID: &group.ID, Kind: KindRDP, Name: "prompt", Host: &host, OptionsJSON: `{"promptPassword":true}`})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(NewStoreResolver(database, stubDecryptor{"group-secret"}))
	t.Cleanup(svc.Close)

	inherited, err := svc.Prepare(owner, open.ID)
	if err != nil || !inherited.HasPassword || inherited.NoPassword {
		t.Fatalf("an empty vnc asset should inherit the group password: %+v, %v", inherited, err)
	}
	explicit, err := svc.Prepare(owner, noPassword.ID)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.HasPassword || !explicit.NoPassword || explicit.PromptPassword {
		t.Fatalf("explicit none prepare = %+v", explicit)
	}
	rdpNone, err := database.AssetCreate(owner, store.AssetInput{GroupID: &group.ID, Kind: KindRDP, Name: "rdp-none", Host: &host, AuthKind: &none, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	rdpPrepared, err := svc.Prepare(owner, rdpNone.ID)
	if err != nil || rdpPrepared.HasPassword || !rdpPrepared.NoPassword {
		t.Fatalf("rdp none prepare = %+v, %v", rdpPrepared, err)
	}
	secret, err := svc.ReleaseSecret(owner, rdpPrepared.Ticket)
	if err != nil || secret.Password != "" {
		t.Fatalf("a password was released for an asset set to none: %q, %v", secret.Password, err)
	}
	resolver := NewStoreResolver(database, stubDecryptor{"group-secret"})
	if password, err := resolver.ResolvePassword(owner, noPassword.ID); err != nil || password != "" {
		t.Fatalf("resolve password for none = %q, %v", password, err)
	}
	asked, err := svc.Prepare(owner, prompting.ID)
	if err != nil || !asked.PromptPassword || asked.HasPassword {
		t.Fatalf("a prompting rdp asset must ask for its own password: %+v, %v", asked, err)
	}
	// An unusable group default cannot fail a prompting asset.
	if err := database.GORM().Exec("DELETE FROM credential WHERE id = ?", "pw").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Prepare(owner, prompting.ID); err != nil {
		t.Fatalf("prompting asset failed on a broken group default: %v", err)
	}
	if _, err := svc.Prepare(owner, noPassword.ID); err != nil {
		t.Fatalf("explicit none failed on a broken group default: %v", err)
	}
}
