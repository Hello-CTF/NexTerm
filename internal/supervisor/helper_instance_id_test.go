package supervisor

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
)

var instanceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestHelperInstanceIDIsStablePerStateDir(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	first, err := HelperInstanceID(a)
	if err != nil || !instanceIDPattern.MatchString(first) {
		t.Fatalf("first id %q, %v", first, err)
	}
	again, err := HelperInstanceID(a)
	if err != nil || again != first {
		t.Fatalf("second id %q, %v; want %q", again, err, first)
	}
	other, err := HelperInstanceID(b)
	if err != nil || !instanceIDPattern.MatchString(other) || other == first {
		t.Fatalf("other dir id %q, %v; want a different valid id", other, err)
	}
	entries, _ := os.ReadDir(a)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestHelperInstanceIDConcurrentFirstCallsAgree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	const n = 16
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ids[i], errs[i] = HelperInstanceID(dir)
		}()
	}
	close(start)
	wg.Wait()
	for i := range n {
		if errs[i] != nil || ids[i] != ids[0] || !instanceIDPattern.MatchString(ids[i]) {
			t.Fatalf("call %d: %q, %v; want %q", i, ids[i], errs[i], ids[0])
		}
	}
}

func TestHelperInstanceIDReplacesInvalidContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if _, err := HelperInstanceID(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, HelperInstanceIDFile)
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := HelperInstanceID(dir)
	if err != nil || !instanceIDPattern.MatchString(id) {
		t.Fatalf("id %q, %v", id, err)
	}
	if again, err := HelperInstanceID(dir); err != nil || again != id {
		t.Fatalf("replacement not persisted: %q, %v", again, err)
	}
}
