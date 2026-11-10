package base

import "testing"

func TestPathSeparators(t *testing.T) {
	cases := map[string]string{
		"/home/a\\b":        "/",
		"~":                 "/",
		"~/a\\b":            "/",
		"C:\\Users":         `/\`,
		"C:/Users":          `/\`,
		"\\\\server\\share": `/\`,
		"relative\\x":       `/\`,
		"":                  `/\`,
	}
	for path, want := range cases {
		if got := PathSeparators(path); got != want {
			t.Errorf("PathSeparators(%q) = %q, want %q", path, got, want)
		}
	}
}
