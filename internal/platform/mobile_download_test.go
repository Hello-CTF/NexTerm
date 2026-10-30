package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMobileDownloadPathSkipsNonPrefixedAndDesktop(t *testing.T) {
	if _, ok, err := MobileDownloadPath("/tmp/plain.txt"); ok || err != nil {
		t.Fatalf("plain path: ok=%v err=%v", ok, err)
	}
	if _, ok, err := MobileDownloadPath(MobileDownloadPrefix + "a.txt"); ok || err != nil {
		t.Fatalf("prefixed path on host GOOS: ok=%v err=%v", ok, err)
	}
}

func TestMobileDownloadPathIOS(t *testing.T) {
	home := t.TempDir()
	got, ok, err := mobileDownloadPath(MobileDownloadPrefix+"report.pdf", "ios",
		func() (string, bool) { return "", false },
		func() (string, error) { return home, nil })
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	want := filepath.Join(home, "Documents", "report.pdf")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if info, err := os.Stat(filepath.Dir(got)); err != nil || !info.IsDir() {
		t.Fatalf("download dir not created: %v", err)
	}
}

func TestMobileDownloadPathSanitizesName(t *testing.T) {
	home := t.TempDir()
	resolve := func(name string) string {
		t.Helper()
		got, ok, err := mobileDownloadPath(MobileDownloadPrefix+name, "ios",
			func() (string, bool) { return "", false },
			func() (string, error) { return home, nil })
		if err != nil || !ok {
			t.Fatalf("%q: ok=%v err=%v", name, ok, err)
		}
		return got
	}
	if got := resolve("../../etc/passwd"); got != filepath.Join(home, "Documents", "passwd") {
		t.Fatalf("traversal name: got %q", got)
	}
	if got := resolve(".."); got != filepath.Join(home, "Documents", "download") {
		t.Fatalf("dot-dot name: got %q", got)
	}
	if got := resolve("sub/dir\\win.txt"); got != filepath.Join(home, "Documents", "win.txt") {
		t.Fatalf("nested name: got %q", got)
	}
}

func TestMobileDownloadPathAndroidExternal(t *testing.T) {
	externalRoot := t.TempDir()
	filesDir := filepath.Join(t.TempDir(), "data", "data", "com.example.nexterm", "files")
	orig := androidExternalDataRoot
	androidExternalDataRoot = externalRoot
	t.Cleanup(func() { androidExternalDataRoot = orig })

	got, ok, err := mobileDownloadPath(MobileDownloadPrefix+"a.txt", "android",
		func() (string, bool) { return filesDir, true },
		func() (string, error) { return "", errors.New("unused") })
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	want := filepath.Join(externalRoot, "com.example.nexterm", "files", "Download", "a.txt")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if info, err := os.Stat(filepath.Dir(got)); err != nil || !info.IsDir() {
		t.Fatalf("external download dir not created: %v", err)
	}
}

func TestMobileDownloadPathAndroidFallsBackToPrivateDir(t *testing.T) {
	// A regular file where the external root should be makes MkdirAll fail, forcing the private fallback.
	externalRoot := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(externalRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	filesDir := filepath.Join(t.TempDir(), "data", "data", "com.example.nexterm", "files")
	orig := androidExternalDataRoot
	androidExternalDataRoot = externalRoot
	t.Cleanup(func() { androidExternalDataRoot = orig })

	got, ok, err := mobileDownloadPath(MobileDownloadPrefix+"a.txt", "android",
		func() (string, bool) { return filesDir, true },
		func() (string, error) { return "", errors.New("unused") })
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	want := filepath.Join(filesDir, "Download", "a.txt")
	if got != want {
		t.Fatalf("got %q, want fallback %q", got, want)
	}
}

func TestMobileDownloadPathPropagatesHomeError(t *testing.T) {
	homeErr := errors.New("no home")
	_, ok, err := mobileDownloadPath(MobileDownloadPrefix+"a.txt", "ios",
		func() (string, bool) { return "", false },
		func() (string, error) { return "", homeErr })
	if !ok || !errors.Is(err, homeErr) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
