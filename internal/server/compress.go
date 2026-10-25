package server

import (
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/compress/gzip"
)

// compressMinBytes is the smallest response worth compressing: below it the gzip header and the CPU cost outweigh the saving.
const compressMinBytes = 1024

// compressibleType reports whether a Content-Type names a text-like payload that deflate shrinks well. Images, fonts
// (woff/woff2), archives and other already-compressed formats are deliberately absent.
func compressibleType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch {
	case strings.HasPrefix(mediaType, "text/"):
		return true
	case strings.HasSuffix(mediaType, "+json"), strings.HasSuffix(mediaType, "+xml"):
		return true
	}
	switch mediaType {
	case "application/json", "application/javascript", "application/xml", "application/wasm", "image/svg+xml", "image/x-icon", "font/ttf", "font/otf":
		return true
	}
	return false
}

// acceptsGzip parses Accept-Encoding and reports whether gzip (or *) is acceptable with a non-zero quality.
func acceptsGzip(header http.Header) bool {
	value := header.Get("Accept-Encoding")
	if value == "" {
		return false
	}
	accepted := false
	for _, part := range strings.Split(value, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "gzip" && name != "x-gzip" && name != "*" {
			continue
		}
		quality := 1.0
		if _, q, ok := strings.Cut(params, "q="); ok {
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(q), 64); err == nil {
				quality = parsed
			}
		}
		if name == "*" {
			if quality > 0 && !accepted {
				accepted = true
			}
			continue
		}
		return quality > 0
	}
	return accepted
}

func addVary(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		for _, token := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) || strings.TrimSpace(token) == "*" {
				return
			}
		}
	}
	header.Add("Vary", value)
}

var gzipPool = sync.Pool{New: func() any {
	writer, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
	return writer
}}

// compressResponses gzips dynamic responses (RPC JSON, staged file downloads) when the client accepts it. It never
// touches range requests, responses that are already encoded, tiny bodies, non-text payloads or non-body statuses, and it
// streams: a Flush commits the decision and flushes the compressor instead of buffering the whole response.
// sniffOctet additionally allows application/octet-stream bodies whose first bytes sniff as text.
func compressResponses(sniffOctet bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || r.Header.Get("Upgrade") != "" || !acceptsGzip(r.Header) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w, sniffOctet: sniffOctet}
		defer cw.finish()
		next.ServeHTTP(cw, r)
	})
}

type compressWriter struct {
	http.ResponseWriter
	sniffOctet bool
	status     int
	headerSent bool
	decided    bool
	gz         *gzip.Writer
	pending    []byte
}

func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *compressWriter) WriteHeader(code int) {
	if c.headerSent || c.status != 0 {
		return
	}
	if code >= 100 && code < 200 {
		c.ResponseWriter.WriteHeader(code)
		return
	}
	c.status = code
}

// eligible decides from the headers and the buffered prefix of the body whether this response is compressed.
func (c *compressWriter) eligible(first []byte) bool {
	header := c.Header()
	switch c.status {
	case 0, http.StatusOK:
	case http.StatusNoContent, http.StatusPartialContent, http.StatusNotModified:
		return false
	default:
		if c.status < 200 {
			return false
		}
	}
	if header.Get("Content-Encoding") != "" || header.Get("Content-Range") != "" {
		return false
	}
	contentType := header.Get("Content-Type")
	if contentType == "" {
		return false
	}
	if mediaType, _, _ := mime.ParseMediaType(contentType); mediaType == "application/octet-stream" && c.sniffOctet {
		return strings.HasPrefix(http.DetectContentType(first), "text/")
	}
	return compressibleType(contentType)
}

func (c *compressWriter) commit(compress bool) {
	c.decided = true
	status := c.status
	if status == 0 {
		status = http.StatusOK
	}
	header := c.Header()
	if compress {
		addVary(header, "Accept-Encoding")
		header.Set("Content-Encoding", "gzip")
		header.Del("Content-Length")
		if etag := header.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
			header.Set("ETag", "W/"+etag)
		}
		c.gz = gzipPool.Get().(*gzip.Writer)
		c.gz.Reset(c.ResponseWriter)
	} else if compressibleType(header.Get("Content-Type")) {
		addVary(header, "Accept-Encoding")
	}
	c.headerSent = true
	c.ResponseWriter.WriteHeader(status)
}

func (c *compressWriter) Write(p []byte) (int, error) {
	if c.decided {
		if err := c.writeBody(p); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	c.pending = append(c.pending, p...)
	chunk := c.pending
	known := -1
	if length, err := strconv.Atoi(c.Header().Get("Content-Length")); err == nil {
		known = length
	}
	switch {
	case known >= 0 && known < compressMinBytes:
		c.commit(false)
	case known < 0 && len(chunk) < compressMinBytes:
		return len(p), nil
	default:
		c.commit(c.eligible(chunk))
	}
	c.pending = nil
	if err := c.writeBody(chunk); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *compressWriter) writeBody(p []byte) error {
	if c.gz != nil {
		_, err := c.gz.Write(p)
		return err
	}
	_, err := c.ResponseWriter.Write(p)
	return err
}

// Flush commits the pending decision (small unflushed bodies go out uncompressed) and flushes the compressor so
// streaming responses keep their latency.
func (c *compressWriter) Flush() {
	if !c.decided {
		chunk := c.pending
		c.pending = nil
		c.commit(len(chunk) >= compressMinBytes && c.eligible(chunk))
		if len(chunk) > 0 {
			_ = c.writeBody(chunk)
		}
	}
	if c.gz != nil {
		_ = c.gz.Flush()
	}
	_ = http.NewResponseController(c.ResponseWriter).Flush()
}

func (c *compressWriter) finish() {
	if !c.decided {
		chunk := c.pending
		c.pending = nil
		if c.status == 0 && len(chunk) == 0 && !c.headerSent {
			// The handler wrote nothing: leave the implicit 200 to net/http.
			return
		}
		if len(chunk) > 0 {
			c.Header().Set("Content-Length", strconv.Itoa(len(chunk)))
		}
		c.commit(false)
		if len(chunk) > 0 {
			_ = c.writeBody(chunk)
		}
		return
	}
	if c.gz != nil {
		_ = c.gz.Close()
		c.gz.Reset(nil)
		gzipPool.Put(c.gz)
		c.gz = nil
	}
}
