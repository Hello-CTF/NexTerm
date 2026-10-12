package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	urlpkg "net/url"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/coder/websocket"
)

// 设备终端桥 (GET /fleet/devices/{id}/bridge) 与远程会话推流端点 (GET
// /share/remote-sessions/{id}/feed) 共用一个带会话凭据的 WS 拨号助手: 握手
// 同时携带会话 cookie 与 CSRF 头 (推流端点强制校验 CSRF), 鉴权在升级时完成,
// 建连后服务端不再复查会话, 因此拨号只在 remoteCall 锁内完成握手, 返回的
// 连接独立存活, 不长期占用 opMu。

// DialDeviceBridge 用已登录会话对服务端设备桥端点完成 WS 升级并返回字节流连接;
// 401 走 remoteCall 的 dropSession 重登环, 与类型化远端方法同一语义。
func (p *RemoteProxy) DialDeviceBridge(ctx context.Context, deviceID string) (net.Conn, error) {
	if deviceID == "" {
		return nil, ipc.NewError(ipc.CodeBadParam, "deviceId 不能为空")
	}
	var conn net.Conn
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		dialed, err := client.dialDeviceBridge(ctx, deviceID)
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

// RemoteSessionFeedDial 用已登录会话对远程壳推流端点完成 WS 升级, 返回消息级
// 连接: 二进制帧 = 输出字节原样, 文本帧 = 控制帧 ({"type":"end"}/{"type":"resize"})。
// 401 走 remoteCall 的 dropSession 重登环, 与类型化远端方法同一语义。
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

func (c *remoteClient) dialDeviceBridge(ctx context.Context, deviceID string) (net.Conn, error) {
	conn, err := c.dialRemoteWS(ctx, "/fleet/devices/"+urlpkg.PathEscape(deviceID)+"/bridge", "远端设备桥")
	if err != nil {
		return nil, err
	}
	return websocket.NetConn(context.Background(), conn, websocket.MessageBinary), nil
}

// dialRemoteWS 用内存中的会话凭据 (cookie+CSRF) 对 path 完成 WS 升级; 握手失败
// 映射成 ipc.Error (401 触发 remoteCall 重登, 其余透传 rpcErr 终态文案)。
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

// wsUpgradeError 把 WS 端点升级失败的 HTTP 响应映射成 ipc.Error: 401 触发
// remoteCall 的重登, 其余状态优先透传服务端 rpcErr 的终态文案。
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
	return ipc.NewError(ipc.CodeDisconnected, fmt.Sprintf("%s暂不可用(HTTP %d)", endpoint, response.StatusCode))
}
