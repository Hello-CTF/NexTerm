package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTempDirResolution(t *testing.T) {
	androidFiles := func() (string, bool) { return "/data/data/com.example.nexterm/files", true }
	noAndroidFiles := func() (string, bool) { return "", false }
	iosHome := filepath.Join(string(filepath.Separator), "var", "mobile", "Containers", "Data", "Application", "ABC")
	for _, test := range []struct {
		name         string
		goos         string
		fallback     string
		home         string
		androidFiles func() (string, bool)
		want         string
	}{
		{name: "linux", goos: "linux", fallback: "/tmp", want: "/tmp"},
		{name: "darwin", goos: "darwin", fallback: "/var/folders/x", want: "/var/folders/x"},
		{name: "windows", goos: "windows", fallback: `C:\Temp`, want: `C:\Temp`},
		{
			name:         "android files dir",
			goos:         "android",
			fallback:     "/data/local/tmp",
			androidFiles: androidFiles,
			want:         filepath.Join("/data/data/com.example.nexterm/files", "tmp"),
		},
		{
			name:         "android unresolved files dir",
			goos:         "android",
			fallback:     "/data/local/tmp",
			androidFiles: noAndroidFiles,
			want:         "/data/local/tmp",
		},
		{name: "ios container", goos: "ios", fallback: "/tmp", home: iosHome, want: filepath.Join(iosHome, "tmp")},
		{name: "ios unresolved home", goos: "ios", fallback: "/tmp", want: "/tmp"},
	} {
		files := test.androidFiles
		if files == nil {
			files = noAndroidFiles
		}
		if got := tempDir(test.goos, test.fallback, test.home, files); got != test.want {
			t.Fatalf("%s = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestTempDirMatchesOSOnDesktop(t *testing.T) {
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		t.Skip("mobile redirects the temp dir")
	}
	if got := TempDir(); got != os.TempDir() {
		t.Fatalf("TempDir() = %q, want %q", got, os.TempDir())
	}
}
