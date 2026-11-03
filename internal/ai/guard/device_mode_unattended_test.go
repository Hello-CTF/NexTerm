package guard

import (
	"context"
	"testing"
)

func TestDeviceModeNeverRelaxesUnattended(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	manager, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	memory := NewMemory()
	memory.Add(KindWriteFS)
	ruling := Confirm(KindWriteFS, "write")
	for _, device := range []Mode{Silent, ReadWrite} {
		if err := manager.SetDeviceMode(context.Background(), "dev", device); err != nil {
			t.Fatal(err)
		}
		// The context marker keeps Unattended whatever the configured mode is.
		cases := map[string]struct {
			ctx    context.Context
			config Config
		}{
			"ctx":        {WithUnattended(context.Background()), Config{Mode: Unattended}},
			"ctx-config": {WithUnattended(context.Background()), Config{Mode: ReadWrite}},
		}
		for name, c := range cases {
			got, err := manager.WithDeviceMode(c.ctx, c.config, "dev")
			if err != nil {
				t.Fatal(err)
			}
			if got.Mode != Unattended {
				t.Fatalf("%s/%s: mode = %s", device, name, got.Mode)
			}
			if d := Decide(got, ruling, memory); d.Action != ActionDeny {
				t.Fatalf("%s/%s: decision = %+v", device, name, d)
			}
		}
	}
	// Unattended context with a non-unattended config is also held strict.
	got, err := manager.WithDeviceMode(WithUnattended(context.Background()), Config{Mode: ReadWrite}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != Unattended {
		t.Fatalf("ctx-only mode = %s", got.Mode)
	}
}

func TestDeviceModeStillOverridesInteractiveConfig(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	manager, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDeviceMode(context.Background(), "dev", Silent); err != nil {
		t.Fatal(err)
	}
	got, err := manager.WithDeviceMode(context.Background(), Config{Mode: ReadOnly}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != Silent {
		t.Fatalf("mode = %s", got.Mode)
	}
}

func TestDeviceModeOverridesGlobalUnattendedWithoutMarker(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	manager, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range []Mode{Silent, ReadWrite} {
		if err := manager.SetDeviceMode(context.Background(), "dev", device); err != nil {
			t.Fatal(err)
		}
		got, err := manager.WithDeviceMode(context.Background(), Config{Mode: Unattended}, "dev")
		if err != nil {
			t.Fatal(err)
		}
		if got.Mode != device {
			t.Fatalf("device %s: mode = %s", device, got.Mode)
		}
	}
}
