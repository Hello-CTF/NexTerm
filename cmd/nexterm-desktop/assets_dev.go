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
