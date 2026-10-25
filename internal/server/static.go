package server

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const TransportMarker = `<script>window.__NEXTERM_TRANSPORT__="web";</script>`

type staticFiles struct {
	root  string
	cache *staticCache
}

func NewStaticHandler(root string) http.Handler {
	return &staticFiles{root: root, cache: newStaticCache()}
}

func (s *staticFiles) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	relative, err := safeStaticPath(r.URL.EscapedPath())
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	candidate := filepath.Join(s.root, relative)
	isIndex := relative == "" || relative == "index.html"
	if !isIndex {
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			s.serveFile(w, r, candidate, relative, info)
			return
		}
	}
	index := filepath.Join(s.root, "index.html")
	info, err := os.Stat(index)
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "index.html not found; check the web root", http.StatusNotFound)
		return
	}
	s.serveFile(w, r, index, "index.html", info)
}

func (s *staticFiles) serveFile(w http.ResponseWriter, r *http.Request, path, relative string, info os.FileInfo) {
	contentType := staticContentType(path)
	isHTML := strings.HasPrefix(contentType, "text/html")
	w.Header().Set("Content-Type", contentType)
	if isHTML {
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.HasPrefix(filepath.ToSlash(relative), "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	compressible := compressibleType(contentType) && info.Size() >= compressMinBytes

	var entry *staticEntry
	if isHTML || info.Size() <= staticCacheMaxFile {
		entry = s.cache.load(path, info, isHTML)
	}
	if entry == nil {
		if isHTML {
			http.Error(w, "static file is not readable", http.StatusNotFound)
			return
		}
		s.serveOpened(w, r, path, info)
		return
	}
	if compressible {
		addVary(w.Header(), "Accept-Encoding")
	}
	if entry.gzip != nil && r.Header.Get("Range") == "" && acceptsGzip(r.Header) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("ETag", entry.gzipETag)
		http.ServeContent(w, r, filepath.Base(path), info.ModTime(), bytes.NewReader(entry.gzip))
		return
	}
	w.Header().Set("ETag", entry.etag)
	if isHTML {
		http.ServeContent(w, r, filepath.Base(path), info.ModTime(), bytes.NewReader(entry.raw))
		return
	}
	s.serveOpened(w, r, path, info)
}

func (s *staticFiles) serveOpened(w http.ResponseWriter, r *http.Request, path string, info os.FileInfo) {
	file, err := os.Open(path)
	if err != nil {
		w.Header().Del("ETag")
		http.Error(w, "static file is not readable", http.StatusNotFound)
		return
	}
	defer file.Close()
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
}

func safeStaticPath(escapedPath string) (string, error) {
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
	return filepath.FromSlash(decoded), nil
}

func InjectTransportMarker(html string) string {
	if index := strings.Index(html, "</head>"); index >= 0 {
		return html[:index] + TransportMarker + html[index:]
	}
	return TransportMarker + html
}

func staticContentType(path string) string {
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
