package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The Linux package names its desktop entry after wails.json's product
// identifier; the GTK application id must stay equal to it.
func TestDesktopAppIDMatchesProductIdentifier(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "wails.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Info struct {
			ProductIdentifier string `json:"productIdentifier"`
		} `json:"info"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Info.ProductIdentifier != desktopAppID {
		t.Fatalf("wails.json productIdentifier = %q, desktopAppID = %q", config.Info.ProductIdentifier, desktopAppID)
	}
}
