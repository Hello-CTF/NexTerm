package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const desktopTransportMarker = `<script>window.__NEXTERM_TRANSPORT__="desktop";</script>`

type desktopAssets struct {
	root string
}

func (s *desktopAssets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	candidate := filepath.Join(s.root, filepath.FromSlash(relative))
	isIndex := relative == "" || relative == "index.html"
	if !isIndex {
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			s.serveFile(w, r, candidate, info)
			return
		}
	}
	index := filepath.Join(s.root, "index.html")
	info, err := os.Stat(index)
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "index.html not found; check the desktop web root", http.StatusNotFound)
		return
	}
	s.serveFile(w, r, index, info)
}

func (s *desktopAssets) serveFile(w http.ResponseWriter, r *http.Request, path string, info os.FileInfo) {
	contentType := desktopAssetContentType(path)
	if strings.HasPrefix(contentType, "text/html") {
		data, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "static file is not readable", http.StatusNotFound)
			return
		}
		html := injectDesktopTransportMarker(string(data))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, filepath.Base(path), info.ModTime(), bytes.NewReader([]byte(html)))
		return
	}

	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "static file is not readable", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", contentType)
	relative, _ := filepath.Rel(s.root, path)
	if strings.HasPrefix(filepath.ToSlash(relative), "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
}

func desktopAssetPath(escapedPath string) (string, error) {
	trimmed := strings.TrimLeft(escapedPath, "/")
	decoded, err := url.PathUnescape(trimmed)
	if err != nil {
		return "", err
	}
	if decoded == "" {
		return "", nil
	}
	if strings.ContainsRune(decoded, '\x00') || strings.Contains(decoded, `\`) || strings.HasPrefix(decoded, "/") || filepath.IsAbs(decoded) || filepath.VolumeName(decoded) != "" {
		return "", fmt.Errorf("path escapes web root")
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == ".." {
			return "", fmt.Errorf("path escapes web root")
		}
	}
	return decoded, nil
}

func injectDesktopTransportMarker(html string) string {
	if index := strings.Index(html, "<head>"); index >= 0 {
		index += len("<head>")
		return html[:index] + desktopTransportMarker + html[index:]
	}
	return desktopTransportMarker + html
}

func desktopAssetContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	case ".wasm":
		return "application/wasm"
	case ".txt":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
