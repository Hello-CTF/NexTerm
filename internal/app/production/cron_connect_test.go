package production

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	gossh "golang.org/x/crypto/ssh"
)

type cronConnectFixture struct {
	services *ProductionServices
	database *store.Store
	owner    context.Context
	other    context.Context
	trigger  cron.Trigger
}

func newCronConnectFixture(t *testing.T) *cronConnectFixture {
	t.Helper()
	owner := ipc.WithUserID(t.Context(), "owner-a")
	other := ipc.WithUserID(t.Context(), "owner-b")
	database, err := store.OpenInMemory(owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	credentialVault := vault.Load(owner, database)
	if err := credentialVault.InitMaster(owner, "test-password"); err != nil {
		if err := credentialVault.UnlockMaster(owner, "test-password"); err != nil {
			t.Fatal(err)
		}
	}
	hostKeys, err := newProductionHostKeyStore(owner, filepath.Join(t.TempDir(), "known_hosts.json"), database, false)
	if err != nil {
		t.Fatal(err)
	}
	manager := newExecBatchTestManager(t, newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	}))
	services := &ProductionServices{
		Store: database, Sessions: manager, Vault: credentialVault,
		hostKeys: hostKeys, desktop: false,
	}
	trigger := cron.Trigger{
		RunID: "run-1", JobID: "job-1", SessionID: "conversation-1",
		Prompt: "inspect", OwnerID: "owner-a", OwnerKind: cron.OwnerKindAccount,
	}
	return &cronConnectFixture{services: services, database: database, owner: owner, other: other, trigger: trigger}
}

