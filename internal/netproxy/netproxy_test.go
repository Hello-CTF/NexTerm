package netproxy

import (
	"net/http"
	"testing"
	"time"

	"golang.org/x/net/http/httpproxy"
)

const scutilOutput = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254.0.0/16
    2 : intranet.example
  }
  ExcludeSimpleHostnames : 1
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 0
  HTTPSPort : 7891
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 7892
  SOCKSProxy : 127.0.0.1
}
`

func TestParseScutil(t *testing.T) {
	got := parseScutil(scutilOutput)
	want := httpproxy.Config{
		HTTPProxy:  "http://127.0.0.1:7890",
		HTTPSProxy: "socks5://127.0.0.1:7892",
		NoProxy:    ".local,169.254.0.0/16,intranet.example",
	}
	if got != want {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
	if got := parseScutil("<dictionary> {\n  HTTPEnable : 0\n}\n"); got != (httpproxy.Config{}) {
		t.Fatalf("disabled proxies = %+v, want none", got)
	}
}

func proxyFor(t *testing.T, r *resolver, target string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := r.proxy(request)
	if err != nil {
		t.Fatal(err)
	}
	if proxy == nil {
		return ""
	}
	return proxy.String()
}

func clearProxyEnv(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"} {
		t.Setenv(name, "")
	}
}

func TestEnvironmentComesFirst(t *testing.T) {
	clearProxyEnv(t)
	systemCalls := 0
	r := &resolver{system: func() httpproxy.Config {
		systemCalls++
		return httpproxy.Config{HTTPSProxy: "http://system:1"}
	}, now: time.Now}

	t.Setenv("ALL_PROXY", "socks5://all:1080")
	t.Setenv("HTTP_PROXY", "http://env:8080")
	t.Setenv("NO_PROXY", "github.internal")
	if got := proxyFor(t, r, "http://example.com"); got != "http://env:8080" {
		t.Fatalf("http proxy = %q", got)
	}
	// ALL_PROXY covers the scheme the specific variables leave out.
	if got := proxyFor(t, r, "https://example.com"); got != "socks5://all:1080" {
		t.Fatalf("https proxy = %q", got)
	}
	if got := proxyFor(t, r, "https://github.internal"); got != "" {
		t.Fatalf("NO_PROXY host went through %q", got)
	}
	if systemCalls != 0 {
		t.Fatal("the system proxy was read although the environment sets one")
	}
}

func TestSystemProxyAppliesWithoutEnvironmentAndIsCached(t *testing.T) {
	clearProxyEnv(t)
	now := time.Unix(0, 0)
	systemCalls := 0
	r := &resolver{system: func() httpproxy.Config {
		systemCalls++
		return httpproxy.Config{HTTPSProxy: "http://system:3128", NoProxy: "intranet.example"}
	}, now: func() time.Time { return now }}

	if got := proxyFor(t, r, "https://github.com"); got != "http://system:3128" {
		t.Fatalf("proxy = %q, want the system proxy", got)
	}
	if got := proxyFor(t, r, "https://intranet.example"); got != "" {
		t.Fatalf("bypassed host went through %q", got)
	}
	if got := proxyFor(t, r, "https://127.0.0.1"); got != "" {
		t.Fatalf("loopback went through %q", got)
	}
	if systemCalls != 1 {
		t.Fatalf("system read %d times within the cache period, want 1", systemCalls)
	}
	now = now.Add(systemCacheTTL)
	proxyFor(t, r, "https://github.com")
	if systemCalls != 2 {
		t.Fatalf("system read %d times after the cache period, want 2", systemCalls)
	}
}
