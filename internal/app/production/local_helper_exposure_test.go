package production

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/fleet/exposure"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/localhelper"
	"github.com/Hello-CTF/NexTerm/internal/store"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

type exposureLink struct {
	link syncservice.Link
	err  error
}

func (l exposureLink) LinkGet(context.Context) (syncservice.Link, error) { return l.link, l.err }
func (exposureLink) Remote() *syncservice.RemoteProxy                    { return nil }

func newExposureHostFixture(t *testing.T) (*localHelperExposureHost, *store.Store, *vault.Vault) {
	t.Helper()
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, "exposure-password"); err != nil {
		t.Fatal(err)
	}
	helper := localhelper.New(localhelper.Config{StateDir: filepath.Join(t.TempDir(), "supervisor")})
	host := &localHelperExposureHost{settings: database, secrets: credentialVault, helper: helper}
	return host, database, credentialVault
}

func TestLocalHelperExposureIsOffByDefault(t *testing.T) {
	host, _, _ := newExposureHostFixture(t)
	if enabled, err := host.Enabled(t.Context()); err != nil || enabled {
		t.Fatalf("Enabled = %v %v, want off", enabled, err)
	}
	if err := host.SetEnabled(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if enabled, err := host.Enabled(t.Context()); err != nil || !enabled {
		t.Fatalf("Enabled after turning it on = %v %v", enabled, err)
	}
}

func TestLocalHelperExposureSealsEnrollment(t *testing.T) {
	host, database, credentialVault := newExposureHostFixture(t)
	ctx := t.Context()
	if enrollment, err := host.LoadEnrollment(ctx); err != nil || enrollment != nil {
		t.Fatalf("LoadEnrollment before enrolling = %+v %v, want none", enrollment, err)
	}
	enrollment := &exposure.Enrollment{
		Account: exposure.Account{URL: "https://nexterm.example.com", Username: "alice"},
		Config: agent.Config{
			Version: agent.ConfigVersion, DeviceID: "desk-1", Secret: "device-secret-value",
			BaseURLs: []agent.BaseURLEntry{{URL: "https://nexterm.example.com"}},
		},
	}
	if err := host.SaveEnrollment(ctx, enrollment); err != nil {
		t.Fatal(err)
	}
	stored, found, err := database.SettingGet(ctx, localHelperExposureEnrollmentKey)
	if err != nil || !found {
		t.Fatalf("stored enrollment = %v %v", found, err)
	}
	if !strings.HasPrefix(stored, store.SecretEnvelopePrefix) || strings.Contains(stored, "device-secret-value") {
		t.Fatal("device credentials were stored outside a vault envelope")
	}
	loaded, err := host.LoadEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(enrollment)
	got, _ := json.Marshal(loaded)
	if string(got) != string(want) {
		t.Fatalf("loaded enrollment = %s, want %s", got, want)
	}

	credentialVault.Lock()
	if _, err := host.LoadEnrollment(ctx); !errors.Is(err, exposure.ErrLocked) {
		t.Fatalf("LoadEnrollment with a locked vault = %v, want ErrLocked", err)
	}
	if err := host.RemoveEnrollment(ctx); err != nil {
		t.Fatal(err)
	}
	if enrollment, err := host.LoadEnrollment(ctx); err != nil || enrollment != nil {
		t.Fatalf("LoadEnrollment after removal = %+v %v, want none", enrollment, err)
	}
}

func TestLocalHelperExposureAccount(t *testing.T) {
	host, _, _ := newExposureHostFixture(t)
	ctx := t.Context()
	host.account = exposureLink{link: syncservice.Link{URL: "https://nexterm.example.com", Username: "alice"}}
	if _, err := host.Account(ctx); !errors.Is(err, exposure.ErrNoAccount) {
		t.Fatalf("Account without a saved password = %v, want ErrNoAccount", err)
	}
	host.account = exposureLink{err: ipc.NewError(ipc.CodeVaultLocked, "locked")}
	if _, err := host.Account(ctx); !errors.Is(err, exposure.ErrLocked) {
		t.Fatalf("Account with a locked vault = %v, want ErrLocked", err)
	}
	host.account = exposureLink{link: syncservice.Link{URL: "https://nexterm.example.com", Username: "alice", HasPassword: true, Insecure: true}}
	account, err := host.Account(ctx)
	if err != nil || account != (exposure.Account{URL: "https://nexterm.example.com", Username: "alice", Insecure: true}) {
		t.Fatalf("Account = %+v %v", account, err)
	}
}

// Regular local terminals block exposure, as does a desktop without a local helper.
func TestLocalHelperExposureFollowsTerminalMode(t *testing.T) {
	host, database, _ := newExposureHostFixture(t)
	ctx := t.Context()
	if reason := host.Blocked(ctx); reason != "" {
		t.Fatalf("Blocked with durable local terminals = %q", reason)
	}
	if err := database.SettingSet(ctx, LocalTerminalModeSettingKey, "regular"); err != nil {
		t.Fatal(err)
	}
	if reason := host.Blocked(ctx); reason == "" {
		t.Fatal("regular local terminals did not block exposure")
	}
	if reason := localHelperExposureBlocked(ctx, nil, database); reason == "" {
		t.Fatal("exposure without a local helper was not blocked")
	}
}

func TestLocalHelperExposureCommandsWithoutController(t *testing.T) {
	dispatcher := ipc.NewDispatcher()
	if err := registerLocalHelperExposureCommands(dispatcher, nil); err != nil {
		t.Fatal(err)
	}
	get := dispatcher.Dispatch(t.Context(), ipc.Request{Command: "local_helper_expose_get"}, ipc.Environment{})
	var status localHelperExposureDTO
	if !get.OK || json.Unmarshal(get.Data, &status) != nil || status.State != string(exposure.StateUnavailable) || status.Enabled {
		t.Fatalf("expose_get = %+v (%+v), want unavailable", get, status)
	}
	set := dispatcher.Dispatch(t.Context(), ipc.Request{Command: "local_helper_expose_set", Args: []byte(`{"enabled":true}`)}, ipc.Environment{})
	if set.OK || set.Error == nil || set.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("expose_set = %+v, want unsupported", set)
	}
}
