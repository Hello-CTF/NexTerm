package sync

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// Gateway auth is the internal link credential between the daemon and the server, unrelated to the token era, kept independent of the sync protocol.
const (
	GatewayAuthHeader = "X-NexTerm-Gateway-Auth"
)

func GatewayAuthorized(r *http.Request, key string) bool {
	if key == "" {
		return false
	}
	presented := r.Header.Get(GatewayAuthHeader)
	if presented == "" {
		return false
	}
	expectedDigest := sha256.Sum256([]byte(key))
	presentedDigest := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(expectedDigest[:], presentedDigest[:]) == 1
}
