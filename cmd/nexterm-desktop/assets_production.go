//go:build production

package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:dist
var desktopDist embed.FS

type embeddedDesktopAssets struct {
	files     fs.FS
	fallback  http.Handler
	index     fs.FileInfo
	indexHTML []byte
	indexETag string
}

func newDesktopAssets(_ string) (http.Handler, error) {
	files, err := fs.Sub(desktopDist, "dist")
	if err != nil {
		return nil, fmt.Errorf("desktop web assets: open embedded dist: %w", err)
	}
	return newEmbeddedDesktopAssets(files)
}

func newEmbeddedDesktopAssets(files fs.FS) (*embeddedDesktopAssets, error) {
	info, err := fs.Stat(files, "index.html")
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("desktop web assets: embedded index.html is missing: %w", err)
	}
	data, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return nil, fmt.Errorf("desktop web assets: embedded index.html is not readable: %w", err)
	}
	html := injectDesktopTransportMarker(string(data))
	return &embeddedDesktopAssets{
		files: files, fallback: application.AssetFileServerFS(files), index: info,
		indexHTML: []byte(html), indexETag: desktopContentETag([]byte(html)),
	}, nil
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
			data, readErr := fs.ReadFile(s.files, relative)
			if readErr != nil {
				http.Error(w, "embedded asset is not readable", http.StatusInternalServerError)
				return
			}
			etag := desktopContentETag(data)
			w.Header().Set("ETag", etag)
			if strings.HasPrefix(relative, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			if desktopNotModified(r.Header.Get("If-None-Match"), etag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			s.fallback.ServeHTTP(w, r)
			return
		}
	}
	w.Header().Set("ETag", s.indexETag)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if desktopNotModified(r.Header.Get("If-None-Match"), s.indexETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	http.ServeContent(w, r, s.index.Name(), s.index.ModTime(), bytes.NewReader(s.indexHTML))
}

func desktopContentETag(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf(`"%x"`, digest)
}

func desktopNotModified(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}
