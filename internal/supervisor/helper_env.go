package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// StartEnv is what a helper inherited from the process that started it: the SHELL value and a digest of
// the PATH (the PATH itself can be long and is not needed to compare). It is reported in the helper's
// hello acknowledgement so a long-lived helper can be told apart from one started in today's environment.
type StartEnv struct {
	Shell    string `json:"shell,omitempty"`
	PathHash string `json:"path_hash,omitempty"`
}

// HashPath returns the digest recorded for a PATH value; an empty PATH has an empty digest.
func HashPath(path string) string {
	if path == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

// CurrentStartEnv is the environment a helper started by this process inherits now.
func CurrentStartEnv() StartEnv {
	return StartEnv{Shell: os.Getenv("SHELL"), PathHash: HashPath(os.Getenv("PATH"))}
}

// expectedStartEnv is the environment a replacement helper would be started in; tests replace it.
var expectedStartEnv = CurrentStartEnv

// EnvStaleAgainst reports whether the helper started in another SHELL or PATH than expected. A helper
// that did not report its start environment is not judged by it.
func (h RunningHelper) EnvStaleAgainst(expected StartEnv) bool {
	return h.Protocol == ProtocolVersion && h.StartEnv != nil && *h.StartEnv != expected
}

// StaleReason says why a same-protocol helper is stale: "build", "env", or empty when it is current.
// A different build wins because it needs the replacement either way.
func (h RunningHelper) StaleReason(local RuntimeMetadata, expected StartEnv) string {
	switch {
	case h.StaleAgainst(local):
		return "build"
	case h.EnvStaleAgainst(expected):
		return "env"
	}
	return ""
}
