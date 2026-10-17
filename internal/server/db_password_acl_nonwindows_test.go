//go:build !windows

package server

import "testing"

func requireDBPasswordACLForTest(*testing.T, string, []string) {}
