package main

import (
	"regexp"
	"strings"
)

var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

type semVer struct {
	Version, Core, Major, Minor, Patch string
	Prerelease, Build                  []string
}

func parseSemVer(value string) (semVer, bool) {
	m := semverPattern.FindStringSubmatch(value)
	if m == nil {
		return semVer{}, false
	}
	v := semVer{Version: value, Core: m[1] + "." + m[2] + "." + m[3], Major: m[1], Minor: m[2], Patch: m[3]}
	if m[4] != "" {
		v.Prerelease = strings.Split(m[4], ".")
	}
	if m[5] != "" {
		v.Build = strings.Split(m[5], ".")
	}
	return v, true
}

// parseReleaseVersion accepts a semantic version other than the 0.0.0 placeholder.
func parseReleaseVersion(value string) (semVer, bool) {
	if value == "0.0.0" {
		return semVer{}, false
	}
	return parseSemVer(value)
}
