package main

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

func (c *checker) checkPrivacyRuntime() {
	err := c.validatePrivacyRuntime()
	if err != nil {
		c.add("privacy-runtime", "failed", err.Error())
		return
	}
	c.add("privacy-runtime", "passed", "local inference libraries and license notices match package platform, architecture and runtime manifest")
}

func (c *checker) privacyEntry(name string) ([]byte, error) {
	entry, ok := c.manifest.entries[name]
	if !ok {
		return nil, fmt.Errorf("required privacy runtime entry is missing: %s", name)
	}
	return readEntry(c.handle, entry, maxExecutableBytes)
}

func (c *checker) validatePrivacyRuntime() error {
	if c.id == "mobile-ios" {
		for _, name := range []string{"NexTermPrivacy", "onnxruntime"} {
			base := "Payload/nexterm.app/Frameworks/" + name + ".framework/"
			plist, err := c.privacyEntry(base + "Info.plist")
			if err != nil {
				return err
			}
			parsed, err := parsePlist(plist)
			if err != nil {
				return err
			}
			dict, ok := parsed.(map[string]any)
			if !ok || dict["CFBundleExecutable"] != name {
				return fmt.Errorf("invalid framework executable in %sInfo.plist", base)
			}
			data, err := c.privacyEntry(base + name)
			if err != nil {
				return err
			}
			platforms, err := machOBuildPlatforms(data)
			if err != nil {
				return err
			}
			for _, platform := range platforms {
				if platform != iosDevicePlatform {
					return fmt.Errorf("%s is not an iOS device runtime", name)
				}
			}
			binary, err := macho.NewFile(bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("%s must be a thin arm64 runtime: %w", name, err)
			}
			if binary.Cpu != macho.CpuArm64 || binary.Type != macho.TypeDylib {
				return fmt.Errorf("%s must be an arm64 dynamic library", name)
			}
		}
		return c.validatePrivacyLicenses("Payload/nexterm.app/Frameworks/", "ios", "arm64")
	}
	found := false
	for name := range c.manifest.entries {
		if !strings.HasPrefix(name, "lib/") || !strings.HasSuffix(name, "/libwails.so") {
			continue
		}
		parts := strings.Split(name, "/")
		if len(parts) != 3 {
			continue
		}
		machine := map[string]elf.Machine{"arm64-v8a": elf.EM_AARCH64, "x86_64": elf.EM_X86_64}[parts[1]]
		if machine == 0 {
			return fmt.Errorf("unsupported Android runtime ABI: %s", parts[1])
		}
		found = true
		for _, library := range []string{"libnexterm_privacy.so", "libonnxruntime.so"} {
			data, err := c.privacyEntry("lib/" + parts[1] + "/" + library)
			if err != nil {
				return err
			}
			binary, err := elf.NewFile(bytes.NewReader(data))
			if err != nil {
				return err
			}
			if binary.Class != elf.ELFCLASS64 || binary.Machine != machine || binary.Type != elf.ET_DYN {
				return fmt.Errorf("%s does not match Android ABI %s", library, parts[1])
			}
		}
		arch := "arm64"
		if parts[1] == "x86_64" {
			arch = "amd64"
		}
		if err := c.validatePrivacyLicenses("assets/ai-runtime/"+parts[1]+"/", "android", arch); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("Android package has no supported libwails.so ABI")
	}
	return nil
}

var requiredPrivacyNotices = []string{"ASTRLINK-LICENSE", "ASTRLINK-SOURCE.md", "ONNX-RUNTIME-LICENSE", "ONNX-RUNTIME-THIRD-PARTY-NOTICES", "native-dependencies.json"}

func (c *checker) validatePrivacyLicenses(base, platform, arch string) error {
	data, err := c.privacyEntry(base + "runtime.json")
	if err != nil {
		return err
	}
	var manifest struct {
		Platform string            `json:"platform"`
		Arch     string            `json:"arch"`
		Files    map[string]string `json:"files"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("invalid privacy runtime manifest: %w", err)
	}
	if manifest.Platform != platform || manifest.Arch != arch {
		return fmt.Errorf("privacy runtime manifest platform does not match %s/%s", platform, arch)
	}
	for _, name := range requiredPrivacyNotices {
		if _, ok := manifest.Files["licenses/"+name]; !ok {
			return fmt.Errorf("privacy runtime manifest is missing license %s", name)
		}
	}
	for name, digest := range manifest.Files {
		if !strings.HasPrefix(name, "licenses/") {
			continue
		}
		if path.Clean(name) != name {
			return fmt.Errorf("invalid privacy license path %s", name)
		}
		content, err := c.privacyEntry(base + name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		if len(content) == 0 || hex.EncodeToString(sum[:]) != digest {
			return fmt.Errorf("privacy license integrity check failed: %s", name)
		}
	}
	return nil
}
