//go:build !production

package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

func newDesktopAssets(root string) (http.Handler, error) {
	if root == "" {
		root = "dist"
	}
	info, err := os.Stat(filepath.Join(root, "index.html"))
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("desktop web assets: index.html not found under %s", root)
	}
	return &desktopAssets{root: root}, nil
}

// desktopBrandIcon reads the brand icon shipped in the web assets so the
// system tray can reuse it. A missing icon is not fatal.
func desktopBrandIcon(root string) []byte {
	if root == "" {
		root = "dist"
	}
	data, err := os.ReadFile(filepath.Join(root, "brand", "icon.ico"))
	if err != nil {
		return nil
	}
	return data
}
