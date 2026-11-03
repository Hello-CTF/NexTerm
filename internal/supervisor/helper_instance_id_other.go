//go:build !unix

package supervisor

import "os"

// The state directory DACL already restricts access on these platforms.
func instanceIDOwner(os.FileInfo) error { return nil }
