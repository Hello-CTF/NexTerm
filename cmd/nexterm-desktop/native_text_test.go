package main

import (
	"strings"
	"testing"
)

func TestMatchNativeLocale(t *testing.T) {
	for tag, want := range map[string]string{
		"zh-CN": "zh-CN", "zh_CN.UTF-8": "zh-CN", "zh-Hans-CN": "zh-CN", "zh-Hant-TW": "zh-TW", "zh_HK": "zh-TW",
		"en-US": "en", "ja_JP.UTF-8": "ja", "ko-KR": "ko", "de_DE@euro": "de", "fr-CA": "fr", "es-419": "es", "ru": "ru",
		"pt-BR": "", "": "",
	} {
		if got := matchNativeLocale(tag); got != want {
			t.Errorf("matchNativeLocale(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestNativeTextForFallsBackLikeTheInterface(t *testing.T) {
	if got := nativeTextFor([]string{"pt-BR", "de-AT"}).Lang; got != "de" {
		t.Errorf("first supported language: got %q", got)
	}
	if got := nativeTextFor([]string{"pt-BR"}).Lang; got != "en" {
		t.Errorf("unsupported language: got %q", got)
	}
	if got := nativeTextFor([]string{"C"}).Lang; got != "zh-CN" {
		t.Errorf("no language: got %q", got)
	}
	if got := nativeTextFor(nil).Lang; got != "zh-CN" {
		t.Errorf("nil: got %q", got)
	}
}

func TestEveryNativeTextIsComplete(t *testing.T) {
	for locale, text := range nativeTexts {
		if text.Lang != locale {
			t.Errorf("%s: Lang = %q", locale, text.Lang)
		}
		for name, value := range map[string]string{
			"TrayOpen": text.TrayOpen, "TrayQuit": text.TrayQuit, "StartupTitle": text.StartupTitle, "StartupLead": text.StartupLead,
			"StartupError": text.StartupError, "StartupDataDir": text.StartupDataDir, "StartupNext": text.StartupNext,
			"StartupRetry": text.StartupRetry, "StartupNote": text.StartupNote, "StartupLogsConsole": text.StartupLogsConsole,
		} {
			if strings.TrimSpace(value) == "" {
				t.Errorf("%s: %s is empty", locale, name)
			}
		}
		if strings.Count(text.StartupLogs, "%s") != 1 {
			t.Errorf("%s: StartupLogs needs exactly one %%s", locale)
		}
	}
}
