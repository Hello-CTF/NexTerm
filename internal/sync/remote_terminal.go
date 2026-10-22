package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	urlpkg "net/url"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/coder/websocket"
)

// The device terminal bridge (GET /fleet/devices/{id}/bridge) and the remote session feed endpoint (GET
// /share/remote-sessions/{id}/feed) share one WS dial helper carrying session credentials: the handshake
// carries both the session cookie and the CSRF header (the feed endpoint enforces CSRF), auth completes during the upgrade,
// and the server does not re-check the session after connecting, so dialing only completes the handshake inside the remoteCall lock; the returned
// connection lives independently and does not hold opMu long-term.

// DialDeviceBridge completes the WS upgrade against the server device bridge endpoint with the logged-in session and returns a byte-stream connection;
// purpose selects the bridge kind ("terminal"/"file"/"forward"/"listen"/"accept", empty means terminal). 401 goes through remoteCall's dropSession re-login loop,
// same semantics as the typed remote methods.
func (p *RemoteProxy) DialDeviceBridge(ctx context.Context, deviceID, purpose string) (net.Conn, error) {
	if deviceID == "" {
		return nil, ipc.NewError(ipc.CodeBadParam, "deviceId 不能为空")
	}
	var conn net.Conn
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		dialed, err := client.dialDeviceBridge(ctx, deviceID, purpose)
		if err != nil {
			return err
		}
		conn = dialed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// RemoteSessionFeedDial completes the WS upgrade against the remote shell feed endpoint with the logged-in session, returning a message-level
// connection: binary frames carry output bytes verbatim, text frames are control frames ({"type":"end"}/{"type":"resize"}).
// 401 goes through remoteCall's dropSession re-login loop, same semantics as the typed remote methods.
func (p *RemoteProxy) RemoteSessionFeedDial(ctx context.Context, sessionID string) (*websocket.Conn, error) {
	if sessionID == "" {
		return nil, ipc.NewError(ipc.CodeBadParam, "sessionId 不能为空")
	}
	var conn *websocket.Conn
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		dialed, err := client.dialRemoteWS(ctx, "/share/remote-sessions/"+urlpkg.PathEscape(sessionID)+"/feed", "远程会话推流端点")
		if err != nil {
			return err
		}
		conn = dialed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// RemoteHostShareIdentity scopes a desktop host-share connection to one normalized server, account and login session generation.
type RemoteHostShareIdentity struct {
	ServerURL  string `json:"serverUrl"`
	UserID     string `json:"userId"`
	Generation string `json:"generation"`
}

func hostShareSessionGeneration(client *remoteClient) string {
	if client == nil || client.cookieToken == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(client.cookieToken))
	return hex.EncodeToString(digest[:])
}

// HostShareIdentity reads the current link and cached account session. Generation is only a one-way digest of the
// current session credential; the raw cookie is never returned or persisted in this identity.
func (p *RemoteProxy) HostShareIdentity(ctx context.Context) (*RemoteHostShareIdentity, error) {
	if err := p.service.requireDesktop(); err != nil {
		return nil, err
	}
	link, err := p.service.LinkGet(ctx)
	if err != nil {
		return nil, err
	}
	base, err := normalizeBase(link.URL)
	if err != nil {
		return nil, err
	}
	p.service.engine.sessionMu.Lock()
	var sessionKey, userID, generation string
	if p.service.engine.session != nil {
		sessionKey = p.service.engine.session.key
		userID = p.service.engine.session.userID
		generation = hostShareSessionGeneration(p.service.engine.session.client)
	}
	p.service.engine.sessionMu.Unlock()
	if !link.IsConfigured() || userID == "" || generation == "" || sessionKey != link.URL+"\x00"+link.Username {
		return nil, ipc.NewError(ipc.CodeForbidden, "请先登录服务端账号")
	}
	current, err := p.service.LinkGet(ctx)
	if err != nil {
		return nil, err
	}
	if current.URL != link.URL || current.Username != link.Username {
		return nil, ipc.NewError(ipc.CodeDisconnected, "账号配置正在变化，请重试")
	}
	return &RemoteHostShareIdentity{ServerURL: base, UserID: userID, Generation: generation}, nil
}

// DialHostShareTerminal opens the dedicated registered-recipient terminal route. It never uses the ordinary device
// bridge, so the server authorizes the share and keeps HostGate enforcement on both directions.
func (p *RemoteProxy) DialHostShareTerminal(ctx context.Context, deviceID string) (*websocket.Conn, error) {
	if deviceID == "" {
		return nil, ipc.NewError(ipc.CodeBadParam, "deviceId 不能为空")
	}
	var conn *websocket.Conn
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		dialed, err := client.dialHostShareTerminal(ctx, deviceID)
		if err != nil {
			return err
		}
		conn = dialed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (c *remoteClient) dialHostShareTerminal(ctx context.Context, deviceID string) (*websocket.Conn, error) {
	conn, err := c.dialRemoteWS(ctx, "/share/devices/"+urlpkg.PathEscape(deviceID)+"/terminal", "共享主机终端")
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(1 << 20)
	return conn, nil
}

func (c *remoteClient) dialDeviceBridge(ctx context.Context, deviceID, purpose string) (net.Conn, error) {
	path := "/fleet/devices/" + urlpkg.PathEscape(deviceID) + "/bridge"
	if purpose != "" {
		path += "?purpose=" + urlpkg.QueryEscape(purpose)
	}
	conn, err := c.dialRemoteWS(ctx, path, "远端设备桥")
	if err != nil {
		return nil, err
	}
	// deviceproto frames can reach 256KiB+overhead on the wire; the default 32KiB read limit would tear the bridge.
	conn.SetReadLimit(1 << 20)
	return websocket.NetConn(context.Background(), conn, websocket.MessageBinary), nil
}

// dialRemoteWS completes the WS upgrade against path with the in-memory session credentials (cookie+CSRF); handshake failures
// map to ipc.Error (401 triggers remoteCall re-login, others pass through the rpcErr final text).
func (c *remoteClient) dialRemoteWS(ctx context.Context, path, endpoint string) (*websocket.Conn, error) {
	header := http.Header{}
	if c.cookieToken != "" {
		header.Set("Cookie", sessionCookieName+"="+c.cookieToken)
	}
	if c.csrfToken != "" {
		header.Set(csrfHeaderName, c.csrfToken)
	}
	wsClient := &http.Client{
		Transport:     syncHTTPClient(c.insecure).Transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	conn, response, err := websocket.Dial(ctx, c.base+path, &websocket.DialOptions{HTTPClient: wsClient, HTTPHeader: header})
	if err != nil {
		if response != nil {
			return nil, wsUpgradeError(response, endpoint)
		}
		return nil, ipc.WrapError(ipc.CodeDisconnected, "无法连接"+endpoint+": "+err.Error(), err)
	}
	return conn, nil
}

// wsUpgradeError maps a failed WS endpoint upgrade HTTP response to ipc.Error: 401 triggers
// remoteCall's re-login, other statuses preferentially pass through the server rpcErr final text.
func wsUpgradeError(response *http.Response, endpoint string) error {
	if response.StatusCode == http.StatusUnauthorized {
		return errSessionExpired
	}
	var failure struct {
		Error *ipc.Error `json:"error"`
	}
	if body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20)); err == nil {
		if json.Unmarshal(body, &failure) == nil && failure.Error != nil {
			return ipc.NewError(failure.Error.Code, failure.Error.Message)
		}
	}
	return ipc.NewError(ipc.CodeDisconnected, fmt.Sprintf("%s暂不可用（HTTP %d）", endpoint, response.StatusCode))
}
