//go:build windows

package local

import (
	"os"
	"testing"
)

func TestShellProfilesOnWindowsHost(t *testing.T) {
	profiles := ShellProfiles()
	found := map[string]bool{}
	for _, profile := range profiles {
		if profile.Args == nil {
			t.Fatalf("profile %+v has null args", profile)
		}
		if info, err := os.Stat(profile.Path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("profile %+v names a missing executable: %v", profile, err)
		}
		found[profile.ID] = true
	}
	for _, id := range []string{"powershell", "cmd"} {
		if !found[id] {
			t.Fatalf("profiles %+v lack %q, which every Windows host has", profiles, id)
		}
	}
}
