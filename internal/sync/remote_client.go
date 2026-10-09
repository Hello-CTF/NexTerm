package sync

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// 会话 cookie 与 CSRF 头是与 internal/server 账号路由约定的线上常量。
const (
	sessionCookieName = "nexterm_session"
	csrfHeaderName    = "X-NexTerm-CSRF"
)

// errSessionExpired 表示服务端会话已失效, 客户端需要用保存的口令重新登录。
var errSessionExpired = errors.New("sync session expired")

type remoteClient struct {
	base        string
	insecure    bool
	cookieToken string
	csrfToken   string
}

// RemoteConfig 是一次同步所需的远端账号连接参数。
type RemoteConfig struct {
	URL      string
	Username string
	Password string
	Insecure bool
}

func newRemoteClient(config RemoteConfig) (*remoteClient, error) {
	base, err := normalizeBase(config.URL)
	if err != nil {
		return nil, err
	}
	return &remoteClient{base: base, insecure: config.Insecure}, nil
}

type accountSessionView struct {
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
	CSRFToken string `json:"csrf_token"`
}

func (c *remoteClient) login(ctx context.Context, username, password string) error {
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "无法编码登录请求", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/auth/login", bytes.NewReader(body))
	if err != nil {
		return ipc.WrapError(ipc.CodeBadParam, "同步链接不合法", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := syncHTTPClient(c.insecure).Do(request)
	if err != nil {
		return ipc.WrapError(ipc.CodeDisconnected, "无法连接远端同步服务: "+err.Error(), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return accountRouteError(response)
	}
	var session accountSessionView
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		return ipc.WrapError(ipc.CodeInternal, "远端登录响应形状不合法", err)
	}
	cookie := ""
	for _, candidate := range response.Cookies() {
		if candidate.Name == sessionCookieName {
			cookie = candidate.Value
		}
	}
	if cookie == "" {
		return ipc.NewError(ipc.CodeInternal, "远端登录响应缺少会话 cookie")
	}
	c.cookieToken = cookie
	c.csrfToken = session.CSRFToken
	return nil
}

func (c *remoteClient) me(ctx context.Context) (string, error) {
	var session accountSessionView
	if err := c.get(ctx, "/auth/me", &session); err != nil {
		return "", err
	}
	if session.User.ID == "" {
		return "", ipc.NewError(ipc.CodeInternal, "远端会话响应缺少用户 ID")
	}
	c.csrfToken = session.CSRFToken
	return session.User.ID, nil
}

func (c *remoteClient) dekEnvelopes(ctx context.Context) (*vault.UserDEKEnvelopes, error) {
	var view struct {
		DEKEnvelope      []byte `json:"dek_envelope"`
		KDFSalt          []byte `json:"kdf_salt"`
		KDFParams        string `json:"kdf_params"`
		RecoveryEnvelope []byte `json:"recovery_envelope"`
		RecoveryHash     string `json:"recovery_hash"`
	}
	if err := c.get(ctx, "/auth/dek", &view); err != nil {
		return nil, err
	}
	return &vault.UserDEKEnvelopes{
		DEKEnvelope: view.DEKEnvelope, KDFSalt: view.KDFSalt, KDFParams: view.KDFParams,
		RecoveryEnvelope: view.RecoveryEnvelope, RecoveryHash: view.RecoveryHash,
	}, nil
}

func (c *remoteClient) push(ctx context.Context, knownHead string, objects []WireObject) (*PushResponse, error) {
	var response PushResponse
	status, rpcErr, err := c.post(ctx, "/sync/v2/push", PushRequest{Protocol: ProtocolVersion, KnownHead: knownHead, Objects: objects}, &response, true)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return &response, nil
	case http.StatusUnauthorized:
		return nil, errSessionExpired
	case http.StatusConflict:
		return nil, errHeadMismatch
	default:
		return nil, remoteStatusError(status, rpcErr)
	}
}

func (c *remoteClient) pull(ctx context.Context, sinceSeq int64, ids []string, maxBytes int64) (*PullResponse, error) {
	var response PullResponse
	status, rpcErr, err := c.post(ctx, "/sync/v2/pull", PullRequest{
		Protocol: ProtocolVersion, SinceSeq: sinceSeq, IDs: ids, MaxBytes: maxBytes,
	}, &response, false)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return &response, nil
	case http.StatusUnauthorized:
		return nil, errSessionExpired
	default:
		return nil, remoteStatusError(status, rpcErr)
	}
}

