package ssh

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	helperGzipCacheKeep = 8
	helperGzipSuffix    = ".gz"
)

// onHelperCompress is called each time a helper is actually gzip-compressed, so
// tests can assert that a cached artifact is reused instead of rebuilt.
var onHelperCompress func()

// defaultHelperGzipCacheDir is used when the Resolver has no explicit CacheDir.
func defaultHelperGzipCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, "NexTerm", "helper-gz")
}

// cachedHelperSource returns a gzip of localPath stored under cacheDir and named
// after the helper content digest. A cached file is reused only after it
// decompresses to exactly the helper bytes, so a corrupted, truncated or
// tampered entry is rebuilt. temporary reports whether the caller owns (and must
// delete) the returned file; it is false for cache entries.
func cachedHelperSource(localPath, cacheDir string) (compressedPath string, compressed, temporary bool, err error) {
	if cacheDir == "" {
		path, ok, err := compressedHelperSource(localPath)
		return path, ok, true, err
	}
	info, err := os.Stat(localPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", false, false, err
	}
	digest, err := fileSHA256(localPath)
	if err != nil {
		return "", false, false, err
	}
	cached := filepath.Join(cacheDir, digest+helperGzipSuffix)
	if gzipMatchesDigest(cached, digest, info.Size()) {
		// Refresh the mtime so pruning drops the least recently used entries.
		now := time.Now()
		_ = os.Chtimes(cached, now, now)
		return cached, true, false, nil
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		// An unusable cache must not block the upload.
		path, ok, err := compressedHelperSource(localPath)
		return path, ok, true, err
	}
	built, ok, err := compressHelperInto(localPath, cacheDir)
	if err != nil || !ok {
		return "", false, false, err
	}
	// Verify what was written before publishing it under the digest name.
	if !gzipMatchesDigest(built, digest, info.Size()) {
		_ = os.Remove(built)
		path, ok, err := compressedHelperSource(localPath)
		return path, ok, true, err
	}
	if err := os.Rename(built, cached); err != nil {
		_ = os.Remove(built)
		path, ok, err := compressedHelperSource(localPath)
		return path, ok, true, err
	}
	pruneHelperGzipCache(cacheDir, cached)
	return cached, true, false, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// gzipMatchesDigest reports whether path is a complete gzip stream whose
// decompressed content has the given size and SHA-256.
func gzipMatchesDigest(path, digest string, size int64) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return false
	}
	defer reader.Close()
	hash := sha256.New()
	count, err := io.Copy(hash, reader)
	if err != nil || count != size {
		return false
	}
	return hex.EncodeToString(hash.Sum(nil)) == digest
}

func pruneHelperGzipCache(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type entry struct {
		path string
		mod  int64
	}
	var files []entry
	for _, item := range entries {
		name := item.Name()
		if item.IsDir() || !strings.HasSuffix(name, helperGzipSuffix) {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		files = append(files, entry{filepath.Join(dir, name), info.ModTime().UnixNano()})
	}
	if len(files) <= helperGzipCacheKeep {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod > files[j].mod })
	for _, stale := range files[helperGzipCacheKeep:] {
		if stale.path != keep {
			if err := os.Remove(stale.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				continue
			}
		}
	}
}
