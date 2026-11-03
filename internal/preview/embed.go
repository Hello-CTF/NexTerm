// Package preview backs the in-app browser: it tells the frontend whether a page may be shown in an iframe.
package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Check results.
const (
	StatusEmbeddable  = "embeddable"
	StatusBlocked     = "blocked"
	StatusUnreachable = "unreachable"
)

const (
	checkTimeout  = 5 * time.Second
	maxRedirects  = 5
	maxBodyToRead = 64 << 10
)

// EmbedCheck is the outcome of CheckEmbeddable.
type EmbedCheck struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// CheckEmbeddable asks the target which framing policy it sends. The embedding page is the app origin,
// which never matches the target, so any frame-ancestors list without a wildcard blocks it.
func CheckEmbeddable(ctx context.Context, client *http.Client, rawURL string) (EmbedCheck, error) {
	return checkEmbeddable(ctx, client, rawURL, checkTimeout)
}

func checkEmbeddable(ctx context.Context, client *http.Client, rawURL string, timeout time.Duration) (EmbedCheck, error) {
	target, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" {
		return EmbedCheck{}, errors.New("仅支持 http 和 https 地址")
	}
	if target.User != nil {
		return EmbedCheck{}, errors.New("地址不允许包含账号密码")
	}

	// A copy keeps the caller's transport but enforces our redirect and cookie policy.
	checker := http.Client{}
	if client != nil {
		checker = *client
	}
	checker.Jar = nil
	checker.Timeout = 0
	checker.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return errors.New("重定向次数过多")
		}
		if next.URL.Scheme != "http" && next.URL.Scheme != "https" {
			return errors.New("重定向到了不支持的协议")
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := fetchHeaders(ctx, &checker, http.MethodHead, target.String())
	if err != nil || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
		if resp != nil {
			_ = resp.Body.Close()
		}
		resp, err = fetchHeaders(ctx, &checker, http.MethodGet, target.String())
		if err != nil {
			return EmbedCheck{Status: StatusUnreachable, Reason: "无法连接到该网站"}, nil
		}
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, maxBodyToRead)

	if reason := blockReason(resp.Header); reason != "" {
		return EmbedCheck{Status: StatusBlocked, Reason: reason}, nil
	}
	return EmbedCheck{Status: StatusEmbeddable}, nil
}

func fetchHeaders(ctx context.Context, client *http.Client, method, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "NexTerm-Preview/1")
	return client.Do(req)
}

func blockReason(header http.Header) string {
	for _, value := range header.Values("X-Frame-Options") {
		for _, part := range strings.Split(value, ",") {
			switch strings.ToUpper(strings.TrimSpace(part)) {
			case "DENY", "SAMEORIGIN":
				return "该网站通过 X-Frame-Options 拒绝嵌入"
			}
		}
	}
	for _, value := range header.Values("Content-Security-Policy") {
		for _, policy := range strings.Split(value, ",") {
			if frameAncestorsRestricted(policy) {
				return "该网站通过 Content-Security-Policy 的 frame-ancestors 拒绝嵌入"
			}
		}
	}
	return ""
}

// frameAncestorsRestricted reports whether one policy has a frame-ancestors directive without a wildcard.
func frameAncestorsRestricted(policy string) bool {
	for _, directive := range strings.Split(policy, ";") {
		fields := strings.Fields(directive)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "frame-ancestors") {
			continue
		}
		for _, source := range fields[1:] {
			if source == "*" {
				return false
			}
		}
		return true
	}
	return false
}
