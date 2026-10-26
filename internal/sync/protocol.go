package sync

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// ProtocolVersion 3: the 1.3.0 object protocol. The object wire format is unchanged from v2, but 1.3.0 drops
// every compatibility path for older peers (pre-generation AI grant records, pre-envelope link settings), so
// v2 peers are rejected on every request instead of being merged with relaxed rules.
const ProtocolVersion = 3

// protocolHeader advertises the server's sync protocol on every /sync/v2 response, success or failure, so a
// client can tell an outdated server from an outdated client when a request is rejected.
const protocolHeader = "X-NexTerm-Sync-Protocol"

// errProtocolIncompatible classifies a rejected protocol handshake on either side.
var errProtocolIncompatible = errors.New("sync protocol incompatible")

// protocolMismatchError is the server-side rejection of a request carrying another protocol version.
func protocolMismatchError(got int) error {
	if got < ProtocolVersion {
		return ipc.WrapError(ipc.CodeUnsupported, fmt.Sprintf(
			"同步协议不兼容：此设备使用同步协议 %d，服务端要求协议 %d。NexTerm 1.3.0 起不再与 1.3.0 之前的设备同步，请将此设备升级到 1.3.0 或更高版本后再同步",
			got, ProtocolVersion), errProtocolIncompatible)
	}
	return ipc.WrapError(ipc.CodeUnsupported, fmt.Sprintf(
		"同步协议不兼容：此设备使用同步协议 %d，服务端仅支持协议 %d。请将服务端升级到与此设备相同或更高的 NexTerm 版本后再同步",
		got, ProtocolVersion), errProtocolIncompatible)
}

// remoteProtocolError maps a rejected sync request to a user-visible incompatibility error. The server's
// advertised protocol comes from protocolHeader; servers older than 1.3.0 do not send it.
func remoteProtocolError(header http.Header) error {
	advertised := strings.TrimSpace(header.Get(protocolHeader))
	server, err := strconv.Atoi(advertised)
	if advertised == "" || err != nil || server < ProtocolVersion {
		version := "1.3.0 之前的版本"
		if advertised != "" && err == nil {
			version = fmt.Sprintf("同步协议 %d", server)
		}
		return ipc.WrapError(ipc.CodeUnsupported, fmt.Sprintf(
			"同步协议不兼容：服务端为%s，此设备使用同步协议 %d。NexTerm 1.3.0 起不再与 1.3.0 之前的服务端或设备同步，请将服务端升级到 1.3.0 或更高版本",
			version, ProtocolVersion), errProtocolIncompatible)
	}
	return ipc.WrapError(ipc.CodeUnsupported, fmt.Sprintf(
		"同步协议不兼容：服务端使用同步协议 %d，此设备使用协议 %d。请将此设备升级到与服务端相同的 NexTerm 版本后再同步",
		server, ProtocolVersion), errProtocolIncompatible)
}

// responseProtocolError rejects a successful response that reports another protocol version.
func responseProtocolError(got int) error {
	if got == ProtocolVersion {
		return nil
	}
	header := http.Header{}
	header.Set(protocolHeader, strconv.Itoa(got))
	return remoteProtocolError(header)
}
