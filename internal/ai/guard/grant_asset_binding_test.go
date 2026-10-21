package guard

import (
	"context"
	"errors"
	"testing"
)

// TestDeviceModeBindsToRealTargetAsset pins the mode binding: a per-device
// permission mode applies to exactly the asset it was set for, resolved by
// identity, and a grants evaluation for that same asset honors the bound
// mode while other assets keep the global mode.
func TestDeviceModeBindsToRealTargetAsset(t *testing.T) {
	settings := &memorySettings{values: map[string]string{}}
	manager, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDeviceMode(context.Background(), "asset-1", Unattended); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDeviceMode(context.Background(), "asset-2", ReadOnly); err != nil {
		t.Fatal(err)
	}

	bound, err := manager.WithDeviceMode(context.Background(), Config{Mode: ReadWrite}, "asset-1")
	if err != nil {
		t.Fatal(err)
	}
	if bound.Mode != Unattended {
		t.Fatalf("asset-1 bound mode = %q, want unattended", bound.Mode)
	}
	other, err := manager.WithDeviceMode(context.Background(), Config{Mode: ReadWrite}, "asset-2")
	if err != nil {
		t.Fatal(err)
	}
	if other.Mode != ReadOnly {
		t.Fatalf("asset-2 bound mode = %q, want read_only", other.Mode)
	}
	unbound, err := manager.WithDeviceMode(context.Background(), Config{Mode: ReadWrite}, "asset-3")
	if err != nil {
		t.Fatal(err)
	}
	if unbound.Mode != ReadWrite {
		t.Fatalf("asset-3 mode = %q, want the global mode", unbound.Mode)
	}

	// The bound mode governs the grants decision for exactly that asset:
	// unattended on asset-1 denies a confirmation it would otherwise ask,
	// read_only on asset-2 denies outright, and asset-3 still asks.
	grants, err := NewGrants(context.Background(), settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindWriteFS, "写入文件")
	if decision := grants.EvaluateResource(context.Background(), bound, ruling, NewMemory(), "asset-1", "write_file", "run-1", Resource{Action: "write_file", Path: "/srv/app/main.go"}); decision.Action != ActionDeny {
		t.Fatalf("unattended asset-1 decision = %+v", decision)
	}
	if decision := grants.EvaluateResource(context.Background(), other, ruling, NewMemory(), "asset-2", "write_file", "run-1", Resource{Action: "write_file", Path: "/srv/app/main.go"}); decision.Action != ActionDeny {
		t.Fatalf("read_only asset-2 decision = %+v", decision)
	}
	if decision := grants.EvaluateResource(context.Background(), unbound, ruling, NewMemory(), "asset-3", "write_file", "run-1", Resource{Action: "write_file", Path: "/srv/app/main.go"}); decision.Action != ActionAsk {
		t.Fatalf("global-mode asset-3 decision = %+v", decision)
	}
}

// TestDeviceModeSurvivesReloadBoundToSameAsset pins persistence: the mode is
// stored against the real asset identity and reloads against it, never
// drifting onto another asset.
func TestDeviceModeSurvivesReloadBoundToSameAsset(t *testing.T) {
	settings := &memorySettings{values: map[string]string{}}
	manager, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDeviceMode(context.Background(), "asset-1", Silent); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	mode, ok, err := manager.DeviceMode(context.Background(), "asset-1")
	if err != nil || !ok || mode != Silent {
		t.Fatalf("reloaded mode = %q ok=%v err=%v", mode, ok, err)
	}
	if _, ok, err := manager.DeviceMode(context.Background(), "asset-2"); err != nil || ok {
		t.Fatalf("foreign asset inherited the mode: ok=%v err=%v", ok, err)
	}
	bound, err := manager.WithDeviceMode(context.Background(), Config{Mode: ReadWrite}, "asset-1")
	if err != nil || bound.Mode != Silent {
		t.Fatalf("reloaded bound config = %+v err=%v", bound, err)
	}
}

// TestDeviceModeClearingBindsToRealTargetAsset pins the clearing path: an
// empty mode removes the binding for exactly that asset and persists the
// removal.
func TestDeviceModeClearingBindsToRealTargetAsset(t *testing.T) {
	settings := &memorySettings{values: map[string]string{}}
	manager, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDeviceMode(context.Background(), "asset-1", Silent); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDeviceMode(context.Background(), "asset-1", ""); err != nil {
		t.Fatal(err)
	}
	bound, err := manager.WithDeviceMode(context.Background(), Config{Mode: ReadWrite}, "asset-1")
	if err != nil {
		t.Fatal(err)
	}
	if bound.Mode != ReadWrite {
		t.Fatalf("cleared asset-1 mode = %q, want the global mode", bound.Mode)
	}
	reloaded, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := reloaded.DeviceMode(context.Background(), "asset-1"); err != nil || ok {
		t.Fatalf("cleared mode resurrected after reload: ok=%v err=%v", ok, err)
	}
}

// TestSetDeviceModeRejectsInvalidMode pins the mode validation boundary.
func TestSetDeviceModeRejectsInvalidMode(t *testing.T) {
	settings := &memorySettings{values: map[string]string{}}
	manager, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDeviceMode(context.Background(), "asset-1", "yolo"); !errors.Is(err, ErrDeviceModeInvalid) {
		t.Fatalf("invalid mode error = %v", err)
	}
	if err := manager.SetDeviceMode(context.Background(), "  ", Silent); !errors.Is(err, ErrDeviceModeDeviceRequired) {
		t.Fatalf("empty device error = %v", err)
	}
}
