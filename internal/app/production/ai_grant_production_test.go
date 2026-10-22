package production

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

type execTargetsFixture struct {
	services  *ProductionServices
	database  *store.Store
	connector *broadcastGateConnector
	owner     context.Context
	other     context.Context
}

func newExecTargetsFixture(t *testing.T) *execTargetsFixture {
	t.Helper()
	owner := ipc.WithUserID(t.Context(), "owner-a")
	other := ipc.WithUserID(t.Context(), "owner-b")
	database, err := store.OpenInMemory(owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	connector := &broadcastGateConnector{transports: make(map[string]*broadcastGateTransport)}
	manager := session.NewManager(session.Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	hostKeys, err := newProductionHostKeyStore(owner, filepath.Join(t.TempDir(), "known_hosts.json"), database, false)
	if err != nil {
		t.Fatal(err)
	}
	credentialVault := vault.Load(owner, database)
	if err := credentialVault.InitMaster(owner, "test-password"); err != nil {
		if err := credentialVault.UnlockMaster(owner, "test-password"); err != nil {
			t.Fatal(err)
		}
	}
	services := &ProductionServices{Store: database, Sessions: manager, Vault: credentialVault, hostKeys: hostKeys, desktop: false}
	return &execTargetsFixture{services: services, database: database, connector: connector, owner: owner, other: other}
}

func (f *execTargetsFixture) approveHostKey(t *testing.T, host string, port int32) {
	t.Helper()
	if err := f.database.KnownHostAccept(f.owner, host, port, "ssh-ed25519", "SHA256:"+host); err != nil {
		t.Fatal(err)
	}
}

func (f *execTargetsFixture) setOptIn(t *testing.T, ctx context.Context, key, value string) {
	t.Helper()
	if err := f.database.SettingSet(ctx, key, value); err != nil {
		t.Fatal(err)
	}
}

func (f *execTargetsFixture) connectAsset(t *testing.T, ctx context.Context, name, tags string) store.AssetRow {
	t.Helper()
	hostname := name + ".example"
	row, err := f.database.AssetCreate(ctx, store.AssetInput{Kind: session.KindSSH, Name: name, Host: &hostname, Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.services.Sessions.ConnectFor(ctx, session.Asset{ID: row.ID, Name: row.Name, Kind: session.KindSSH, StoreBacked: true}); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestResolveProductionExecTargetsUsesConfiguredEnvironmentList(t *testing.T) {
	fixture := newExecTargetsFixture(t)
	live := fixture.connectAsset(t, fixture.owner, "live", "env:live")
	production := fixture.connectAsset(t, fixture.owner, "prod", "env:production")
	staging := fixture.connectAsset(t, fixture.owner, "staging", "env:staging")

	// Built-in production names are always classified as production.
	targets, err := resolveProductionExecTargets(fixture.owner, fixture.services, []string{production.ID}, nil)
	if err != nil || !targets.Production {
		t.Fatalf("built-in production targets = %+v, %v", targets, err)
	}
	// Custom environment names are not production without the configuration.
	targets, err = resolveProductionExecTargets(fixture.owner, fixture.services, []string{live.ID}, nil)
	if err != nil || targets.Production {
		t.Fatalf("unconfigured live targets = %+v, %v", targets, err)
	}
	if err := guard.SetProductionEnvironments(fixture.owner, fixture.database, []string{"live"}); err != nil {
		t.Fatal(err)
	}
	targets, err = resolveProductionExecTargets(fixture.owner, fixture.services, []string{live.ID}, nil)
	if err != nil || !targets.Production {
		t.Fatalf("configured live targets = %+v, %v", targets, err)
	}
	// The configuration is per owner: another owner's own live asset is not flagged.
	otherLive := fixture.connectAsset(t, fixture.other, "other-live", "env:live")
	targets, err = resolveProductionExecTargets(fixture.other, fixture.services, []string{otherLive.ID}, nil)
	if err != nil || targets.Production {
		t.Fatalf("cross-owner live targets = %+v, %v", targets, err)
	}
	// Non-production environments stay unflagged for the configured owner.
	targets, err = resolveProductionExecTargets(fixture.owner, fixture.services, []string{staging.ID}, nil)
	if err != nil || targets.Production {
		t.Fatalf("staging targets = %+v, %v", targets, err)
	}
}

func TestResolveProductionExecTargetsConnectsDisconnectedAssetWithOptIn(t *testing.T) {
	fixture := newExecTargetsFixture(t)
	hostname := "disconnected.example"
	row, err := fixture.database.AssetCreate(fixture.owner, store.AssetInput{
		Kind: session.KindSSH, Name: "disconnected", Host: &hostname, Tags: "env:staging",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.approveHostKey(t, hostname, 22)

	// Default: no opt-in keeps the original no-auto-connect error and never dials.
	if _, err := resolveProductionExecTargets(fixture.owner, fixture.services, []string{row.ID}, nil); err == nil ||
		!strings.Contains(err.Error(), "不会自动连接") {
		t.Fatalf("default resolve error = %v", err)
	}
	if fixture.connector.dials.Load() != 0 {
		t.Fatalf("default resolve dialed %d times", fixture.connector.dials.Load())
	}

	// With the target-set opt-in the asset is connected exactly once and the
	// second resolution reuses the live session without redialing.
	fixture.setOptIn(t, fixture.owner, TargetSetConnectOptInSettingKeyForUser(fixture.owner), "true")
	targets, err := resolveProductionExecTargets(fixture.owner, fixture.services, []string{row.ID}, nil)
	if err != nil {
		t.Fatalf("opt-in resolve = %v", err)
	}
	if len(targets.SessionIDs) != 1 || targets.Production {
		t.Fatalf("resolved targets = %+v", targets)
	}
	if fixture.connector.dials.Load() != 1 {
		t.Fatalf("opt-in resolve dialed %d times, want 1", fixture.connector.dials.Load())
	}
	if _, err := resolveProductionExecTargets(fixture.owner, fixture.services, []string{row.ID}, nil); err != nil {
		t.Fatalf("second resolve = %v", err)
	}
	if fixture.connector.dials.Load() != 1 {
		t.Fatalf("second resolve redialed: %d dials", fixture.connector.dials.Load())
	}

	// Already-connected assets are never dialed.
	connected := fixture.connectAsset(t, fixture.owner, "connected", "")
	before := fixture.connector.dials.Load()
	if _, err := resolveProductionExecTargets(fixture.owner, fixture.services, []string{connected.ID}, nil); err != nil {
		t.Fatalf("connected resolve = %v", err)
	}
	if fixture.connector.dials.Load() != before {
		t.Fatalf("connected resolve redialed: %d dials", fixture.connector.dials.Load())
	}
}

func TestResolveProductionExecTargetsConnectGates(t *testing.T) {
	fixture := newExecTargetsFixture(t)
	fixture.setOptIn(t, fixture.owner, TargetSetConnectOptInSettingKeyForUser(fixture.owner), "true")

	unknownKey := fixture.createAsset(t, fixture.owner, "unknown-key", "unknown.example", "")
	if _, err := resolveProductionExecTargets(fixture.owner, fixture.services, []string{unknownKey.ID}, nil); !errors.Is(err, guard.ErrConnectHostKeyUnknown) {
		t.Fatalf("unknown host key resolve = %v", err)
	}
	if fixture.connector.dials.Load() != 0 {
		t.Fatalf("unknown host key dialed %d times", fixture.connector.dials.Load())
	}

	production := fixture.createAsset(t, fixture.owner, "production", "production.example", "env:production")
	fixture.approveHostKey(t, "production.example", 22)
	if _, err := resolveProductionExecTargets(fixture.owner, fixture.services, []string{production.ID}, nil); !errors.Is(err, guard.ErrConnectProductionConfirmationRequired) {
		t.Fatalf("unconfirmed production resolve = %v", err)
	}
	fixture.setOptIn(t, fixture.owner, TargetSetConnectProductionOptInSettingKeyForUser(fixture.owner), "true")
	if _, err := resolveProductionExecTargets(fixture.owner, fixture.services, []string{production.ID}, nil); err != nil {
		t.Fatalf("confirmed production resolve = %v", err)
	}

	credentialID, err := fixture.database.CredentialPut(fixture.owner, store.CredentialInput{
		Name: "key", Kind: vault.KindPrivateKey, Nonce: []byte{1}, Blob: []byte{2},
	})
	if err != nil {
		t.Fatal(err)
	}
	withCred := fixture.createAsset(t, fixture.owner, "with-cred", "with-cred.example", "")
	if _, err := fixture.database.AssetUpdate(fixture.owner, withCred.ID, store.AssetPatch{CredID: store.Value(credentialID)}); err != nil {
		t.Fatal(err)
	}
	fixture.approveHostKey(t, "with-cred.example", 22)
	fixture.services.Vault.Lock()
	if _, err := resolveProductionExecTargets(fixture.owner, fixture.services, []string{withCred.ID}, nil); !errors.Is(err, guard.ErrConnectVaultUnavailable) {
		t.Fatalf("locked vault resolve = %v", err)
	}
	if err := fixture.services.Vault.UnlockMaster(fixture.owner, "test-password"); err != nil {
		t.Fatal(err)
	}

	// Cross-owner: another owner's asset is rejected before any dial.
	foreign := fixture.createAsset(t, fixture.other, "foreign", "foreign.example", "")
	if _, err := resolveProductionExecTargets(fixture.owner, fixture.services, []string{foreign.ID}, nil); err == nil {
		t.Fatal("cross-owner asset resolved for the wrong owner")
	}
	if _, err := resolveProductionExecTargets(fixture.other, fixture.services, []string{foreign.ID}, nil); err == nil ||
		!strings.Contains(err.Error(), "不会自动连接") {
		t.Fatalf("cross-owner default resolve = %v", err)
	}
}

func (f *execTargetsFixture) createAsset(t *testing.T, ctx context.Context, name, host, tags string) store.AssetRow {
	t.Helper()
	row, err := f.database.AssetCreate(ctx, store.AssetInput{Kind: session.KindSSH, Name: name, Host: &host, Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	return row
}
