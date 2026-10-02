//go:build production

package main

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:dist
var desktopDist embed.FS

type embeddedDesktopAssets struct {
	files    fs.FS
	fallback http.Handler
	index    fs.FileInfo
}

func newDesktopAssets(_ string) (http.Handler, error) {
	files, err := fs.Sub(desktopDist, "dist")
	if err != nil {
		return nil, fmt.Errorf("desktop web assets: open embedded dist: %w", err)
	}
	info, err := fs.Stat(files, "index.html")
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("desktop web assets: embedded index.html is missing: %w", err)
	}
	return &embeddedDesktopAssets{files: files, fallback: application.AssetFileServerFS(files), index: info}, nil
}

func (s *embeddedDesktopAssets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	relative, err := desktopAssetPath(r.URL.EscapedPath())
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	isIndex := relative == "" || relative == "index.html"
	if !isIndex {
		if info, statErr := fs.Stat(s.files, relative); statErr == nil && info.Mode().IsRegular() {
			if strings.HasPrefix(filepath.ToSlash(relative), "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			s.fallback.ServeHTTP(w, r)
			return
		}
	}
	data, err := fs.ReadFile(s.files, "index.html")
	if err != nil {
		http.Error(w, "embedded index.html is not readable", http.StatusNotFound)
		return
	}
	html := injectDesktopTransportMarker(string(data))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, s.index.Name(), s.index.ModTime(), bytes.NewReader([]byte(html)))
}
