package winrm

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	gowinrm "github.com/masterzen/winrm"
)

func TestConfigDefaultsAndDomainPrincipal(t *testing.T) {
	config, err := (Config{Host: "server.example", User: "alice", Domain: "EXAMPLE"}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	if config.Port != 5985 || config.UseTLS || config.Auth != AuthNTLM {
		t.Fatalf("unexpected defaults: %+v", config)
	}
	if config.RequestTimeout != DefaultRequestTimeout || config.InitialCWD != `C:\` {
		t.Fatalf("unexpected timeout or cwd: %+v", config)
	}
	if got := config.principal(); got != `EXAMPLE\alice` {
		t.Fatalf("principal = %q", got)
	}

	tlsConfig, err := (Config{Host: "server.example", Port: 5986, User: `OTHER\bob`, Auth: AuthBasic}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	if !tlsConfig.UseTLS || tlsConfig.principal() != `OTHER\bob` {
		t.Fatalf("TLS or qualified user not preserved: %+v", tlsConfig)
	}
}

func TestConfigRejectsAmbiguousOrInvalidValues(t *testing.T) {
	tests := []Config{
		{User: "alice"},
		{Host: "https://server.example", User: "alice"},
		{Host: "server.example", Port: 70000, User: "alice"},
		{Host: "server.example"},
		{Host: "server.example", User: `DOMAIN\alice`, Domain: "OTHER"},
		{Host: "server.example", User: "alice@example.com", Domain: "EXAMPLE"},
		{Host: "server.example", User: "alice", Auth: "kerberos"},
		{Host: "server.example", User: "alice", RequestTimeout: -1},
		{Host: "server.example", User: "alice", CACert: []byte("certificate")},
		{Host: "server.example", User: "alice", TLSServerName: "server.example"},
		{Host: "server.example", User: "alice", ProxyURL: "://bad"},
		{Host: "server.example", User: "alice", ProxyURL: "ftp://proxy.example"},
	}
	for _, config := range tests {
		if _, err := config.normalized(); err == nil {
			t.Errorf("expected config to fail: %+v", config)
		}
	}
}

func TestAuthenticationDecoratorIsConsumed(t *testing.T) {
	direct, err := proxyFunction("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := transportDecorator(AuthNTLM, direct)().(*gowinrm.ClientNTLM); !ok {
		t.Fatalf("NTLM config produced %T", transportDecorator(AuthNTLM, direct)())
	}
	basic := transportDecorator(AuthBasic, direct)()
	if _, ok := basic.(*gowinrm.ClientNTLM); ok || !strings.Contains(fmt.Sprintf("%T", basic), "clientRequest") {
		t.Fatalf("Basic config produced %T", basic)
	}
}

func TestProxyIsExplicitAndEnvironmentIsIgnored(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://environment-proxy.invalid:8080")
	t.Setenv("HTTPS_PROXY", "http://environment-proxy.invalid:8080")
	request := httptest.NewRequest("POST", "http://server.example/wsman", nil)
	direct, err := proxyFunction("")
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := direct(request)
	if err != nil || proxyURL != nil {
		t.Fatalf("direct proxy = %v, %v", proxyURL, err)
	}
	configured, err := proxyFunction("http://user:pass@proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err = configured(request)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL == nil || proxyURL.Host != "proxy.example:8080" {
		t.Fatalf("configured proxy = %v", proxyURL)
	}
}

func TestTLSValidationConfigurationIsConsumed(t *testing.T) {
	config, err := (Config{
		Host:       "server.example",
		User:       "alice",
		UseTLS:     true,
		CACert:     []byte("not a PEM certificate"),
		InitialCWD: `D:\`,
	}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newLibraryClient(config); err == nil || !strings.Contains(err.Error(), "build WinRM client") {
		t.Fatalf("invalid CA should fail client construction, got %v", err)
	}
}
