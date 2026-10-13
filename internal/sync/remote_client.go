package sync

import (
	"bytes"
	"compress/gzip"
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

// Session cookie and CSRF header are wire constants agreed with the internal/server account routes.
const (
	sessionCookieName = "nexterm_session"
	csrfHeaderName    = "X-NexTerm-CSRF"
	// accountStateResetRequired is the wire constant for the account view user.state (internal/account StateResetRequired).
	accountStateResetRequired = "reset_required"
)

// errSessionExpired means the server-side session is no longer valid; the client must re-login with the saved password.
var errSessionExpired = errors.New("sync session expired")

type remoteClient struct {
	base        string
	insecure    bool
	cookieToken string
	csrfToken   string
}

// RemoteConfig holds the remote account connection parameters needed for one sync.
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
		ID                 string `json:"id"`
		Username           string `json:"username"`
		State              string `json:"state"`
		MustChangePassword bool   `json:"must_change_password"`
	} `json:"user"`
	CSRFToken string `json:"csrf_token"`
}

// loginOutcome classifies the /auth/login 200 response: an established session (optionally with forced password change) or an MFA challenge.
// The MFA challenge carries no session cookie; the ticket is exchanged for a formal session via /auth/totp/login.
type loginOutcome struct {
	established   bool
	resetRequired bool
	mfaTicket     string
	mfaExpiresAt  int64
}

func (c *remoteClient) login(ctx context.Context, username, password string) (*loginOutcome, error) {
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeInternal, "无法编码登录请求", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/auth/login", bytes.NewReader(body))
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeBadParam, "同步链接不合法", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := syncHTTPClient(c.insecure).Do(request)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeDisconnected, "无法连接远端同步服务: "+err.Error(), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, accountRouteError(response)
	}
	var view struct {
		accountSessionView
		Ticket    string `json:"ticket"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		return nil, ipc.WrapError(ipc.CodeInternal, "远端登录响应结构无效", err)
	}
	if view.Ticket != "" {
		return &loginOutcome{mfaTicket: view.Ticket, mfaExpiresAt: view.ExpiresAt}, nil
	}
	if err := c.holdSession(response, view.CSRFToken); err != nil {
		return nil, err
	}
	return &loginOutcome{
		established:   true,
		resetRequired: view.User.State == accountStateResetRequired || view.User.MustChangePassword,
	}, nil
}

// loginTOTP exchanges the MFA challenge ticket + a TOTP code (or a one-time recovery code) for a formal session.
func (c *remoteClient) loginTOTP(ctx context.Context, ticket, code string) error {
	body, err := json.Marshal(map[string]string{"ticket": ticket, "code": code})
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "无法编码登录请求", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/auth/totp/login", bytes.NewReader(body))
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
		return ipc.WrapError(ipc.CodeInternal, "远端登录响应结构无效", err)
	}
	return c.holdSession(response, session.CSRFToken)
}

// holdSession takes the login response's session cookie and CSRF token into memory.
func (c *remoteClient) holdSession(response *http.Response, csrfToken string) error {
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
	c.csrfToken = csrfToken
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

func (c *remoteClient) ids(ctx context.Context, knownHead ...string) (*IDsResponse, error) {
	var response IDsResponse
	request := IDsRequest{Protocol: ProtocolVersion}
	if len(knownHead) > 0 {
		request.KnownHead = knownHead[0]
	}
	status, rpcErr, err := c.post(ctx, "/sync/v2/ids", request, &response, false)
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

// request executes a JSON request with any method and preserves the HTTP status, same semantics as do:
// err covers transport-level failures only; business failures return status code + rpcErr. Write requests automatically carry the CSRF header.
func (c *remoteClient) request(ctx context.Context, method, path string, payload, out any) (int, *ipc.Error, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, ipc.WrapError(ipc.CodeInternal, "无法编码远端请求", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return 0, nil, ipc.WrapError(ipc.CodeBadParam, "同步链接不合法", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	return c.do(request, out, true)
}

// api is the unified exit for typed remote methods: 200 decodes success, 401 goes to the caller's re-login flow,
// other statuses map to ipc.Error.
func (c *remoteClient) api(ctx context.Context, method, path string, payload, out any) error {
	status, rpcErr, err := c.request(ctx, method, path, payload, out)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return errSessionExpired
	default:
		return remoteAPIError(status, rpcErr)
	}
}

// remoteAPIError maps failures of remote account/fleet/sharing routes to ipc.Error:
// server-issued rpcErr codes pass through verbatim (their message is already the final user-facing text),
// otherwise failures are classified by status code.
func remoteAPIError(status int, rpcErr *ipc.Error) error {
	if rpcErr != nil {
		return ipc.NewError(rpcErr.Code, rpcErr.Message)
	}
	switch {
	case status == http.StatusBadRequest:
		return ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("远端服务拒绝了请求参数(HTTP %d)", status))
	case status == http.StatusForbidden || status == http.StatusTooManyRequests:
		return ipc.NewError(ipc.CodeForbidden, fmt.Sprintf("远端服务拒绝了请求(HTTP %d)", status))
	case status == http.StatusNotFound:
		return ipc.NewError(ipc.CodeNotFound, fmt.Sprintf("远端资源不存在或该功能未启用(HTTP %d)", status))
	case status >= http.StatusInternalServerError:
		return ipc.NewError(ipc.CodeDisconnected, fmt.Sprintf("远端服务暂不可用(HTTP %d)", status))
	default:
		return ipc.NewError(ipc.CodeInternal, fmt.Sprintf("远端服务返回 HTTP %d", status))
	}
}

func (c *remoteClient) post(ctx context.Context, path string, payload, out any, csrf bool) (int, *ipc.Error, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, ipc.WrapError(ipc.CodeInternal, "无法编码同步请求", err)
	}
	wireBody := body
	contentEncoding := ""
	if len(body) >= 1024 {
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(body); err != nil {
			return 0, nil, ipc.WrapError(ipc.CodeInternal, "无法压缩同步请求", err)
		}
		if err := writer.Close(); err != nil {
			return 0, nil, ipc.WrapError(ipc.CodeInternal, "无法压缩同步请求", err)
		}
		if compressed.Len() < len(body) {
			wireBody = compressed.Bytes()
			contentEncoding = "gzip"
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(wireBody))
	if err != nil {
		return 0, nil, ipc.WrapError(ipc.CodeBadParam, "同步链接不合法", err)
	}
	if contentEncoding != "" {
		request.Header.Set("Content-Encoding", contentEncoding)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	return c.do(request, out, csrf)
}

// do executes the request and preserves the HTTP status: err covers transport-level failures only; business failures return status code + rpcErr,
// so semantic statuses like 409 reach the caller's mapping logic (e.g. errHeadMismatch).
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
			return 0, nil, ipc.WrapError(ipc.CodeInternal, "远端同步响应数据结构无效", err)
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

// Sync HTTP clients are cached and reused by insecure: the Transport holds a connection pool; creating one per request would exhaust ports and lose keep-alive.
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
