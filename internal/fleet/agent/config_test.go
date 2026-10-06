package agent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testConfig() *Config {
	return &Config{
		Version:           configVersion,
		DeviceID:          "01J5FLEET0000000000000000",
		Secret:            "super-secret-value",
		Name:              "test-device",
		Platform:          runtime.GOOS,
		AppVersion:        "dev",
		BaseURLs:          []BaseURLEntry{{URL: "https://fleet.example.com"}},
		MetricsIntervalMS: 60000,
		DesiredAutostart:  true,
		TerminalEnabled:   true,
		EnrolledAt:        1700000000000,
	}
}

func TestStoreRoundTripKeepsPrivatePermissions(t *testing.T) {
	store := StoreAt(t.TempDir())
	if _, err := store.Load(); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("Load on empty store = %v, want ErrNotEnrolled", err)
	}
	config := testConfig()
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeviceID != config.DeviceID || loaded.Secret != config.Secret || loaded.BaseURLs[0].URL != config.BaseURLs[0].URL {
		t.Fatalf("loaded config = %+v", loaded)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("config dir mode = %v, want 0700", dirInfo.Mode().Perm())
	}
}

func TestStoreSaveRejectsUnboundCredentials(t *testing.T) {
	store := StoreAt(t.TempDir())
	config := testConfig()
	config.Secret = ""
	if err := store.Save(config); err == nil {
		t.Fatal("Save without secret must fail")
	}
	config = testConfig()
	config.DeviceID = ""
	if err := store.Save(config); err == nil {
		t.Fatal("Save without device ID must fail")
	}
}

func TestStoreLoadRejectsTamperedConfig(t *testing.T) {
	store := StoreAt(t.TempDir())
	if err := store.Save(testConfig()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), []byte(`{"version":99,"device_id":"x","secret":"y","base_urls":[{"url":"https://a.example.com"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load of unsupported config version must fail")
	}
	if err := os.WriteFile(store.Path(), []byte(`{"version":1,"device_id":"x","secret":"y","base_urls":[{"url":"ftp://a.example.com"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load of non-HTTP base URL must fail")
	}
	if err := os.WriteFile(store.Path(), []byte(`{"version":1,"device_id":"x","secret":"y","base_urls":[{"url":"http://203.0.113.10"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load of public cleartext HTTP base URL without insecure opt-in must fail")
	}
	if err := os.WriteFile(store.Path(), []byte(`{"version":1,"device_id":"x","secret":"y","base_urls":[{"url":"http://203.0.113.10","insecure":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load with explicit insecure public HTTP must pass: %v", err)
	}
}

func TestStoreRemoveIsIdempotent(t *testing.T) {
	store := StoreAt(t.TempDir())
	if err := store.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(testConfig()); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("Load after Remove = %v, want ErrNotEnrolled", err)
	}
}
