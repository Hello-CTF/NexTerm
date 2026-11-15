package sync

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/netproxy"
)

// forwardProxy is an HTTP forward proxy that requires u:p and counts the requests it relays.
func forwardProxy(t *testing.T) (*atomic.Int64, netproxy.Manual) {
	t.Helper()
	var relayed atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if username, password, ok := parseProxyAuth(r); !ok || username != "u" || password != "p" {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		relayed.Add(1)
		outbound := r.Clone(r.Context())
		outbound.RequestURI = ""
		outbound.Header.Del("Proxy-Authorization")
		response, err := http.DefaultTransport.RoundTrip(outbound)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			w.Header()[key] = values
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(server.Close)
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	return &relayed, netproxy.Manual{Type: netproxy.TypeHTTP, Host: host, Port: port, Username: "u", Password: "p"}
}

func parseProxyAuth(r *http.Request) (string, string, bool) {
	header := r.Header.Get("Proxy-Authorization")
	if header == "" {
		return "", "", false
	}
	probe := &http.Request{Header: http.Header{"Authorization": {header}}}
	return probe.BasicAuth()
}

func proxyPatch(proxy netproxy.Manual, password *string) *LinkProxyPatch {
	return &LinkProxyPatch{Type: proxy.Type, Host: proxy.Host, Port: proxy.Port, Username: proxy.Username, Password: password}
}

func TestLinkSetRoutesAccountThroughManualProxy(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	relayed, proxy := forwardProxy(t)
	ctx := context.Background()

	patch := linkPatch(fixture.URL, "alice", "alice-pw-123")
	patch.Proxy = proxyPatch(proxy, testPtr(proxy.Password))
	result, err := instance.service.LinkSet(ctx, patch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectConnected {
		t.Fatalf("connect = %q (%s), want connected through the proxy", result.Connect, result.LastError)
	}
	if relayed.Load() == 0 {
		t.Fatal("login did not go through the proxy")
	}
	wantView := LinkProxy{Type: proxy.Type, Host: proxy.Host, Port: proxy.Port, Username: "u", HasPassword: true}
	if result.Proxy != wantView {
		t.Fatalf("proxy view = %+v, want %+v without the password", result.Proxy, wantView)
	}
	if saved, err := instance.service.LinkProxy(ctx); err != nil || saved != proxy {
		t.Fatalf("LinkProxy = %+v %v, want %+v", saved, err, proxy)
	}

	before := relayed.Load()
	if _, err := instance.service.Remote().ListDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if relayed.Load() == before {
		t.Fatal("remote calls of the session bypassed the proxy")
	}

	// Removing the proxy is a changed connection: the next call logs in again, directly.
	logins := fixture.logins.Load()
	if _, err := instance.service.LinkSet(ctx, LinkPatch{Proxy: &LinkProxyPatch{}}); err != nil {
		t.Fatal(err)
	}
	if fixture.logins.Load() == logins {
		t.Fatal("a changed proxy must not reuse the session of the earlier connection")
	}
	before = relayed.Load()
	if _, err := instance.service.Remote().ListDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if relayed.Load() != before {
		t.Fatal("a removed proxy still relayed requests")
	}
	if link, err := instance.service.LinkGet(ctx); err != nil || link.Proxy != (LinkProxy{}) {
		t.Fatalf("link proxy = %+v %v, want none", link.Proxy, err)
	}
}

func TestLinkSetKeepsProxyPasswordAndRejectsInvalidProxy(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	proxy := netproxy.Manual{Type: netproxy.TypeSOCKS5, Host: "proxy.internal", Port: 1080, Username: "u", Password: "p"}

	if _, err := instance.service.LinkSet(ctx, LinkPatch{Proxy: proxyPatch(proxy, testPtr("p"))}); err != nil {
		t.Fatal(err)
	}
	moved := proxy
	moved.Host = "proxy2.internal"
	if _, err := instance.service.LinkSet(ctx, LinkPatch{Proxy: proxyPatch(moved, nil)}); err != nil {
		t.Fatal(err)
	}
	if saved, err := instance.service.LinkProxy(ctx); err != nil || saved != moved {
		t.Fatalf("LinkProxy = %+v %v, want %+v with the kept password", saved, err, moved)
	}

	anonymous := moved
	anonymous.Username, anonymous.Password = "", ""
	if _, err := instance.service.LinkSet(ctx, LinkPatch{Proxy: proxyPatch(anonymous, nil)}); err != nil {
		t.Fatalf("clearing the proxy username must drop the kept password: %v", err)
	}
	if saved, err := instance.service.LinkProxy(ctx); err != nil || saved != anonymous {
		t.Fatalf("LinkProxy = %+v %v, want %+v", saved, err, anonymous)
	}
	if _, err := instance.service.LinkSet(ctx, LinkPatch{Proxy: proxyPatch(moved, testPtr("p"))}); err != nil {
		t.Fatal(err)
	}

	socks4 := netproxy.Manual{Type: netproxy.TypeSOCKS4, Host: "proxy.internal", Port: 1080, Username: "u"}
	_, err := instance.service.LinkSet(ctx, LinkPatch{Proxy: proxyPatch(socks4, testPtr("p"))})
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("SOCKS4 with an entered password: err = %v, want bad_param", err)
	}
	if saved, err := instance.service.LinkProxy(ctx); err != nil || saved != moved {
		t.Fatalf("a rejected proxy changed the saved one: %+v %v", saved, err)
	}
	if _, err := instance.service.LinkSet(ctx, LinkPatch{Proxy: proxyPatch(socks4, nil)}); err != nil {
		t.Fatalf("switching to SOCKS4 must drop the kept password: %v", err)
	}
	if saved, err := instance.service.LinkProxy(ctx); err != nil || saved != socks4 {
		t.Fatalf("LinkProxy = %+v %v, want %+v", saved, err, socks4)
	}
}

func TestLinkSetCompletesURLWithHTTPS(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	for raw, want := range map[string]string{
		" nexterm.example.com ":       "https://nexterm.example.com",
		"nexterm.example.com:8443":    "https://nexterm.example.com:8443",
		"http://192.168.1.10:8080":    "http://192.168.1.10:8080",
		"https://nexterm.example.com": "https://nexterm.example.com",
	} {
		result, err := instance.service.LinkSet(ctx, LinkPatch{URL: testPtr(raw)})
		if err != nil {
			t.Fatal(err)
		}
		if result.URL != want {
			t.Errorf("LinkSet(%q) saved %q, want %q", raw, result.URL, want)
		}
	}
}
