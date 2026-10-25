package server

import "testing"

// An entry evicted by a concurrent insert before it could be accounted must not add its bytes afterwards, or the
// total drifts upward and the cache keeps evicting everything else.
func TestStaticCacheDoesNotAccountBytesForAnEvictedEntry(t *testing.T) {
	cache := newStaticCache()
	live := staticKey{path: "live.js"}
	cache.entries[live] = &staticEntry{}
	cache.order = append(cache.order, live)

	cache.account(staticKey{path: "gone.js"}, 1<<20)
	if cache.bytes != 0 {
		t.Fatalf("bytes = %d after accounting an entry that is no longer cached, want 0", cache.bytes)
	}

	cache.account(live, 4096)
	if cache.bytes != 4096 {
		t.Fatalf("bytes = %d, want the cached entry's size", cache.bytes)
	}
}
