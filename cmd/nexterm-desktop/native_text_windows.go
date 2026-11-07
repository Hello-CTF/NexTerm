//go:build windows

package main

import "golang.org/x/sys/windows"

// systemLanguageTags returns the user's preferred Windows display languages, most preferred first.
func systemLanguageTags() []string {
	languages, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil {
		return nil
	}
	return languages
}
