//go:build unix

package supervisor

import "os"

func instanceIDOwner(info os.FileInfo) error { return ownedByExpectedUser(info) }
