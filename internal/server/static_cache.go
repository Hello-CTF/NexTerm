package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
	"time"

	"github.com/klauspost/compress/gzip"
)

const (
	// staticCacheMaxFile bounds the files that get a content-hash ETag and a cached compressed variant.
	staticCacheMaxFile = 16 << 20
	// staticCacheMaxBytes bounds the memory held by cached compressed variants and injected HTML.
	staticCacheMaxBytes = 64 << 20
	staticCacheMaxItems = 512
)

type staticKey struct {
	path    string
	size    int64
	modTime time.Time
	html    bool
}

// staticEntry is the immutable result of reading one static file revision: a strong content-hash ETag and, for
// text-like files, the gzip variant produced once so requests cost no CPU.
type staticEntry struct {
	once     sync.Once
	ok       bool
	raw      []byte // injected HTML only
	etag     string
	gzip     []byte
	gzipETag string
}

type staticCache struct {
	mu      sync.Mutex
	entries map[staticKey]*staticEntry
	order   []staticKey
	bytes   int64
}

func newStaticCache() *staticCache {
	return &staticCache{entries: make(map[staticKey]*staticEntry)}
}

// load returns the entry for the file revision (path, size, mtime), computing it on first use; it returns nil when the
// file cannot be read so the caller falls back to streaming it without validators.
func (c *staticCache) load(path string, info os.FileInfo, html bool) *staticEntry {
	key := staticKey{path: path, size: info.Size(), modTime: info.ModTime(), html: html}
	c.mu.Lock()
	entry, found := c.entries[key]
	if !found {
		entry = &staticEntry{}
		c.entries[key] = entry
		c.order = append(c.order, key)
	}
	c.mu.Unlock()
	entry.once.Do(func() {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		if html {
			data = []byte(InjectTransportMarker(string(data)))
			entry.raw = data
		}
		sum := sha256.Sum256(data)
		tag := hex.EncodeToString(sum[:16])
		entry.etag = `"` + tag + `"`
		entry.ok = true
		if compressibleType(staticContentType(path)) && len(data) >= compressMinBytes {
			var buffer bytes.Buffer
			writer, _ := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
			if _, err := writer.Write(data); err == nil && writer.Close() == nil && buffer.Len() < len(data) {
				entry.gzip = buffer.Bytes()
				entry.gzipETag = `"` + tag + `-gzip"`
			}
		}
		c.account(key, int64(len(entry.raw)+len(entry.gzip)))
	})
	if !entry.ok {
		c.mu.Lock()
		if c.entries[key] == entry {
			delete(c.entries, key)
			for index, queued := range c.order {
				if queued == key {
					c.order = append(c.order[:index], c.order[index+1:]...)
					break
				}
			}
		}
		c.mu.Unlock()
		return nil
	}
	return entry
}

// account records the retained size of a new entry and evicts the oldest revisions beyond the bounds.
func (c *staticCache) account(key staticKey, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, present := c.entries[key]; !present {
		// Evicted by a concurrent insert before it could be accounted; counting it now would leak the bytes.
		return
	}
	c.bytes += size
	for len(c.order) > 1 && (c.bytes > staticCacheMaxBytes || len(c.order) > staticCacheMaxItems) {
		oldest := c.order[0]
		c.order = c.order[1:]
		if oldest == key {
			c.order = append(c.order, oldest)
			continue
		}
		if evicted, ok := c.entries[oldest]; ok {
			c.bytes -= int64(len(evicted.raw) + len(evicted.gzip))
			delete(c.entries, oldest)
		}
	}
}