func (f *cronConnectFixture) createSSHAsset(t *testing.T, name, host, tags string) store.AssetRow {
	t.Helper()
	port := int32(22)
	row, err := f.database.AssetCreate(f.owner, store.AssetInput{
		Kind: session.KindSSH, Name: name, Host: &host, Port: &port, Tags: tags,
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func (f *cronConnectFixture) approveHostKey(t *testing.T, host string, port int32) {
	t.Helper()
	if err := f.database.KnownHostAccept(f.owner, host, port, "ssh-ed25519", "SHA256:"+host); err != nil {
		t.Fatal(err)
	}
}

func (f *cronConnectFixture) setConnectOptIn(t *testing.T, ctx context.Context, key string, value bool) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.database.SettingSet(ctx, key, string(encoded)); err != nil {
		t.Fatal(err)
	}
}

func TestCronConnectEvidenceOwnerMatch(t *testing.T) {
	fixture := newCronConnectFixture(t)
	row := fixture.createSSHAsset(t, "owned", "owned.example", "")
	evidence := cronConnectEvidence{services: fixture.services}
	match, err := evidence.OwnerMatch(fixture.owner, fixture.trigger, row.ID)
	if err != nil || !match {
		t.Fatalf("owner match = %v, %v", match, err)
	}
	match, err = evidence.OwnerMatch(fixture.other, fixture.trigger, row.ID)
	if err != nil || match {
		t.Fatalf("cross-owner match = %v, %v", match, err)
	}
	match, err = evidence.OwnerMatch(fixture.owner, fixture.trigger, "missing-asset")
	if err != nil || match {
		t.Fatalf("missing asset match = %v, %v", match, err)
	}
}

func TestCronConnectEvidenceHostKey(t *testing.T) {
	fixture := newCronConnectFixture(t)
	evidence := cronConnectEvidence{services: fixture.services}

	sshAsset := fixture.createSSHAsset(t, "ssh", "ssh.example", "")
	if verdict, err := evidence.HostKey(fixture.owner, fixture.trigger, sshAsset.ID); err != nil || verdict != guard.HostKeyUnknown {
		t.Fatalf("unregistered SSH verdict = %q, %v", verdict, err)
	}
	fixture.approveHostKey(t, "ssh.example", 22)
	if verdict, err := evidence.HostKey(fixture.owner, fixture.trigger, sshAsset.ID); err != nil || verdict != guard.HostKeyKnown {
		t.Fatalf("approved SSH verdict = %q, %v", verdict, err)
	}

	local, err := fixture.database.AssetCreate(fixture.owner, store.AssetInput{Kind: session.KindLocal, Name: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if verdict, err := evidence.HostKey(fixture.owner, fixture.trigger, local.ID); err != nil || verdict != guard.HostKeyKnown {
		t.Fatalf("local verdict = %q, %v", verdict, err)
	}

	fixture.services.hostKeys = nil
	if verdict, err := evidence.HostKey(fixture.owner, fixture.trigger, sshAsset.ID); err != nil || verdict != guard.HostKeyUnknown {
		t.Fatalf("nil host-key store verdict = %q, %v", verdict, err)
	}
}

func TestCronConnectEvidenceVaultReady(t *testing.T) {
	fixture := newCronConnectFixture(t)
	evidence := cronConnectEvidence{services: fixture.services}

	agentless := fixture.createSSHAsset(t, "agent", "agent.example", "")
	if ready, err := evidence.VaultReady(fixture.owner, fixture.trigger, agentless.ID); err != nil || !ready {
		t.Fatalf("credential-less vault ready = %v, %v", ready, err)
	}

	credentialID, err := fixture.database.CredentialPut(fixture.owner, store.CredentialInput{
		Name: "key", Kind: vault.KindPrivateKey, Nonce: []byte{1}, Blob: []byte{2},
	})
	if err != nil {
		t.Fatal(err)
	}
	withCred := fixture.createSSHAsset(t, "cred", "cred.example", "")
	if _, err := fixture.database.AssetUpdate(fixture.owner, withCred.ID, store.AssetPatch{CredID: store.Value(credentialID)}); err != nil {
		t.Fatal(err)
	}
	if ready, err := evidence.VaultReady(fixture.owner, fixture.trigger, withCred.ID); err != nil || !ready {
		t.Fatalf("unlocked credential vault ready = %v, %v", ready, err)
	}
	fixture.services.Vault.Lock()
	if ready, err := evidence.VaultReady(fixture.owner, fixture.trigger, withCred.ID); err != nil || ready {
		t.Fatalf("locked vault ready = %v, %v", ready, err)
	}
	if err := fixture.services.Vault.UnlockMaster(fixture.owner, "test-password"); err != nil {
		t.Fatal(err)
	}
	otherCredID, err := fixture.database.CredentialPut(fixture.other, store.CredentialInput{
		Name: "other-key", Kind: vault.KindPrivateKey, Nonce: []byte{1}, Blob: []byte{2},
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign := fixture.createSSHAsset(t, "foreign-cred", "foreign.example", "")
	if _, err := fixture.database.AssetUpdate(fixture.owner, foreign.ID, store.AssetPatch{CredID: store.Value(otherCredID)}); err != nil {
		t.Fatal(err)
	}
	if ready, err := evidence.VaultReady(fixture.owner, fixture.trigger, foreign.ID); err != nil || ready {
		t.Fatalf("foreign credential vault ready = %v, %v", ready, err)
	}
}

func TestCronConnectEvidenceEnvironmentAndProductionOptIn(t *testing.T) {
	fixture := newCronConnectFixture(t)
	evidence := cronConnectEvidence{services: fixture.services}
	production := fixture.createSSHAsset(t, "prod", "prod.example", "env:production")
	if environment, err := evidence.Environment(fixture.owner, fixture.trigger, production.ID); err != nil || environment != "production" {
		t.Fatalf("environment = %q, %v", environment, err)
	}
	if confirmed, err := evidence.ProductionConfirmed(fixture.owner, fixture.trigger, production.ID); err != nil || confirmed {
		t.Fatalf("default production opt-in = %v, %v", confirmed, err)
	}
	fixture.setConnectOptIn(t, fixture.owner, cron.ConnectProductionOptInSettingKeyForUser(fixture.owner), true)
	if confirmed, err := evidence.ProductionConfirmed(fixture.owner, fixture.trigger, production.ID); err != nil || !confirmed {
		t.Fatalf("enabled production opt-in = %v, %v", confirmed, err)
	}
	if confirmed, err := evidence.ProductionConfirmed(fixture.other, fixture.trigger, production.ID); err != nil || confirmed {
		t.Fatalf("cross-owner production opt-in = %v, %v", confirmed, err)
	}
}

func TestCronConnectorEndToEndGates(t *testing.T) {
	fixture := newCronConnectFixture(t)
	connector := newCronConnector(fixture.services)
	staging := fixture.createSSHAsset(t, "staging", "staging.example", "env:staging")
	fixture.approveHostKey(t, "staging.example", 22)

	if _, err := connector.Connect(fixture.owner, fixture.trigger, staging.ID); !errors.Is(err, guard.ErrConnectOptInRequired) {
		t.Fatalf("connect without opt-in = %v", err)
	}
	fixture.setConnectOptIn(t, fixture.owner, cron.ConnectOptInSettingKeyForUser(fixture.owner), true)

	unregistered := fixture.createSSHAsset(t, "unregistered", "unregistered.example", "")
	if _, err := connector.Connect(fixture.owner, fixture.trigger, unregistered.ID); !errors.Is(err, guard.ErrConnectHostKeyUnknown) {
		t.Fatalf("connect with unknown host key = %v", err)
	}
	if sessions, err := fixture.services.Sessions.ListSessionsFor(fixture.owner); err != nil || len(sessions) != 0 {
		t.Fatalf("rejected connects dialed %d sessions: %v", len(sessions), err)
	}

	sessionID, err := connector.Connect(fixture.owner, fixture.trigger, staging.ID)
	if err != nil || sessionID == "" {
		t.Fatalf("authorized connect = %q, %v", sessionID, err)
	}

	production := fixture.createSSHAsset(t, "production", "production.example", "env:production")
	fixture.approveHostKey(t, "production.example", 22)
	if _, err := connector.Connect(fixture.owner, fixture.trigger, production.ID); !errors.Is(err, guard.ErrConnectProductionConfirmationRequired) {
		t.Fatalf("production connect without confirmation = %v", err)
	}
	fixture.setConnectOptIn(t, fixture.owner, cron.ConnectProductionOptInSettingKeyForUser(fixture.owner), true)
	if _, err := connector.Connect(fixture.owner, fixture.trigger, production.ID); err != nil {
		t.Fatalf("confirmed production connect = %v", err)
	}

	if err := guard.SetProductionEnvironments(fixture.owner, fixture.database, []string{"live"}); err != nil {
		t.Fatal(err)
	}
	live := fixture.createSSHAsset(t, "live", "live.example", "env:live")
	fixture.approveHostKey(t, "live.example", 22)
	fixture.setConnectOptIn(t, fixture.owner, cron.ConnectProductionOptInSettingKeyForUser(fixture.owner), false)
	if _, err := connector.Connect(fixture.owner, fixture.trigger, live.ID); !errors.Is(err, guard.ErrConnectProductionConfirmationRequired) {
		t.Fatalf("configured production connect without confirmation = %v", err)
	}
	fixture.setConnectOptIn(t, fixture.owner, cron.ConnectProductionOptInSettingKeyForUser(fixture.owner), true)
	if _, err := connector.Connect(fixture.owner, fixture.trigger, live.ID); err != nil {
		t.Fatalf("confirmed configured production connect = %v", err)
	}
}

func TestCronConnectDialNeverAutoAcceptsHostKeys(t *testing.T) {
	fixture := newCronConnectFixture(t)
	row := fixture.createSSHAsset(t, "dial", "dial.example", "")
	dial := cronConnectDial(fixture.services)
	sessionID, err := dial(fixture.owner, fixture.trigger, row.ID)
	if err != nil || sessionID == "" {
		t.Fatalf("dial = %q, %v", sessionID, err)
	}
	connected, err := fixture.services.Sessions.SessionFor(fixture.owner, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	options, ok := connected.Asset().Options.(map[string]any)
	if !ok {
		t.Fatalf("asset options = %T", connected.Asset().Options)
	}
	if _, exists := options["autoAcceptUnknownHost"]; exists {
		t.Fatal("cron dial set autoAcceptUnknownHost")
	}
	if _, err := dial(fixture.owner, fixture.trigger, "missing-asset"); err == nil {
		t.Fatal("dial accepted a missing asset")
	}
}

func TestScopeForConversationMapsDisconnectedAsset(t *testing.T) {
	fixture := newCronConnectFixture(t)
	disconnected := fixture.createSSHAsset(t, "disconnected", "disconnected.example", "")
	conversation, err := fixture.database.ConvCreate(fixture.owner, "scoped", map[string]any{
		"scope": map[string]any{"assetId": disconnected.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	trigger := fixture.trigger
	trigger.SessionID = conversation.ID
	scopeFor := scopeForConversation(fixture.database, productionScopeValidator(fixture.services))
	if _, err := scopeFor(fixture.owner, trigger); !errors.Is(err, cron.ErrAssetDisconnected) {
		t.Fatalf("disconnected scope error = %v, want ErrAssetDisconnected", err)
	}
	var disconnectedErr *cron.AssetDisconnectedError
	if _, err := scopeFor(fixture.owner, trigger); !errors.As(err, &disconnectedErr) || disconnectedErr.AssetID != disconnected.ID {
		t.Fatalf("disconnected scope detail = %v", err)
	}

	connectedRow := fixture.createSSHAsset(t, "connected", "connected.example", "")
	if _, err := fixture.services.Sessions.ConnectFor(fixture.owner, session.Asset{ID: connectedRow.ID, Name: connectedRow.Name, Kind: session.KindSSH, StoreBacked: true}); err != nil {
		t.Fatal(err)
	}
	connectedConversation, err := fixture.database.ConvCreate(fixture.owner, "connected", map[string]any{
		"scope": map[string]any{"assetId": connectedRow.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	trigger.SessionID = connectedConversation.ID
	scope, err := scopeFor(fixture.owner, trigger)
	if err != nil || scope.AssetID != connectedRow.ID {
		t.Fatalf("connected scope = %+v, %v", scope, err)
	}

	trigger.SessionID = "missing-conversation"
	if _, err := scopeFor(fixture.owner, trigger); errors.Is(err, cron.ErrAssetDisconnected) {
		t.Fatal("missing conversation mapped to disconnected asset")
	}
}

type cronFakeRunner struct {
	mu     sync.Mutex
	args   []agent.ChatArgs
	emit   func(agent.Stream)
	cancel []string
}

func (f *cronFakeRunner) Start(ctx context.Context, args agent.ChatArgs, factory agent.StreamFactory) (agent.StartResponse, error) {
	stream, err := factory(ctx, "", "run-1")
	if err != nil {
		return agent.StartResponse{}, err
	}
	f.mu.Lock()
	f.args = append(f.args, args)
	f.mu.Unlock()
	if f.emit != nil {
		go func() {
			f.emit(stream)
			_ = stream.Close()
		}()
	}
	return agent.StartResponse{JobID: "run-1", ConversationID: args.ConversationID}, nil
}

func (f *cronFakeRunner) Cancel(jobID string) error {
	f.mu.Lock()
	f.cancel = append(f.cancel, jobID)
	f.mu.Unlock()
	return nil
}

func (f *cronFakeRunner) Wait(context.Context, string) error { return nil }

func TestCronExecutorConnectsThroughCompositionRoot(t *testing.T) {
	fixture := newCronConnectFixture(t)
	disconnected := fixture.createSSHAsset(t, "disconnected", "disconnected.example", "")
	fixture.approveHostKey(t, "disconnected.example", 22)
	conversation, err := fixture.database.ConvCreate(fixture.owner, "scoped", map[string]any{
		"scope": map[string]any{"assetId": disconnected.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	trigger := fixture.trigger
	trigger.SessionID = conversation.ID

	runner := &cronFakeRunner{emit: func(stream agent.Stream) {
		_ = stream.Send(context.Background(), agent.Event{Type: "done"})
	}}
	executor := &cron.AgentExecutor{
		Runner:   runner,
		ScopeFor: scopeForConversation(fixture.database, productionScopeValidator(fixture.services)),
		Connect:  newCronConnector(fixture.services),
	}

	if err := executor.Execute(fixture.owner, trigger); !errors.Is(err, cron.ErrAssetDisconnected) {
		t.Fatalf("execute without opt-in = %v, want the original disconnected failure", err)
	}
	fixture.setConnectOptIn(t, fixture.owner, cron.ConnectOptInSettingKeyForUser(fixture.owner), true)
	if err := executor.Execute(fixture.owner, trigger); err != nil {
		t.Fatalf("execute with opt-in = %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.args) != 1 || runner.args[0].Scope.AssetID != disconnected.ID {
		t.Fatalf("started runs = %+v", runner.args)
	}
	if _, err := productionScopeValidator(fixture.services).liveSessionForAsset(fixture.owner, disconnected.ID); err != nil {
		t.Fatalf("connect flow left no live session for the asset: %v", err)
	}
}

func TestCronExecutorConnectRejectionStaysHonest(t *testing.T) {
	fixture := newCronConnectFixture(t)
	production := fixture.createSSHAsset(t, "production", "production.example", "env:production")
	fixture.approveHostKey(t, "production.example", 22)
	conversation, err := fixture.database.ConvCreate(fixture.owner, "scoped", map[string]any{
		"scope": map[string]any{"assetId": production.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	trigger := fixture.trigger
	trigger.SessionID = conversation.ID
	fixture.setConnectOptIn(t, fixture.owner, cron.ConnectOptInSettingKeyForUser(fixture.owner), true)

	runner := &cronFakeRunner{}
	executor := &cron.AgentExecutor{
		Runner:   runner,
		ScopeFor: scopeForConversation(fixture.database, productionScopeValidator(fixture.services)),
		Connect:  newCronConnector(fixture.services),
	}
	err = executor.Execute(fixture.owner, trigger)
	if !errors.Is(err, guard.ErrConnectProductionConfirmationRequired) {
		t.Fatalf("execute error = %v, want production confirmation required", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.args) != 0 {
		t.Fatalf("started %d runs after rejection", len(runner.args))
	}
	if sessions, err := fixture.services.Sessions.ListSessionsFor(fixture.owner); err != nil || len(sessions) != 0 {
		t.Fatalf("rejected connect dialed %d sessions: %v", len(sessions), err)
	}
}

func TestCronConnectDialRejectsStoredAutoAcceptWithActuallyUnknownKey(t *testing.T) {
	production := newHostKeyTestProduction(t)
	ctx := context.Background()
	server := newHostKeyTestServer(t)
	// The asset stores auto-accept, and the trust database holds an approval
	// for a DIFFERENT key, so the key the server presents is actually unknown.
	assetID := createConnectorTestAsset(t, production, "stored-auto-accept", "127.0.0.1", server.port(), "password", "",
		`{"password":"host-key-test-secret","autoAcceptUnknownHost":true}`)
	const foreignFingerprint = "SHA256:bm90LXRoZS1yZWFsLWtleQ=="
	if err := production.Services.Store.KnownHostAccept(ctx, "127.0.0.1", int32(server.port()), "ssh-ed25519", foreignFingerprint); err != nil {
		t.Fatal(err)
	}
	if err := production.Services.Store.SettingSet(ctx, cron.ConnectOptInSettingKey, "true"); err != nil {
		t.Fatal(err)
	}
	connector := newCronConnector(&production.Services)
	trigger := cron.Trigger{RunID: "run-1", JobID: "job-1", OwnerKind: cron.OwnerKindOpen}
	if _, err := connector.Connect(ctx, trigger, assetID); !errors.Is(err, ssh.ErrHostKeyPending) {
		t.Fatalf("connect error = %v, want host key pending", err)
	}
	keys, err := production.Services.Store.KnownHostList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Fingerprint != foreignFingerprint {
		t.Fatalf("trust store changed: %+v", keys)
	}
	if sessions, err := production.Services.Sessions.ListSessionsFor(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("rejected connect created %d sessions: %v", len(sessions), err)
	}
}

func TestPlainConnectKeepsStoredAutoAcceptBehavior(t *testing.T) {
	production := newHostKeyTestProduction(t)
	ctx := context.Background()
	server := newHostKeyTestServer(t)
	assetID := createConnectorTestAsset(t, production, "stored-auto-accept", "127.0.0.1", server.port(), "password", "",
		`{"password":"host-key-test-secret","autoAcceptUnknownHost":true}`)
	row, err := production.Services.Store.AssetGetLive(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := productionSessionAsset(row, false, "")
	if err != nil {
		t.Fatal(err)
	}
	connected, err := production.Services.Sessions.ConnectFor(ctx, asset)
	if err != nil {
		t.Fatalf("interactive connect with stored auto-accept = %v", err)
	}
	if connected == nil || connected.ID == "" {
		t.Fatal("interactive connect returned no session")
	}
}

func TestCronConnectDialRejectsUnknownJumpHop(t *testing.T) {
	production := newHostKeyTestProduction(t)
	ctx := context.Background()
	jump := newSSHConnectorServer(t, nil)
	target := newHostKeyTestServer(t)
	targetKey := (*target.signer.Load()).PublicKey()
	if err := production.Services.Store.KnownHostAccept(ctx, "127.0.0.1", int32(target.port()), targetKey.Type(), gossh.FingerprintSHA256(targetKey)); err != nil {
		t.Fatal(err)
	}
	jumpAssetID := createConnectorTestAsset(t, production, "jump", "127.0.0.1", jump.port(), "password", "",
		`{"password":"secret","autoAcceptUnknownHost":true}`)
	targetAssetID := createConnectorTestAsset(t, production, "target", "127.0.0.1", target.port(), "password", "",
		`{"password":"host-key-test-secret","jumpAssetId":"`+jumpAssetID+`"}`)
	if err := production.Services.Store.SettingSet(ctx, cron.ConnectOptInSettingKey, "true"); err != nil {
		t.Fatal(err)
	}
	connector := newCronConnector(&production.Services)
	trigger := cron.Trigger{RunID: "run-1", JobID: "job-1", OwnerKind: cron.OwnerKindOpen}
	_, err := connector.Connect(ctx, trigger, targetAssetID)
	if !errors.Is(err, ssh.ErrHostKeyPending) {
		t.Fatalf("connect error = %v, want host key pending for the jump hop", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(jump.port())) {
		t.Fatalf("connect error does not name the jump hop: %v", err)
	}
	keys, err := production.Services.Store.KnownHostList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if int(key.Port) == jump.port() {
			t.Fatalf("unknown jump key was trusted: %+v", key)
		}
	}
	if sessions, err := production.Services.Sessions.ListSessionsFor(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("rejected jump connect created %d sessions: %v", len(sessions), err)
	}
}
