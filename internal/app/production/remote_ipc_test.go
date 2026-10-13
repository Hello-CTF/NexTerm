package production

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestRemoteCommandsRegistered(t *testing.T) {
	production := newUpdateTestProduction(t, false, nil)
	commands := production.Dispatcher.Commands()
	for _, command := range []string{
		"remote_healthz",
		"remote_fleet_devices",
		"remote_fleet_device_metrics",
		"remote_fleet_device_revoke",
		"remote_fleet_device_autostart",
		"remote_fleet_enroll_code",
		"remote_fleet_base_urls_get",
		"remote_fleet_base_urls_put",
		"remote_share_host_list",
		"remote_share_host_create",
		"remote_share_host_revoke",
		"remote_share_link_list",
		"remote_share_link_create",
		"remote_share_link_revoke",
		"remote_share_preview_list",
		"remote_share_preview_revoke",
		"remote_account_devices",
		"remote_account_device_revoke",
		"remote_account_admin_users",
		"remote_account_admin_create_user",
		"remote_account_admin_set_user_state",
		"remote_account_change_password",
		"sync_link_mfa",
		"sync_link_reset_password",
	} {
		found := false
		for _, registered := range commands {
			if registered == command {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("production is missing %s", command)
		}
	}
}

func TestRemoteCommandsServerModeRejected(t *testing.T) {
	production := newUpdateTestProduction(t, false, nil)
	response := dispatchHostKeyTest(t, production, "remote_fleet_devices", `null`)
	if response.OK {
		t.Fatal("server mode must reject remote proxy commands")
	}
	if response.Error == nil || response.Error.Code != "unsupported" {
		t.Fatalf("error = %+v, want unsupported", response.Error)
	}
}

func TestRemoteCommandsDesktopFlow(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := newRemoteTestAccounts(t, database)
	if err := accounts.createVerifiedUser(context.Background(), "alice", "alice-pw-123"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newRemoteTestMux(t, accounts))
	t.Cleanup(server.Close)

	production := newUpdateTestProduction(t, true, nil)

	response := dispatchHostKeyTest(t, production, "sync_link_set", `{"args":{"url":"`+server.URL+`","username":"alice","password":"alice-pw-123"}}`)
	var link struct {
		Connect string `json:"connect"`
	}
	requireStoreTestResponse(t, response, &link)
	if link.Connect != "connected" {
		t.Fatalf("link connect = %q, want connected", link.Connect)
	}

	response = dispatchHostKeyTest(t, production, "remote_fleet_devices", `null`)
	var devices struct {
		Devices []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"devices"`
	}
	requireStoreTestResponse(t, response, &devices)
	if len(devices.Devices) != 1 || devices.Devices[0].ID != "d-1" {
		t.Fatalf("devices = %+v", devices)
	}

	response = dispatchHostKeyTest(t, production, "remote_healthz", `null`)
	var version string
	requireStoreTestResponse(t, response, &version)
	if version != "9.9.9-test" {
		t.Fatalf("healthz version = %q", version)
	}

	response = dispatchHostKeyTest(t, production, "remote_fleet_device_autostart", `{"args":{"deviceId":"d-1","desired":true}}`)
	var autostart struct {
		OK               bool `json:"ok"`
		DesiredAutostart bool `json:"desired_autostart"`
	}
	requireStoreTestResponse(t, response, &autostart)
	if !autostart.OK || !autostart.DesiredAutostart {
		t.Fatalf("autostart = %+v", autostart)
	}

	response = dispatchHostKeyTest(t, production, "remote_fleet_device_revoke", `{"args":{"deviceId":""}}`)
	if response.OK || response.Error == nil || response.Error.Code != "bad_param" {
		t.Fatalf("empty deviceId = %+v, want bad_param", response)
	}

	// After the server revokes the session: the next remote call triggers a 401, and the proxy surface must re-login with the saved password successfully.
	if err := accounts.revokeAllSessions(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	response = dispatchHostKeyTest(t, production, "remote_fleet_devices", `null`)
	requireStoreTestResponse(t, response, &devices)
	if len(devices.Devices) != 1 {
		t.Fatalf("devices after re-login = %+v", devices)
	}
}

func TestRemoteLinkMFACommandFlow(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := newRemoteTestAccounts(t, database)
	if err := accounts.createVerifiedUser(context.Background(), "alice", "alice-pw-123"); err != nil {
		t.Fatal(err)
	}
	secret := accounts.bindTOTP(t, "alice", "alice-pw-123")
	server := httptest.NewServer(newRemoteTestMux(t, accounts))
	t.Cleanup(server.Close)

	production := newUpdateTestProduction(t, true, nil)

	response := dispatchHostKeyTest(t, production, "sync_link_set", `{"args":{"url":"`+server.URL+`","username":"alice","password":"alice-pw-123"}}`)
	var link struct {
		Connect   string `json:"connect"`
		MFATicket string `json:"mfaTicket"`
	}
	requireStoreTestResponse(t, response, &link)
	if link.Connect != "mfa_required" || link.MFATicket == "" {
		t.Fatalf("link = %+v, want mfa_required with ticket", link)
	}

	response = dispatchHostKeyTest(t, production, "sync_link_mfa", `{"args":{"code":"`+accounts.totpCode(t, secret)+`"}}`)
	requireStoreTestResponse(t, response, &link)
	if link.Connect != "connected" {
		t.Fatalf("link after mfa = %+v, want connected", link)
	}

	response = dispatchHostKeyTest(t, production, "remote_fleet_devices", `null`)
	var fleetDevices struct {
		Devices []struct {
			ID string `json:"id"`
		} `json:"devices"`
	}
	requireStoreTestResponse(t, response, &fleetDevices)
	if len(fleetDevices.Devices) != 1 {
		t.Fatalf("fleet devices after mfa = %+v", fleetDevices)
	}

	// reset_required branch: after an admin reset, re-login must enter reset_required, and the password change leads to connected.
	if err := accounts.adminResetUser(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	response = dispatchHostKeyTest(t, production, "sync_link_set", `{"args":{"url":"`+server.URL+`","username":"alice","password":"alice-pw-123"}}`)
	requireStoreTestResponse(t, response, &link)
	if link.Connect != "reset_required" {
		t.Fatalf("link after admin reset = %+v, want reset_required", link)
	}
	response = dispatchHostKeyTest(t, production, "sync_link_reset_password", `{"args":{"newPassword":"brand-new-pw-1"}}`)
	var reset struct {
		Connect     string `json:"connect"`
		RecoveryKey string `json:"recoveryKey"`
	}
	requireStoreTestResponse(t, response, &reset)
	if reset.Connect != "connected" || reset.RecoveryKey == "" || !strings.Contains(reset.RecoveryKey, "-") {
		t.Fatalf("reset = %+v, want connected with formatted recovery key", reset)
	}
}
