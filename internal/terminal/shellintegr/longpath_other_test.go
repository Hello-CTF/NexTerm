//go:build !windows

package shellintegr

import "testing"

func longPath(_ *testing.T, path string) string { return path }
