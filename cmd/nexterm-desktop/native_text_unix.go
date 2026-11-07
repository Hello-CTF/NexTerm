//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"time"
)

var appleLanguagePattern = regexp.MustCompile(`"?([A-Za-z]{2,3}(?:[-_][A-Za-z0-9]+)*)"?`)

// systemLanguageTags returns the locale environment and, on macOS, where apps started from Finder carry no locale
// environment, the user's preferred languages.
func systemLanguageTags() []string {
	var tags []string
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		if value := os.Getenv(name); value != "" {
			tags = append(tags, value)
		}
	}
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
			for _, match := range appleLanguagePattern.FindAllStringSubmatch(string(out), -1) {
				tags = append(tags, match[1])
			}
		}
	}
	return tags
}
