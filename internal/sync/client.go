package sync

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type Client struct {
	service *Service
}

func NewClient(service *Service) *Client {
	return &Client{service: service}
}

type rpcWireResponse struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *ipc.Error      `json:"error"`
}

func (c *Client) RemoteDigest(ctx context.Context) (digest Digest, returnErr error) {
	defer func() { c.service.recordProbe(context.WithoutCancel(ctx), returnErr) }()
	err := c.call(ctx, CommandDigest, struct{}{}, &digest)
	return digest, err
}

func (c *Client) Push(ctx context.Context, request PushRequest) (ImportReport, error) {
	bundle, err := c.service.Export(ctx, ExportRequest{
		AssetIDs: request.AssetIDs, WithCredentials: request.WithCredentials,
	})
	if err != nil {
		return ImportReport{}, err
	}
	if len(bundle.Assets) == 0 && len(bundle.Snippets) == 0 {
		return ImportReport{Warnings: append([]string{}, bundle.Warnings...)}, nil
	}
	var report ImportReport
	if err := c.call(ctx, CommandImport, ImportRequest{Bundle: bundle, Force: request.Force}, &report); err != nil {
		return ImportReport{}, err
	}
	return report, nil
}

func (c *Client) Pull(ctx context.Context, request PullRequest) (ImportReport, error) {
	var bundle Bundle
	if err := c.call(ctx, CommandExport, ExportRequest{
		AssetIDs: request.AssetIDs, WithCredentials: request.WithCredentials,
	}, &bundle); err != nil {
		return ImportReport{}, err
	}
	return c.service.Import(ctx, ImportRequest{Bundle: bundle, Force: request.Force})
}

func (c *Client) call(ctx context.Context, command string, args, result any) error {
	link, err := c.service.LinkGet(ctx)
	if err != nil {
		return err
	}
	if !link.IsConfigured() {
		return ipc.NewError(ipc.CodeBadParam, "同步链接需要同时配置 URL 和令牌")
	}
	base, err := normalizeBase(link.URL)
	if err != nil {
		return err
	}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "无法编码同步请求参数", err)
	}
	if command == CommandExport || command == CommandImport {
		encodedArgs, err = json.Marshal(struct {
			Args json.RawMessage `json:"args"`
		}{Args: encodedArgs})
		if err != nil {
			return ipc.WrapError(ipc.CodeInternal, "无法编码同步嵌套参数", err)
		}
	}
	body, err := json.Marshal(ipc.Request{Command: command, Args: encodedArgs})
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "无法编码同步请求", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/sync/rpc", bytes.NewReader(body))
	if err != nil {
		return ipc.WrapError(ipc.CodeBadParam, "同步链接不合法", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(TokenHeader, link.Token)

	response, err := syncHTTPClient(link.Insecure).Do(req)
	if err != nil {
		return ipc.WrapError(ipc.CodeDisconnected, "无法连接远端同步服务: "+err.Error(), err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		location := response.Header.Get("Location")
		if strings.Contains(strings.ToLower(location), "/sys/login") {
			return ipc.NewError(ipc.CodeForbidden, "远端被平台登录页拦截，请先在平台中登录或使用已公开的对端同步地址")
		}
		return ipc.NewError(ipc.CodeInternal, "远端同步地址发生重定向，请检查 NexTerm 对端地址（同步客户端不会自动跟随重定向）")
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, ipc.DefaultMaxRPCBytes+1))
	if err != nil {
		return ipc.WrapError(ipc.CodeDisconnected, "读取远端同步响应失败", err)
	}
	if int64(len(raw)) > ipc.DefaultMaxRPCBytes {
		return ipc.NewError(ipc.CodeBadParam, "远端同步响应超过 256 MiB 限制")
	}
	var envelope rpcWireResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		if response.StatusCode == http.StatusUnauthorized {
			return forbiddenTokenError()
		}
		return ipc.WrapError(ipc.CodeInternal,
			fmt.Sprintf("远端返回了非 JSON 响应（HTTP %d）", response.StatusCode), err)
	}
	if response.StatusCode == http.StatusUnauthorized {
		return forbiddenTokenError()
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if envelope.Error != nil {
			return remoteError(envelope.Error)
		}
		return ipc.NewError(ipc.CodeInternal, fmt.Sprintf("远端同步服务返回 HTTP %d", response.StatusCode))
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return ipc.NewError(ipc.CodeInternal, "远端同步响应缺少错误信息")
		}
		return remoteError(envelope.Error)
	}
	if result != nil {
		if len(envelope.Data) == 0 {
			return ipc.NewError(ipc.CodeInternal, "远端同步响应缺少 data")
		}
		if err := json.Unmarshal(envelope.Data, result); err != nil {
			return ipc.WrapError(ipc.CodeInternal, "远端同步响应数据形状不合法", err)
		}
	}
	return nil
}

func forbiddenTokenError() error {
	return ipc.NewError(ipc.CodeForbidden, "远端同步令牌无效，请检查服务器同步设置并更新链接令牌")
}

func remoteError(remote *ipc.Error) error {
	mapped := ipc.NewError(remote.Code, "远端同步失败: "+remote.Message)
	if remote.Detail != nil {
		mapped.Detail = remote.Detail
	}
	return mapped
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
		return "", ipc.NewError(ipc.CodeForbidden, "公网地址必须使用 HTTPS；同步令牌和凭据不能通过明文 HTTP 传输")
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

func syncHTTPClient(insecure bool) *http.Client {
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
		Timeout:   60 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
