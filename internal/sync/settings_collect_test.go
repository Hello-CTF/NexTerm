package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestCollectKnownHostsFieldsAndPagination(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	want := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		id := ids.New()
		want = append(want, id)
		if _, err := instance.db.DB().ExecContext(ctx,
			`INSERT INTO known_host(id, host, port, key_type, fingerprint, added_at) VALUES(?,?,?,?,?,?)`,
			id, fmt.Sprintf("host-%d.example.com", index), 22, "ssh-ed25519", "SHA256:fp", int64(100+index)); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(want)

	var walked []string
	afterID := ""
	pages := 0
	for {
		result, err := instance.service.CollectKnownHosts(ctx, CollectKnownHostsRequest{AfterID: afterID, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, entry := range result.KnownHosts {
			if !strings.HasPrefix(entry.Host, "host-") || entry.Port != 22 || entry.KeyType != "ssh-ed25519" || entry.Fingerprint != "SHA256:fp" {
				t.Fatalf("collect fields mangled: %+v", entry)
			}
			walked = append(walked, entry.ID)
		}
		if !result.HasMore {
			break
		}
		afterID = result.NextAfterID
	}
	if pages != 3 || len(walked) != len(want) {
		t.Fatalf("pages=%d walked=%v want=%v", pages, walked, want)
	}
	sort.Strings(walked)
	for index := range want {
		if walked[index] != want[index] {
			t.Fatalf("page walk mismatch: %v vs %v", walked, want)
		}
	}
}

func putLockedTestAIProfile(t *testing.T, instance *testInstance, id, secret string) {
	t.Helper()
	putTestAIProfile(t, instance, sealedTestAIProfile(t, instance, id, secret), 100)
	instance.vault.Lock()
}

func TestCollectAIProfilesWithheldWithoutRevealRequest(t *testing.T) {
	instance := newTestInstance(t, true)
	profileID := ids.New()
	putTestAIProfile(t, instance, sealedTestAIProfile(t, instance, profileID, "withheld-key"), 100)

	result, err := instance.service.CollectAIProfiles(context.Background(), CollectAIProfilesRequest{RevealSecrets: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Profiles) != 1 {
		t.Fatalf("profiles=%+v", result.Profiles)
	}
	entry := result.Profiles[0]
	if entry.APIKeyState != CollectSecretWithheld || entry.APIKey != nil || !entry.APIKeySet {
		t.Fatalf("withheld outcome must be explicit: %+v", entry)
	}
	if entry.UpdatedAt != 100 || entry.Model != "local-model" || entry.BaseURL != "https://local.example.com" {
		t.Fatalf("metadata must stay readable: %+v", entry)
	}
	requireNoSecretLeak(t, result, "withheld-key")
}

func TestCollectAIProfilesLocked(t *testing.T) {
	instance := newTestInstance(t, true)
	profileID := ids.New()
	putLockedTestAIProfile(t, instance, profileID, "locked-key")

	result, err := instance.service.CollectAIProfiles(context.Background(), CollectAIProfilesRequest{RevealSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Profiles) != 1 {
		t.Fatalf("locked vault must not drop profiles: %+v", result.Profiles)
	}
	entry := result.Profiles[0]
	if entry.APIKeyState != CollectSecretLocked || entry.APIKey != nil || !entry.APIKeySet {
		t.Fatalf("locked outcome must be explicit: %+v", entry)
	}
	requireNoSecretLeak(t, result, "locked-key")
}

func TestCollectAIProfilesUnavailableWithoutVault(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	if _, err := instance.db.DB().ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)`,
		store.AIProfilesSettingKey, `{"version":1,"profiles":[{"id":"p1","name":"n","baseUrl":"","apiKey":"plain-key","model":"m","temperature":0,"contextWindow":0,"proxy":null,"stream":true}],"activeId":"p1"}`, 100); err != nil {
		t.Fatal(err)
	}

	result, err := instance.service.CollectAIProfiles(ctx, CollectAIProfilesRequest{RevealSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Profiles) != 1 {
		t.Fatalf("unavailable vault must not drop profiles: %+v", result.Profiles)
	}
	entry := result.Profiles[0]
	if entry.APIKeyState != CollectSecretUnavailable || entry.APIKey != nil || !entry.APIKeySet {
		t.Fatalf("unavailable outcome must be explicit: %+v", entry)
	}
	requireNoSecretLeak(t, result, "plain-key")
}

func TestCollectAIProfilesUnlockedReveal(t *testing.T) {
	instance := newTestInstance(t, true)
	profileID := ids.New()
	putTestAIProfile(t, instance, sealedTestAIProfile(t, instance, profileID, "revealed-key"), 100)

	result, err := instance.service.CollectAIProfiles(context.Background(), CollectAIProfilesRequest{RevealSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	entry := result.Profiles[0]
	if entry.APIKeyState != CollectSecretRevealed || entry.APIKey == nil || *entry.APIKey != "revealed-key" {
		t.Fatalf("unlocked reveal must carry the key: %+v", entry)
	}
}

func TestCollectAIProfilesDecryptFailureIsExplicit(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	if _, err := instance.db.DB().ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)`,
		store.AIProfilesSettingKey, `{"version":1,"profiles":[{"id":"p1","name":"n","baseUrl":"","apiKey":"enc:v1:dGFtcGVyZWQ","model":"m","temperature":0,"contextWindow":0,"proxy":null,"stream":true}],"activeId":"p1"}`, 100); err != nil {
		t.Fatal(err)
	}

	result, err := instance.service.CollectAIProfiles(ctx, CollectAIProfilesRequest{RevealSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	entry := result.Profiles[0]
	if entry.APIKeyState != CollectSecretError || entry.APIKey != nil || !entry.APIKeySet {
		t.Fatalf("decrypt failure must be explicit, not dropped: %+v", entry)
	}
}

func TestCollectAIProfilesKeyless(t *testing.T) {
	instance := newTestInstance(t, true)
	profileID := ids.New()
	putTestAIProfile(t, instance, aiProfileRecord{ID: profileID, Name: "无密钥档案", Model: "m", Stream: true}, 100)

	result, err := instance.service.CollectAIProfiles(context.Background(), CollectAIProfilesRequest{RevealSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	entry := result.Profiles[0]
	if entry.APIKeySet || entry.APIKey != nil {
		t.Fatalf("keyless profile must not carry key material: %+v", entry)
	}
	if entry.APIKeyState != CollectSecretRevealed {
		t.Fatalf("keyless profile state=%q, want revealed", entry.APIKeyState)
	}
}

func TestCollectTombstonesIncludesSettingsKinds(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	hostID := ids.New()
	profileID := ids.New()
	if err := instance.service.engine.syncTombstonePut(ctx, hostID, KindKnownHost, 200); err != nil {
		t.Fatal(err)
	}
	if err := instance.service.engine.syncTombstonePut(ctx, profileID, KindAIProfile, 300); err != nil {
		t.Fatal(err)
	}

	result, err := instance.service.CollectTombstones(ctx, CollectTombstonesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]CollectTombstone{}
	for _, tombstone := range result.Tombstones {
		byID[tombstone.ID] = tombstone
	}
	if entry := byID[hostID]; entry.TargetKind != KindKnownHost || entry.DeletedAt != 200 {
		t.Fatalf("known host tombstone=%+v", entry)
	}
	if entry := byID[profileID]; entry.TargetKind != KindAIProfile || entry.DeletedAt != 300 {
		t.Fatalf("ai profile tombstone=%+v", entry)
	}
}

func TestCollectSettingsCommandsOfflineAnonymous(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	if _, err := instance.db.DB().ExecContext(ctx,
		`INSERT INTO known_host(id, host, port, key_type, fingerprint, added_at) VALUES(?,?,?,?,?,?)`,
		ids.New(), "anon.example.com", 22, "ssh-ed25519", "SHA256:anon", 100); err != nil {
		t.Fatal(err)
	}

	dispatcher := ipc.NewDispatcher()
	if err := instance.service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	dispatch := func(command, args string) json.RawMessage {
		response := dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
		if !response.OK {
			t.Fatalf("%s rejected: %+v", command, response.Error)
		}
		return response.Data
	}

	var hosts CollectKnownHostsResult
	if err := json.Unmarshal(dispatch(CommandCollectKnownHosts, `{"args":{}}`), &hosts); err != nil {
		t.Fatal(err)
	}
	if len(hosts.KnownHosts) != 1 || hosts.KnownHosts[0].Host != "anon.example.com" {
		t.Fatalf("known hosts=%+v", hosts.KnownHosts)
	}

	var profiles CollectAIProfilesResult
	if err := json.Unmarshal(dispatch(CommandCollectAIProfiles, `{"args":{"revealSecrets":false}}`), &profiles); err != nil {
		t.Fatal(err)
	}
	if len(profiles.Profiles) != 0 {
		t.Fatalf("profiles=%+v", profiles.Profiles)
	}
}
