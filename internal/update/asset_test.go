package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchAssetSelectsPerPlatform(t *testing.T) {
	release := Release{Assets: []Asset{
		{Name: "nexterm-desktop_windows_amd64_0.3.0.exe"},
		{Name: "nexterm-desktop_windows_arm64_0.3.0.exe"},
		{Name: "nexterm-desktop_macos_arm64_0.3.0.dmg"},
		{Name: "nexterm-desktop_macos_amd64_0.3.0.dmg"},
		{Name: "nexterm-desktop_linux_amd64_0.3.0.deb"},
		{Name: "nexterm-desktop_linux_arm64_0.3.0.deb"},
		{Name: "nexterm-server_linux_amd64_0.3.0.tar.gz"},
		{Name: "SHA256SUMS"},
	}}
	cases := map[string]string{
		"windows/amd64": "nexterm-desktop_windows_amd64_0.3.0.exe",
		"windows/arm64": "nexterm-desktop_windows_arm64_0.3.0.exe",
		"darwin/arm64":  "nexterm-desktop_macos_arm64_0.3.0.dmg",
		"darwin/amd64":  "nexterm-desktop_macos_amd64_0.3.0.dmg",
		"linux/amd64":   "nexterm-desktop_linux_amd64_0.3.0.deb",
		"linux/arm64":   "nexterm-desktop_linux_arm64_0.3.0.deb",
	}
	for platform, want := range cases {
		goos, goarch, _ := strings.Cut(platform, "/")
		asset, ok := matchAsset(release, goos, goarch)
		if !ok || asset.Name != want {
			t.Fatalf("matchAsset(%s) = %+v, %v, want %s", platform, asset, ok, want)
		}
	}
	if _, ok := matchAsset(release, "plan9", "amd64"); ok {
		t.Fatal("unsupported platform must not match")
	}
}

func TestMatchAssetSkipsServerArchive(t *testing.T) {
	release := Release{Assets: []Asset{{Name: "nexterm-server_linux_amd64_0.3.0.tar.gz"}}}
	if _, ok := matchAsset(release, "linux", "amd64"); ok {
		t.Fatal("server archive must not match the desktop update asset")
	}
}

func TestMatchAssetRejectsTarGz(t *testing.T) {
	legacy := Release{Assets: []Asset{{Name: "nexterm-desktop_linux_amd64_0.3.0.tar.gz"}}}
	if asset, ok := matchAsset(legacy, "linux", "amd64"); ok {
		t.Fatalf("tar.gz must not match = %+v", asset)
	}
	both := Release{Assets: []Asset{
		{Name: "nexterm-desktop_linux_amd64_0.3.0.tar.gz"},
		{Name: "nexterm-desktop_linux_amd64_0.3.0.deb"},
	}}
	if asset, ok := matchAsset(both, "linux", "amd64"); !ok || asset.Name != "nexterm-desktop_linux_amd64_0.3.0.deb" {
		t.Fatalf("only deb must match = %+v, %v", asset, ok)
	}
}

func TestParseSHA256SUMS(t *testing.T) {
	sums, err := parseSHA256SUMS(strings.NewReader(
		"aaaaaaaabbbbccccddddaaaaaaaabbbbccccddddaaaaaaaabbbbccccddddaaaa  nexterm-desktop_windows_amd64_0.3.0.exe\n" +
			"1111111122222222333333334444444455555555666666667777777788888888 *nexterm-desktop_macos_arm64_0.3.0.dmg\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 2 {
		t.Fatalf("sums = %v", sums)
	}
	if got := sums["nexterm-desktop_windows_amd64_0.3.0.exe"]; got != "aaaaaaaabbbbccccddddaaaaaaaabbbbccccddddaaaaaaaabbbbccccddddaaaa" {
		t.Fatalf("digest = %q", got)
	}
	if got := sums["nexterm-desktop_macos_arm64_0.3.0.dmg"]; got != "1111111122222222333333334444444455555555666666667777777788888888" {
		t.Fatalf("digest = %q", got)
	}
	if _, err := parseSHA256SUMS(strings.NewReader("not-a-digest  file\n")); err == nil {
		t.Fatal("invalid digest must fail")
	}
	if _, err := parseSHA256SUMS(strings.NewReader("\n")); err == nil {
		t.Fatal("empty sums must fail")
	}
}

func TestVerifyChecksumFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "asset.bin")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksumFile(path, "asset.bin", map[string]string{"asset.bin": digest}); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksumFile(path, "asset.bin", map[string]string{"asset.bin": strings.Repeat("0", 64)}); err == nil {
		t.Fatal("checksum mismatch must fail")
	}
	if err := verifyChecksumFile(path, "asset.bin", map[string]string{}); err == nil {
		t.Fatal("missing checksum entry must fail")
	}
}
