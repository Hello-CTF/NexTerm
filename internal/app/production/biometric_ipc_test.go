package production

import (
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/biometric"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

type fakeBiometric struct {
	status         biometric.Status
	currentErr     error
	unlockPassword string
	unlockErr      error
	enabled        string
	enableErr      error
	disabled       bool
	disableErr     error
}

func (f *fakeBiometric) Current() (biometric.Status, error) { return f.status, f.currentErr }
func (f *fakeBiometric) Unlock() (string, error)            { return f.unlockPassword, f.unlockErr }
func (f *fakeBiometric) Enable(password string) error {
	f.enabled = password
	return f.enableErr
}
func (f *fakeBiometric) Disable() error {
	f.disabled = true
	return f.disableErr
}

type fakeVaultState struct {
	unlocked bool
}

func (f fakeVaultState) Status() vault.Status {
	return vault.Status{Unlocked: f.unlocked}
}

func newBiometricTestDispatcher(t *testing.T, backend biometricBackend) *ipc.Dispatcher {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := registerBiometricCommands(dispatcher, backend, fakeVaultState{unlocked: true}); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func TestBiometricStatusMapsBackend(t *testing.T) {
	backend := &fakeBiometric{status: biometric.Status{Available: true, CanEvaluate: true, HasMaterial: true}}
	dispatcher := newBiometricTestDispatcher(t, backend)
	response := dispatchStoreTest(dispatcher, "biometric_status", `null`)
	var status biometric.Status
	requireStoreTestResponse(t, response, &status)
	if !status.Available || !status.CanEvaluate || !status.HasMaterial {
		t.Fatalf("status = %+v", status)
	}
}

func TestBiometricUnlockReturnsMaterialOnlyOnSuccess(t *testing.T) {
	backend := &fakeBiometric{unlockPassword: "master-pw"}
	dispatcher := newBiometricTestDispatcher(t, backend)
	response := dispatchStoreTest(dispatcher, "biometric_unlock", `null`)
	var result biometricUnlockResult
	requireStoreTestResponse(t, response, &result)
	if !result.OK || result.Password != "master-pw" || result.Message != "" {
		t.Fatalf("unlock = %+v", result)
	}

	backend.unlockErr = errors.New("已取消面容验证")
	response = dispatchStoreTest(dispatcher, "biometric_unlock", `null`)
	var canceled biometricUnlockResult
	requireStoreTestResponse(t, response, &canceled)
	if canceled.OK || canceled.Password != "" || canceled.Message != "已取消面容验证" {
		t.Fatalf("canceled unlock = %+v", canceled)
	}
}

func TestBiometricEnableValidatesAndDelegates(t *testing.T) {
	backend := &fakeBiometric{}
	dispatcher := newBiometricTestDispatcher(t, backend)
	response := dispatchStoreTest(dispatcher, "biometric_enable", `{"password":""}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("empty enable = %+v", response)
	}
	response = dispatchStoreTest(dispatcher, "biometric_enable", `{"password":"master-pw"}`)
	requireStoreTestResponse(t, response, &struct{}{})
	if backend.enabled != "master-pw" {
		t.Fatalf("enabled = %q", backend.enabled)
	}
	response = dispatchStoreTest(dispatcher, "biometric_disable", `null`)
	requireStoreTestResponse(t, response, &struct{}{})
	if !backend.disabled {
		t.Fatal("disable not delegated")
	}
}

func TestBiometricEnableRequiresUnlockedVault(t *testing.T) {
	backend := &fakeBiometric{}
	dispatcher := ipc.NewDispatcher()
	if err := registerBiometricCommands(dispatcher, backend, fakeVaultState{unlocked: false}); err != nil {
		t.Fatal(err)
	}
	response := dispatchStoreTest(dispatcher, "biometric_enable", `{"password":"master-pw"}`)
	if response.OK || response.Error == nil {
		t.Fatalf("locked enable = %+v", response)
	}
	if backend.enabled != "" {
		t.Fatalf("backend received material while locked: %q", backend.enabled)
	}

	dispatcher = ipc.NewDispatcher()
	if err := registerBiometricCommands(dispatcher, backend, nil); err != nil {
		t.Fatal(err)
	}
	response = dispatchStoreTest(dispatcher, "biometric_enable", `{"password":"master-pw"}`)
	if response.OK || response.Error == nil {
		t.Fatalf("nil-vault enable = %+v", response)
	}
}

func TestBiometricPlatformStubAnswersStatus(t *testing.T) {
	dispatcher := newBiometricTestDispatcher(t, platformBiometric{})
	response := dispatchStoreTest(dispatcher, "biometric_status", `null`)
	var status biometric.Status
	requireStoreTestResponse(t, response, &status)
	if status.Available {
		t.Fatalf("non-ios status = %+v, want unavailable", status)
	}
}
