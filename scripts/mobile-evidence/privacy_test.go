package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivacyRuntimeValidation(t *testing.T) {
	for _, test := range []struct {
		name, id string
		entries  []zipFixture
		want     string
	}{
		{name: "missing android runtime", id: "mobile-android", entries: []zipFixture{{name: "lib/arm64-v8a/libwails.so"}}, want: "missing"},
		{name: "wrong android ABI", id: "mobile-android", entries: []zipFixture{{name: "lib/arm64-v8a/libwails.so"}, {name: "lib/arm64-v8a/libnexterm_privacy.so", data: privacyELF(62)}}, want: "does not match"},
		{name: "missing ios runtime", id: "mobile-ios", want: "missing"},
		{name: "simulator runtime", id: "mobile-ios", entries: []zipFixture{{name: "Payload/nexterm.app/Frameworks/NexTermPrivacy.framework/Info.plist", data: []byte("<plist><dict><key>CFBundleExecutable</key><string>NexTermPrivacy</string></dict></plist>")}, {name: "Payload/nexterm.app/Frameworks/NexTermPrivacy.framework/NexTermPrivacy", data: thinMachO(7)}}, want: "not an iOS device"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "package.zip")
			writeZip(t, path, zip.Store, test.entries)
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			stat, _ := file.Stat()
			manifest, err := inspectZip(file, stat.Size(), 512*mib, 4*gib, nil)
			if err != nil {
				t.Fatal(err)
			}
			c := checker{id: test.id, handle: file, manifest: manifest}
			err = c.validatePrivacyRuntime()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %s", err, test.want)
			}
		})
	}
}

func privacyLicenseFixtures(base, platform, arch string) []zipFixture {
	var entries []zipFixture
	files := map[string]string{}
	for _, name := range append(append([]string{}, requiredPrivacyNotices...), "dependency-1.0/LICENSE") {
		content := []byte("license fixture " + name)
		files["licenses/"+name] = fmt.Sprintf("%x", sha256.Sum256(content))
		entries = append(entries, zipFixture{name: base + "licenses/" + name, data: content, mode: 0644})
	}
	manifest, _ := json.Marshal(map[string]any{"platform": platform, "arch": arch, "files": files})
	return append(entries, zipFixture{name: base + "runtime.json", data: manifest, mode: 0644})
}

func TestPrivacyLicenseEvidence(t *testing.T) {
	for _, platform := range []string{"ios", "android"} {
		base := "Payload/nexterm.app/Frameworks/"
		if platform == "android" {
			base = "assets/ai-runtime/arm64-v8a/"
		}
		for _, variant := range []string{"complete", "missing upstream", "missing dependency", "altered notice", "wrong platform"} {
			t.Run(platform+"/"+variant, func(t *testing.T) {
				entries := privacyLicenseFixtures(base, platform, "arm64")
				for i := range entries {
					if variant == "missing upstream" && entries[i].name == base+"licenses/ASTRLINK-LICENSE" {
						entries[i].name += ".removed"
					}
					if variant == "missing dependency" && entries[i].name == base+"licenses/dependency-1.0/LICENSE" {
						entries[i].name += ".removed"
					}
					if variant == "altered notice" && entries[i].name == base+"licenses/ONNX-RUNTIME-THIRD-PARTY-NOTICES" {
						entries[i].data = []byte("altered")
					}
				}
				archive := filepath.Join(t.TempDir(), "package.zip")
				writeZip(t, archive, zip.Store, entries)
				file, err := os.Open(archive)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				stat, _ := file.Stat()
				manifest, err := inspectZip(file, stat.Size(), 512*mib, 4*gib, nil)
				if err != nil {
					t.Fatal(err)
				}
				c := checker{handle: file, manifest: manifest}
				expectedPlatform := platform
				if variant == "wrong platform" {
					expectedPlatform = "other"
				}
				err = c.validatePrivacyLicenses(base, expectedPlatform, "arm64")
				if (variant == "complete") != (err == nil) {
					t.Fatalf("license check: %v", err)
				}
			})
		}
	}
}
