package production

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	p2pclient "github.com/Hello-CTF/NexTerm/internal/p2p/client"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func dispatchDevicePath(t *testing.T, dispatcher *ipc.Dispatcher, command string, args any, out any) *ipc.Error {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: encoded}, ipc.Environment{})
	if !response.OK {
		return response.Error
	}
	if out != nil {
		if err := json.Unmarshal(response.Data, out); err != nil {
			t.Fatal(err)
		}
	}
	return nil
}

// The mode lives in the device's kind=device asset row: set merges it into the row the asset tree uses (keeping
// its group and deviceId), or creates an ungrouped row; get reads it back; a bad mode is refused.
func TestDeviceConnectionModeStorage(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	group, err := database.GroupCreate(ctx, store.GroupInput{Name: "racks"})
	if err != nil {
		t.Fatal(err)
	}
	grouped, err := database.AssetCreate(ctx, store.AssetInput{Kind: store.AssetKindDevice, Name: "box", GroupID: &group.ID, OptionsJSON: `{"deviceId":"dev-1"}`})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := ipc.NewDispatcher()
	if err := registerDevicePathCommands(dispatcher, &devicePathService{modes: deviceModeStore{store: database}}); err != nil {
		t.Fatal(err)
	}
	var mode deviceModeDTO
	if err := dispatchDevicePath(t, dispatcher, "fleet_device_connection_mode_get", map[string]string{"deviceId": "dev-1"}, &mode); err != nil || mode.Mode != "auto" {
		t.Fatalf("default mode = %+v %v", mode, err)
	}
	for _, want := range []string{"relay", "direct", "auto"} {
		if err := dispatchDevicePath(t, dispatcher, "fleet_device_connection_mode_set", map[string]string{"deviceId": "dev-1", "mode": want}, &mode); err != nil || mode.Mode != want {
			t.Fatalf("set %s = %+v %v", want, mode, err)
		}
		if err := dispatchDevicePath(t, dispatcher, "fleet_device_connection_mode_get", map[string]string{"deviceId": "dev-1"}, &mode); err != nil || mode.Mode != want {
			t.Fatalf("get after set %s = %+v %v", want, mode, err)
		}
	}
	row, err := database.AssetGet(ctx, grouped.ID)
	if err != nil {
		t.Fatal(err)
	}
	var options map[string]any
	_ = json.Unmarshal([]byte(row.OptionsJSON), &options)
	if row.GroupID == nil || *row.GroupID != group.ID || options["deviceId"] != "dev-1" || options[deviceConnectionModeOption] != "auto" {
		t.Fatalf("device row after set = group %v options %v", row.GroupID, options)
	}

	if err := dispatchDevicePath(t, dispatcher, "fleet_device_connection_mode_set", map[string]string{"deviceId": "dev-2", "mode": "relay"}, &mode); err != nil {
		t.Fatal(err)
	}
	rows, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, candidate := range rows {
		var parsed map[string]any
		_ = json.Unmarshal([]byte(candidate.OptionsJSON), &parsed)
		if candidate.Kind == store.AssetKindDevice && parsed["deviceId"] == "dev-2" {
			created++
			if candidate.GroupID != nil || parsed[deviceConnectionModeOption] != "relay" {
				t.Fatalf("created row = %+v", candidate)
			}
		}
	}
	if created != 1 {
		t.Fatalf("rows created for dev-2 = %d", created)
	}

	for _, bad := range []map[string]string{{"deviceId": "dev-1", "mode": "fast"}, {"deviceId": "", "mode": "relay"}} {
		if err := dispatchDevicePath(t, dispatcher, "fleet_device_connection_mode_set", bad, nil); err == nil || err.Code != ipc.CodeBadParam {
			t.Fatalf("set %v = %v, want bad_param", bad, err)
		}
	}
}

// Without a path manager (the browser-mode server) every device reports the relay with browser_or_share.
func TestDevicePathStatusWithoutManager(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerDevicePathCommands(dispatcher, &devicePathService{modes: deviceModeStore{store: database}}); err != nil {
		t.Fatal(err)
	}
	var status p2pclient.DevicePathStatus
	if err := dispatchDevicePath(t, dispatcher, "fleet_device_path_status", map[string]string{"deviceId": "dev-1"}, &status); err != nil {
		t.Fatal(err)
	}
	if status.Path != p2pclient.PathRelay || status.Reason != p2pclient.ReasonBrowserOrShare || status.Mode != "auto" || status.DeviceID != "dev-1" {
		t.Fatalf("status = %+v", status)
	}
	var diag p2pclient.DeviceP2PDiagnostics
	if err := dispatchDevicePath(t, dispatcher, "fleet_device_p2p_diagnostics", map[string]string{"deviceId": "dev-1"}, &diag); err != nil {
		t.Fatal(err)
	}
	if diag.Status != status || diag.RecentSwitches == nil {
		t.Fatalf("diagnostics = %+v", diag)
	}
}
