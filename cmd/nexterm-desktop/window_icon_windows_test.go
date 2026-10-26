//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/w32"
)

func TestCreateWindowIcons(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "public", "brand", "icon.ico"))
	if err != nil {
		t.Fatal(err)
	}
	icons, err := createWindowIcons(data)
	if icons.large != 0 {
		defer w32.DestroyIcon(icons.large)
	}
	if icons.small != 0 {
		defer w32.DestroyIcon(icons.small)
	}
	if err != nil {
		t.Fatalf("createWindowIcons: %v", err)
	}
	if icons.large == 0 || icons.small == 0 {
		t.Fatalf("large=%v small=%v, want two native icons", icons.large, icons.small)
	}
}

func TestCreateWindowIconsRejectsInvalidData(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("not an icon")} {
		icons, err := createWindowIcons(data)
		if icons.large != 0 {
			w32.DestroyIcon(icons.large)
		}
		if icons.small != 0 {
			w32.DestroyIcon(icons.small)
		}
		if err == nil {
			t.Fatalf("createWindowIcons(%q) succeeded, want error", data)
		}
	}
}
