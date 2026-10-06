package sync

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// 网关认证是守护进程与服务端之间的内部链路凭据, 与令牌时代无关, 独立于同步协议保留。
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
