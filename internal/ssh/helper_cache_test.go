package ssh

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func countCompressions(t *testing.T) *int {
	t.Helper()
	count := 0
	onHelperCompress = func() { count++ }
	t.Cleanup(func() { onHelperCompress = nil })
	return &count
}

func writeHelper(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCachedHelperSourceCompressesOncePerDigest(t *testing.T) {
	count := countCompressions(t)
	cacheDir := filepath.Join(t.TempDir(), "cache")
	helper := writeHelper(t, strings.Repeat("helper-payload-", 32<<10))
	first, compressed, temporary, err := cachedHelperSource(helper, cacheDir)
	if err != nil || !compressed || temporary {
		t.Fatalf("first = %q compressed=%v temporary=%v err=%v", first, compressed, temporary, err)
	}
	second, compressed, temporary, err := cachedHelperSource(helper, cacheDir)
	if err != nil || !compressed || temporary || second != first {
		t.Fatalf("second = %q compressed=%v temporary=%v err=%v", second, compressed, temporary, err)
	}
	if *count != 1 {
		t.Fatalf("helper compressed %d times, want 1", *count)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("cached artifact must survive reuse: %v", err)
	}
	// Another helper content gets its own entry.
	other := writeHelper(t, strings.Repeat("other-payload-", 32<<10))
	otherPath, _, _, err := cachedHelperSource(other, cacheDir)
	if err != nil || otherPath == first || *count != 2 {
		t.Fatalf("other = %q, %v, compressions=%d", otherPath, err, *count)
	}
}

func TestCachedHelperSourceRebuildsCorruptEntries(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"truncated": func(t *testing.T, path string) {
			data, _ := os.ReadFile(path)
			if err := os.WriteFile(path, data[:len(data)/2], 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"flipped byte": func(t *testing.T, path string) {
			data, _ := os.ReadFile(path)
			data[len(data)/2] ^= 0xff
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"valid gzip of other content": func(t *testing.T, path string) {
			other := writeHelper(t, strings.Repeat("tampered-", 32<<10))
			built, _, err := compressedHelperSource(other)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(built)
			os.Remove(built)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"empty": func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			count := countCompressions(t)
			cacheDir := t.TempDir()
			helper := writeHelper(t, strings.Repeat("helper-payload-", 32<<10))
			path, _, _, err := cachedHelperSource(helper, cacheDir)
			if err != nil {
				t.Fatal(err)
			}
			corrupt(t, path)
			*count = 0
			rebuilt, compressed, temporary, err := cachedHelperSource(helper, cacheDir)
			if err != nil || !compressed || temporary || rebuilt != path || *count != 1 {
				t.Fatalf("rebuild = %q compressed=%v temporary=%v err=%v compressions=%d", rebuilt, compressed, temporary, err, *count)
			}
			if !gzipMatchesDigest(rebuilt, mustDigest(t, helper), int64(len(strings.Repeat("helper-payload-", 32<<10)))) {
				t.Fatal("rebuilt cache entry does not decompress to the helper")
			}
			entries, _ := os.ReadDir(cacheDir)
			if len(entries) != 1 {
				t.Fatalf("stray files left in cache: %v", entries)
			}
		})
	}
}

func TestCachedHelperSourceWithoutCacheDirUsesCallerOwnedTemporary(t *testing.T) {
	helper := writeHelper(t, strings.Repeat("helper-payload-", 32<<10))
	path, compressed, temporary, err := cachedHelperSource(helper, "")
	if err != nil || !compressed || !temporary {
		t.Fatalf("path=%q compressed=%v temporary=%v err=%v", path, compressed, temporary, err)
	}
	os.Remove(path)
}

func TestCachedHelperSourceSkipsIncompressibleHelpers(t *testing.T) {
	random := make([]byte, 64<<10)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(helper, random, 0o700); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	path, compressed, _, err := cachedHelperSource(helper, cacheDir)
	if err != nil || compressed || path != "" {
		t.Fatalf("path=%q compressed=%v err=%v", path, compressed, err)
	}
	if entries, _ := os.ReadDir(cacheDir); len(entries) != 0 {
		t.Fatalf("incompressible helper left cache files: %v", entries)
	}
}

func TestCachedHelperSourceFallsBackWhenCacheDirIsUnusable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	helper := writeHelper(t, strings.Repeat("helper-payload-", 32<<10))
	path, compressed, temporary, err := cachedHelperSource(helper, filepath.Join(blocker, "cache"))
	if err != nil || !compressed || !temporary {
		t.Fatalf("path=%q compressed=%v temporary=%v err=%v", path, compressed, temporary, err)
	}
	os.Remove(path)
}

func TestHelperGzipCacheKeepsOnlyRecentEntries(t *testing.T) {
	cacheDir := t.TempDir()
	var newest string
	for i := 0; i < helperGzipCacheKeep+3; i++ {
		helper := writeHelper(t, strings.Repeat(string(rune('a'+i)), 64<<10))
		path, _, _, err := cachedHelperSource(helper, cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		// Distinct, increasing mtimes keep the pruning order independent of clock resolution.
		stamp := time.Unix(1_700_000_000+int64(i), 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		newest = path
	}
	entries, _ := os.ReadDir(cacheDir)
	if len(entries) > helperGzipCacheKeep {
		t.Fatalf("cache holds %d entries, want at most %d", len(entries), helperGzipCacheKeep)
	}
	if _, err := os.Stat(newest); err != nil {
		t.Fatalf("newest entry was pruned: %v", err)
	}
}

func mustDigest(t *testing.T, path string) string {
	t.Helper()
	digest, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
