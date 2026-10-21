package sshconfig

import "os"

// OpenSharedDelete exposes openSharedDelete — the shared-delete read open
// used by the config parser and one-shot identity-key reads — to packages
// outside sshconfig whose readers hold the same Windows delete/rotation
// contract, currently the production SSH import key-file fingerprint reader.
// It applies no boundary checks of its own: callers keep their symlink,
// regular-file and size checks. Off Windows it is a plain os.Open.
func OpenSharedDelete(path string) (*os.File, error) {
	return openSharedDelete(path)
}
