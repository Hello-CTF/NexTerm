package main

import (
	"testing"
)

// desktopAssetPath results feed embed.FS lookups, which are keyed by
// slash-separated paths on every OS. Native separators (filepath.FromSlash
// yields backslashes on Windows) make every nested asset lookup fail and
// silently fall back to index.html; the WebView2 module scripts are then
// blocked by the HTML MIME type and the desktop smoke times out without a
// verdict (M68).
func TestDesktopAssetPathReturnsIOFSRelativePath(t *testing.T) {
	for _, test := range []struct {
		escaped string
		want    string
	}{
		{escaped: "/", want: ""},
		{escaped: "/index.html", want: "index.html"},
		{escaped: "/icon.png", want: "icon.png"},
		{escaped: "/assets/index-DDzVmQ83.js", want: "assets/index-DDzVmQ83.js"},
		{escaped: "/assets/nested/chunk-CwsofdO1.js", want: "assets/nested/chunk-CwsofdO1.js"},
		{escaped: "/brand/nexterm-mark-128.png", want: "brand/nexterm-mark-128.png"},
	} {
		got, err := desktopAssetPath(test.escaped)
		if err != nil {
			t.Fatalf("desktopAssetPath(%q) error: %v", test.escaped, err)
		}
		if got != test.want {
			t.Errorf("desktopAssetPath(%q) = %q, want %q", test.escaped, got, test.want)
		}
	}
}

func TestDesktopAssetPathRejectsEscapes(t *testing.T) {
	for _, escaped := range []string{
		"/../secret",
		"/%2e%2e/secret",
		"/a/../../secret",
		"/a%5cb",
		"/%2fetc",
		"/a%00b",
	} {
		if got, err := desktopAssetPath(escaped); err == nil {
			t.Errorf("desktopAssetPath(%q) = %q, want rejection", escaped, got)
		}
	}
}
