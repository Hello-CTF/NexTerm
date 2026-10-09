package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchAssetSelectsPerPlatform(t *testing.T) {
	release := Release{Assets: []Asset{
		{Name: "NexTerm_0.3.0_x64-setup.exe"},
		{Name: "NexTerm_0.3.0_arm64-setup.exe"},
		{Name: "NexTerm_0.3.0_aarch64.dmg"},
		{Name: "NexTerm_0.3.0_x86_64.dmg"},
		{Name: "NexTerm-desktop_0.3.0_linux_amd64.tar.gz"},
		{Name: "NexTerm-desktop_0.3.0_linux_arm64.tar.gz"},
		{Name: "NexTerm-server_0.3.0_linux_amd64.tar.gz"},
		{Name: "SHA256SUMS"},
	}}
	cases := map[string]string{
		"windows/amd64": "NexTerm_0.3.0_x64-setup.exe",
		"windows/arm64": "NexTerm_0.3.0_arm64-setup.exe",
		"darwin/arm64":  "NexTerm_0.3.0_aarch64.dmg",
		"darwin/amd64":  "NexTerm_0.3.0_x86_64.dmg",
		"linux/amd64":   "NexTerm-desktop_0.3.0_linux_amd64.tar.gz",
		"linux/arm64":   "NexTerm-desktop_0.3.0_linux_arm64.tar.gz",
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
	release := Release{Assets: []Asset{{Name: "NexTerm-server_0.3.0_linux_amd64.tar.gz"}}}
	if _, ok := matchAsset(release, "linux", "amd64"); ok {
		t.Fatal("server archive must not match the desktop update asset")
	}
}

func TestParseSHA256SUMS(t *testing.T) {
	sums, err := parseSHA256SUMS(strings.NewReader(
		"aaaaaaaabbbbccccddddaaaaaaaabbbbccccddddaaaaaaaabbbbccccddddaaaa  NexTerm_0.3.0_x64-setup.exe\n" +
			"1111111122222222333333334444444455555555666666667777777788888888 *NexTerm_0.3.0_aarch64.dmg\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 2 {
		t.Fatalf("sums = %v", sums)
	}
	if got := sums["NexTerm_0.3.0_x64-setup.exe"]; got != "aaaaaaaabbbbccccddddaaaaaaaabbbbccccddddaaaaaaaabbbbccccddddaaaa" {
		t.Fatalf("digest = %q", got)
	}
	if got := sums["NexTerm_0.3.0_aarch64.dmg"]; got != "1111111122222222333333334444444455555555666666667777777788888888" {
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
