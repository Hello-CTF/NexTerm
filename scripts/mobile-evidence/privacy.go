package main

import (
	"bytes"
	"debug/buildinfo"
	"fmt"
	"sort"
	"strings"
)

// checkPrivacyEngine requires that the package carries no native privacy
// runtime: the engine is pure Go and lives in the app's Go library. Android
// libraries must also be built with GOEXPERIMENT=simd, which the engine's
// vector kernels need for usable speed.
func (c *checker) checkPrivacyEngine() {
	var stale []string
	for name := range c.manifest.entries {
		if strings.Contains(strings.ToLower(name), "onnxruntime") || strings.Contains(name, "/ai-runtime/") || strings.HasPrefix(name, "ai-runtime/") {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		c.add("privacy-engine", "failed", "package contains native privacy runtime files: "+strings.Join(stale, ", "))
		return
	}
	if c.id == "mobile-ios" {
		c.add("privacy-engine", "passed", "no native privacy runtime files; the privacy engine is built into the Go library")
		return
	}
	var checked []string
	for name, entry := range c.manifest.entries {
		parts := strings.Split(name, "/")
		if len(parts) != 3 || parts[0] != "lib" || parts[2] != "libwails.so" {
			continue
		}
		data, err := readEntry(c.handle, entry, maxExecutableBytes)
		if err != nil {
			c.add("privacy-engine", "failed", err.Error())
			return
		}
		info, err := buildinfo.Read(bytes.NewReader(data))
		if err != nil {
			c.add("privacy-engine", "failed", fmt.Sprintf("%s has no readable Go build info: %v", name, err))
			return
		}
		experiment := ""
		for _, setting := range info.Settings {
			if setting.Key == "GOEXPERIMENT" {
				experiment = setting.Value
			}
		}
		if !hasExperiment(experiment, "simd") {
			c.add("privacy-engine", "failed", fmt.Sprintf("%s was not built with GOEXPERIMENT=simd (GOEXPERIMENT=%q)", name, experiment))
			return
		}
		checked = append(checked, name)
	}
	if len(checked) == 0 {
		c.add("privacy-engine", "failed", "Android package has no lib/<abi>/libwails.so")
		return
	}
	sort.Strings(checked)
	c.add("privacy-engine", "passed", "no native privacy runtime files; built with GOEXPERIMENT=simd: "+strings.Join(checked, ", "))
}

func hasExperiment(list, name string) bool {
	for _, item := range strings.Split(list, ",") {
		if strings.TrimSpace(item) == name {
			return true
		}
	}
	return false
}
