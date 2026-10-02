package winrm

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const fixtureCreateShellResponse = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:rsp="http://schemas.microsoft.com/wbem/wsman/1/windows/shell"><s:Header><a:Action>http://schemas.xmlsoap.org/ws/2004/09/transfer/CreateResponse</a:Action></s:Header><s:Body><rsp:Shell><rsp:ShellId>fixture-shell</rsp:ShellId></rsp:Shell></s:Body></s:Envelope>`

const fixtureExecuteCommandResponse = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:rsp="http://schemas.microsoft.com/wbem/wsman/1/windows/shell"><s:Header><a:Action>http://schemas.microsoft.com/wbem/wsman/1/windows/shell/CommandResponse</a:Action></s:Header><s:Body><rsp:CommandResponse><rsp:CommandId>fixture-command</rsp:CommandId></rsp:CommandResponse></s:Body></s:Envelope>`

func fixtureEndpoint(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func writeFixtureSOAP(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
	_, _ = io.WriteString(w, body)
}

func createShellFixture() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureSOAP(w, fixtureCreateShellResponse)
	})
}

func TestBasicDomainCredentialsAndExecOverSOAPFixture(t *testing.T) {
	stdout := "fixture output\n" + cwdSentinelHead + `C:\fixture` + cwdSentinelTail + "\r\n"
	receiveResponse := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:rsp="http://schemas.microsoft.com/wbem/wsman/1/windows/shell"><s:Body><rsp:ReceiveResponse><rsp:Stream Name="stdout" CommandId="fixture-command">` + base64.StdEncoding.EncodeToString([]byte(stdout)) + `</rsp:Stream><rsp:Stream Name="stderr" CommandId="fixture-command">` + base64.StdEncoding.EncodeToString([]byte("fixture stderr")) + `</rsp:Stream><rsp:CommandState CommandId="fixture-command" State="http://schemas.microsoft.com/wbem/wsman/1/windows/shell/CommandState/Done"><rsp:ExitCode>17</rsp:ExitCode></rsp:CommandState></rsp:ReceiveResponse></s:Body></s:Envelope>`
	var principal atomic.Value
	var password atomic.Value
	var encodedPowerShell atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		user, pass, _ := request.BasicAuth()
		principal.Store(user)
		password.Store(pass)
		body, _ := io.ReadAll(request.Body)
		switch {
		case strings.Contains(string(body), "transfer/Create"):
			writeFixtureSOAP(w, fixtureCreateShellResponse)
		case strings.Contains(string(body), "shell/Command"):
			encodedPowerShell.Store(strings.Contains(string(body), "powershell.exe -EncodedCommand"))
			writeFixtureSOAP(w, fixtureExecuteCommandResponse)
		case strings.Contains(string(body), "shell/Receive"):
			writeFixtureSOAP(w, receiveResponse)
		default:
			writeFixtureSOAP(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"/>`)
		}
	}))
	defer server.Close()
	host, port := fixtureEndpoint(t, server.URL)
	transport, err := New(Config{
		Host: host, Port: port, User: "alice", Domain: "EXAMPLE", Password: "secret", Auth: AuthBasic,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	result, err := transport.Exec(context.Background(), "Write-Output fixture", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == nil || *result.ExitCode != 17 || result.Stdout != "fixture output" || result.Stderr != "fixture stderr" {
		t.Fatalf("SOAP exec = %+v", result)
	}
	if principal.Load() != `EXAMPLE\alice` || password.Load() != "secret" {
		t.Fatalf("Basic credentials = %q / %q", principal.Load(), password.Load())
	}
	if !encodedPowerShell.Load() || transport.CWD() != `C:\fixture` {
		t.Fatalf("PowerShell or cwd missing: encoded=%v cwd=%q", encodedPowerShell.Load(), transport.CWD())
	}
}

func TestExplicitHTTPProxyOverSOAPFixture(t *testing.T) {
	var requests atomic.Int32
	var targetHost atomic.Value
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		targetHost.Store(request.Host)
		writeFixtureSOAP(w, fixtureCreateShellResponse)
	}))
	defer proxy.Close()
	config, err := (Config{
		Host: "winrm-fixture.invalid", User: "alice", Password: "secret", Auth: AuthBasic, ProxyURL: proxy.URL,
	}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	client, err := newLibraryClient(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateShell(); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || targetHost.Load() != "winrm-fixture.invalid:5985" {
		t.Fatalf("proxy requests=%d target=%q", requests.Load(), targetHost.Load())
	}
}

func TestTLSModesOverSOAPFixture(t *testing.T) {
	server := httptest.NewTLSServer(createShellFixture())
	defer server.Close()
	host, port := fixtureEndpoint(t, server.URL)
	baseConfig := Config{Host: host, Port: port, User: "alice", Password: "secret", Auth: AuthBasic, UseTLS: true}

	t.Run("accept invalid certificate", func(t *testing.T) {
		config := baseConfig
		config.AcceptInvalidCerts = true
		normalized, err := config.normalized()
		if err != nil {
			t.Fatal(err)
		}
		client, err := newLibraryClient(normalized)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.CreateShell(); err != nil {
			t.Fatalf("self-signed TLS fixture was not accepted: %v", err)
		}
	})

	t.Run("custom CA", func(t *testing.T) {
		config := baseConfig
		config.CACert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
		normalized, err := config.normalized()
		if err != nil {
			t.Fatal(err)
		}
		client, err := newLibraryClient(normalized)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.CreateShell(); err != nil {
			t.Fatalf("CA-trusted TLS fixture failed: %v", err)
		}
	})

	t.Run("reject untrusted", func(t *testing.T) {
		normalized, err := baseConfig.normalized()
		if err != nil {
			t.Fatal(err)
		}
		client, err := newLibraryClient(normalized)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.CreateShell(); err == nil {
			t.Fatal("untrusted self-signed certificate succeeded")
		}
	})

	t.Run("server name", func(t *testing.T) {
		config := baseConfig
		config.AcceptInvalidCerts = false
		config.TLSServerName = "wrong-fixture.invalid"
		config.CACert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
		normalized, err := config.normalized()
		if err != nil {
			t.Fatal(err)
		}
		client, err := newLibraryClient(normalized)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.CreateShell(); err == nil || !strings.Contains(err.Error(), "wrong-fixture.invalid") {
			t.Fatalf("TLS server name was not enforced: %v", err)
		}
	})
}