func (c *remoteClient) ids(ctx context.Context) (*IDsResponse, error) {
	var response IDsResponse
	status, rpcErr, err := c.post(ctx, "/sync/v2/ids", IDsRequest{Protocol: ProtocolVersion}, &response, false)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return &response, nil
	case http.StatusUnauthorized:
		return nil, errSessionExpired
	default:
		return nil, remoteStatusError(status, rpcErr)
	}
}

func (c *remoteClient) get(ctx context.Context, path string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return ipc.WrapError(ipc.CodeBadParam, "同步链接不合法", err)
	}
	status, rpcErr, err := c.do(request, out, false)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return errSessionExpired
	default:
		return remoteStatusError(status, rpcErr)
	}
}

func (c *remoteClient) post(ctx context.Context, path string, payload, out any, csrf bool) (int, *ipc.Error, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, ipc.WrapError(ipc.CodeInternal, "无法编码同步请求", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, ipc.WrapError(ipc.CodeBadParam, "同步链接不合法", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	return c.do(request, out, csrf)
}

// do 执行请求并保留 HTTP 状态: err 仅覆盖传输层失败, 业务失败以状态码+rpcErr 返回,
// 保证 409 等语义状态能到达调用方的映射逻辑(如 errHeadMismatch)。
func (c *remoteClient) do(request *http.Request, out any, csrf bool) (int, *ipc.Error, error) {
	if c.cookieToken != "" {
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: c.cookieToken})
	}
	if csrf && c.csrfToken != "" {
		request.Header.Set(csrfHeaderName, c.csrfToken)
	}
	response, err := syncHTTPClient(c.insecure).Do(request)
	if err != nil {
		return 0, nil, ipc.WrapError(ipc.CodeDisconnected, "无法连接远端同步服务: "+err.Error(), err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxPullWireBytes+(1<<20)+1))
	if err != nil {
		return 0, nil, ipc.WrapError(ipc.CodeDisconnected, "读取远端同步响应失败", err)
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error *ipc.Error `json:"error"`
		}
		if err := json.Unmarshal(raw, &failure); err == nil && failure.Error != nil {
			return response.StatusCode, failure.Error, nil
		}
		return response.StatusCode, nil, nil
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return 0, nil, ipc.WrapError(ipc.CodeInternal, "远端同步响应数据形状不合法", err)
		}
	}
	return response.StatusCode, nil, nil
}

func remoteStatusError(status int, rpcErr *ipc.Error) error {
	if rpcErr != nil {
		return ipc.NewError(rpcErr.Code, "远端同步失败: "+rpcErr.Message)
	}
	return ipc.NewError(ipc.CodeInternal, fmt.Sprintf("远端同步服务返回 HTTP %d", status))
}

func accountRouteError(response *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return ipc.WrapError(ipc.CodeDisconnected, "读取远端登录响应失败", err)
	}
	var failure struct {
		Error *ipc.Error `json:"error"`
	}
	if err := json.Unmarshal(raw, &failure); err == nil && failure.Error != nil {
		return ipc.NewError(failure.Error.Code, "远端登录失败: "+failure.Error.Message)
	}
	return ipc.NewError(ipc.CodeForbidden, "远端登录失败")
}

func normalizeBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", ipc.WrapError(ipc.CodeBadParam, "同步链接 URL 不合法", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" {
		return "", ipc.NewError(ipc.CodeBadParam, "同步链接必须显式使用 http:// 或 https://，并包含主机名")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return "", ipc.NewError(ipc.CodeBadParam, "同步链接不能包含用户信息、查询参数或片段")
	}
	parsed.Scheme = scheme
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	if scheme == "http" && !isLocalOrPrivate(parsed.Hostname()) {
		return "", ipc.NewError(ipc.CodeForbidden, "公网地址必须使用 HTTPS；同步口令和凭据不能通过明文 HTTP 传输")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isLocalOrPrivate(hostname string) bool {
	hostname = strings.ToLower(strings.TrimSuffix(hostname, "."))
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || strings.HasSuffix(hostname, ".local") {
		return true
	}
	address, err := netip.ParseAddr(hostname)
	if err != nil {
		return false
	}
	address = address.Unmap()
	return address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast()
}

// 同步客户端按 insecure 缓存复用: Transport 内含连接池, 每请求新建会耗尽端口并丢失 keep-alive。
var (
	secureSyncHTTPClient   = newSyncHTTPClient(false)
	insecureSyncHTTPClient = newSyncHTTPClient(true)
)

func syncHTTPClient(insecure bool) *http.Client {
	if insecure {
		return insecureSyncHTTPClient
	}
	return secureSyncHTTPClient
}

func newSyncHTTPClient(insecure bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.InsecureSkipVerify = insecure
	return &http.Client{
		Transport: transport,
		Timeout:   120 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
