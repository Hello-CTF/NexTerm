//go:build windows

package local

import (
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func detectShellProfiles() []ShellProfile {
	var programFiles []string
	for _, name := range []string{"ProgramW6432", "ProgramFiles"} {
		if value := os.Getenv(name); value != "" && !containsFold(programFiles, value) {
			programFiles = append(programFiles, value)
		}
	}
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	return windowsShellProfiles(windowsShellSources{
		LookPath:        exec.LookPath,
		Exists:          regularFileExists,
		SystemRoot:      systemRoot,
		ComSpec:         os.Getenv("ComSpec"),
		ProgramFiles:    programFiles,
		WSL:             readWSLDistributions(),
		GitInstallPaths: readGitInstallPaths(),
	})
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func containsFold(values []string, value string) bool {
	for _, existing := range values {
		if strings.EqualFold(existing, value) {
			return true
		}
	}
	return false
}

func readWSLDistributions() []WSLDistribution {
	lxss, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Lxss`, registry.READ)
	if err != nil {
		return nil
	}
	defer lxss.Close()
	defaultID, _, _ := lxss.GetStringValue("DefaultDistribution")
	ids, err := lxss.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}
	distributions := make([]WSLDistribution, 0, len(ids))
	for _, id := range ids {
		distro, err := registry.OpenKey(lxss, id, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		name, _, nameErr := distro.GetStringValue("DistributionName")
		state, _, stateErr := distro.GetIntegerValue("State")
		distro.Close()
		if nameErr != nil || stateErr != nil {
			continue
		}
		distributions = append(distributions, WSLDistribution{Name: name, State: state, Default: strings.EqualFold(id, defaultID)})
	}
	return distributions
}

// readGitInstallPaths reads InstallPath of Git for Windows: the machine-wide install in the 64-bit and 32-bit
// registry views, then a per-user install.
func readGitInstallPaths() []string {
	var paths []string
	for _, source := range []struct {
		root   registry.Key
		access uint32
	}{
		{registry.LOCAL_MACHINE, registry.WOW64_64KEY},
		{registry.LOCAL_MACHINE, registry.WOW64_32KEY},
		{registry.CURRENT_USER, 0},
	} {
		key, err := registry.OpenKey(source.root, `SOFTWARE\GitForWindows`, registry.QUERY_VALUE|source.access)
		if err != nil {
			continue
		}
		value, kind, err := key.GetStringValue("InstallPath")
		key.Close()
		if err != nil || strings.TrimSpace(value) == "" {
			continue
		}
		if kind == registry.EXPAND_SZ {
			if expanded, err := registry.ExpandString(value); err == nil {
				value = expanded
			}
		}
		if !containsFold(paths, value) {
			paths = append(paths, value)
		}
	}
	return paths
}
