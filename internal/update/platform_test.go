package update

import (
	"strings"
	"testing"
)

func TestPlatformSupportedMatrix(t *testing.T) {
	for _, platform := range []string{
		"windows/amd64", "windows/arm64",
		"darwin/amd64", "darwin/arm64",
		"linux/amd64", "linux/arm64",
	} {
		goos, goarch, _ := strings.Cut(platform, "/")
		if err := platformSupported(goos, goarch); err != nil {
			t.Fatalf("platformSupported(%s): %v", platform, err)
		}
	}
}

func TestPlatformSupportedRejectsUnsupportedPlatform(t *testing.T) {
	if err := platformSupported("plan9", "amd64"); err == nil {
		t.Fatal("unsupported platform must fail")
	}
}
