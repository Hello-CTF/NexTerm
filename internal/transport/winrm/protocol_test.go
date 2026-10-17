package winrm

import (
	"bytes"
	"context"
	"crypto/rc4"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"golang.org/x/crypto/md4"
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
	runner, err := newLibraryRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.createShell(context.Background()); err != nil {
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
		runner, err := newLibraryRunner(normalized)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.createShell(context.Background()); err != nil {
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
		runner, err := newLibraryRunner(normalized)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.createShell(context.Background()); err != nil {
			t.Fatalf("CA-trusted TLS fixture failed: %v", err)
		}
	})

	t.Run("reject untrusted", func(t *testing.T) {
		normalized, err := baseConfig.normalized()
		if err != nil {
			t.Fatal(err)
		}
		runner, err := newLibraryRunner(normalized)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.createShell(context.Background()); err == nil {
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
		runner, err := newLibraryRunner(normalized)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.createShell(context.Background()); err == nil || !strings.Contains(err.Error(), "wrong-fixture.invalid") {
			t.Fatalf("TLS server name was not enforced: %v", err)
		}
	})
}

func TestNTLMMultipartMessageRoundTrip(t *testing.T) {
	sessionKey := make([]byte, 16)
	for i := range sessionKey {
		sessionKey[i] = byte(i + 1)
	}
	flags := uint32(ntlmNegotiateExtendedSessionSecurity | ntlmNegotiate128 | ntlmNegotiateSign | ntlmNegotiateSeal | ntlmNegotiateKeyExch)
	client, err := newAzureNTLMSecuritySession(sessionKey, flags, true)
	if err != nil {
		t.Fatal(err)
	}
	server, err := newAzureNTLMSecuritySession(sessionKey, flags, false)
	if err != nil {
		t.Fatal(err)
	}
	clientEncryption, err := newWinRMMessageEncryption("ntlm", azureNTLMMessageProtector{session: client})
	if err != nil {
		t.Fatal(err)
	}
	serverEncryption, err := newWinRMMessageEncryption("ntlm", azureNTLMMessageProtector{session: server})
	if err != nil {
		t.Fatal(err)
	}
	plaintext := make([]byte, 2*maxEncryptedPartSize+257)
	for i := range plaintext {
		plaintext[i] = byte(i * 31)
	}
	encrypted, err := clientEncryption.encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(clientEncryption.contentType(), "multipart/x-multi-encrypted;") {
		t.Fatalf("multipart content type = %q", clientEncryption.contentType())
	}
	if bytes.Contains(encrypted, plaintext[:128]) {
		t.Fatal("encrypted NTLM message contains a plaintext chunk")
	}
	decrypted, err := serverEncryption.decrypt(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted request differs: got %d bytes, want %d", len(decrypted), len(plaintext))
	}
	response, err := serverEncryption.encrypt([]byte("encrypted response"))
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err = clientEncryption.decrypt(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(decrypted) != "encrypted response" {
		t.Fatalf("decrypted response = %q", decrypted)
	}
}

func TestNTLMHandshakeHonorsRequestContext(t *testing.T) {
	requestCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
			close(requestCanceled)
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	host, port := fixtureEndpoint(t, server.URL)
	config, err := (Config{Host: host, Port: port, User: "alice", Password: "secret", Auth: AuthNTLM, RequestTimeout: time.Second}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := newLibraryRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = runner.createShell(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("NTLM handshake error = %v, want context deadline", err)
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("NTLM handshake HTTP request context was not canceled")
	}
}

const ntlmTestChallengeFlags uint32 = 0x201 | ntlmNegotiateExtendedSessionSecurity | ntlmNegotiateSign | ntlmNegotiateSeal | ntlmNegotiateKeyExch

type ntlmTestServer struct {
	flags             uint32
	malformedResponse bool
	mu                sync.Mutex
	encryption        *winRMMessageEncryption
	challenges        int
	handshakes        int
	sealed            int
}

func (s *ntlmTestServer) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/") {
		s.sealed++
		if s.encryption == nil {
			http.Error(w, "missing NTLM session", http.StatusBadRequest)
			return
		}
		plaintext, err := s.encryption.decrypt(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if s.malformedResponse {
			s.malformedResponse = false
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("malformed encrypted response"))
			return
		}
		encrypted, err := s.encryption.encrypt(append([]byte("echo:"), plaintext...))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", s.encryption.contentType())
		_, _ = w.Write(encrypted)
		return
	}
	_, encoded, _ := strings.Cut(request.Header.Get("Authorization"), " ")
	token, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(token) < 12 || string(token[:8]) != "NTLMSSP\x00" {
		http.Error(w, "invalid NTLM token", http.StatusBadRequest)
		return
	}
	switch binary.LittleEndian.Uint32(token[8:12]) {
	case 1:
		s.challenges++
		challenge := make([]byte, 48)
		copy(challenge[:8], "NTLMSSP\x00")
		binary.LittleEndian.PutUint32(challenge[8:12], 2)
		binary.LittleEndian.PutUint32(challenge[20:24], s.flags)
		copy(challenge[24:32], []byte{1, 2, 3, 4, 5, 6, 7, 8})
		w.Header().Set("WWW-Authenticate", "Negotiate "+base64.StdEncoding.EncodeToString(challenge))
		w.WriteHeader(http.StatusUnauthorized)
	case 3:
		sessionKey, err := ntlmTestSessionKey(token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		session, err := newAzureNTLMSecuritySession(sessionKey, s.flags, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.encryption, err = newWinRMMessageEncryption("ntlm", azureNTLMMessageProtector{session: session})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.handshakes++
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "unexpected NTLM message", http.StatusBadRequest)
	}
}

func (s *ntlmTestServer) counts() (challenges, handshakes, sealed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.challenges, s.handshakes, s.sealed
}

func ntlmTestSessionKey(token []byte) ([]byte, error) {
	ntResponse := ntlmTestField(token, 20)
	encryptedSessionKey := ntlmTestField(token, 52)
	if len(ntResponse) < 16 || len(encryptedSessionKey) != 16 {
		return nil, errors.New("invalid NTLM authenticate fields")
	}
	hash := md4.New()
	_, _ = hash.Write(ntlmTestUTF16("secret"))
	ntlmV2Hash := ntlmHmacMd5(hash.Sum(nil), ntlmTestUTF16("ALICE"))
	sessionBaseKey := ntlmHmacMd5(ntlmV2Hash, ntResponse[:16])
	cipher, err := rc4.NewCipher(sessionBaseKey)
	if err != nil {
		return nil, err
	}
	sessionKey := make([]byte, 16)
	cipher.XORKeyStream(sessionKey, encryptedSessionKey)
	return sessionKey, nil
}

func ntlmTestField(token []byte, fieldOffset int) []byte {
	if len(token) < fieldOffset+8 {
		return nil
	}
	length := int(binary.LittleEndian.Uint16(token[fieldOffset : fieldOffset+2]))
	offset := int(binary.LittleEndian.Uint32(token[fieldOffset+4 : fieldOffset+8]))
	if offset < 0 || length < 0 || offset > len(token) || length > len(token)-offset {
		return nil
	}
	return token[offset : offset+length]
}

func ntlmTestUTF16(value string) []byte {
	encoded := utf16.Encode([]rune(value))
	result := make([]byte, 2*len(encoded))
	for i, value := range encoded {
		binary.LittleEndian.PutUint16(result[2*i:2*i+2], value)
	}
	return result
}

func ntlmTestHTTPTransport() *http.Transport {
	return &http.Transport{MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1}
}

func ntlmTestRequest(t *testing.T, ctx context.Context, endpoint, body string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")
	request.SetBasicAuth("alice", "secret")
	return request
}

type ntlmCancelFirstSealed struct {
	inner   http.RoundTripper
	started chan struct{}
	once    sync.Once
}

func (t *ntlmCancelFirstSealed) RoundTrip(request *http.Request) (*http.Response, error) {
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/") {
		canceled := false
		t.once.Do(func() {
			canceled = true
			close(t.started)
			<-request.Context().Done()
		})
		if canceled {
			return nil, request.Context().Err()
		}
	}
	return t.inner.RoundTrip(request)
}

func TestNTLMCanceledSendRenegotiatesBeforeReuse(t *testing.T) {
	state := &ntlmTestServer{flags: ntlmTestChallengeFlags | ntlmNegotiate128}
	server := httptest.NewServer(state)
	defer server.Close()
	inner := ntlmTestHTTPTransport()
	defer inner.CloseIdleConnections()
	canceler := &ntlmCancelFirstSealed{inner: inner, started: make(chan struct{})}
	transport := newNTLMSealingTransport(canceler)
	ctx, cancel := context.WithCancel(context.Background())
	request := ntlmTestRequest(t, ctx, server.URL, "first")
	done := make(chan error, 1)
	go func() {
		response, err := transport.RoundTrip(request)
		if response != nil {
			_ = response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-canceler.started:
	case <-time.After(time.Second):
		t.Fatal("first sealed request did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled send error = %v", err)
	}
	reuseCtx, reuseCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer reuseCancel()
	response, err := transport.RoundTrip(ntlmTestRequest(t, reuseCtx, server.URL, "second"))
	if err != nil {
		t.Fatalf("reuse after cancellation: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(body) != "echo:second" {
		t.Fatalf("reuse response = %q, %v", body, err)
	}
	challenges, handshakes, sealed := state.counts()
	if challenges != 2 || handshakes != 2 || sealed != 1 {
		t.Fatalf("reuse did not renegotiate: challenges=%d handshakes=%d sealed=%d", challenges, handshakes, sealed)
	}
}

func TestNTLMDecryptFailureRenegotiatesBeforeReuse(t *testing.T) {
	state := &ntlmTestServer{flags: ntlmTestChallengeFlags | ntlmNegotiate128, malformedResponse: true}
	server := httptest.NewServer(state)
	defer server.Close()
	inner := ntlmTestHTTPTransport()
	defer inner.CloseIdleConnections()
	transport := newNTLMSealingTransport(inner)
	response, err := transport.RoundTrip(ntlmTestRequest(t, context.Background(), server.URL, "first"))
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("malformed encrypted response succeeded")
	}
	response, err = transport.RoundTrip(ntlmTestRequest(t, context.Background(), server.URL, "second"))
	if err != nil {
		t.Fatalf("reuse after decrypt failure: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(body) != "echo:second" {
		t.Fatalf("reuse response = %q, %v", body, err)
	}
	challenges, handshakes, sealed := state.counts()
	if challenges != 2 || handshakes != 2 || sealed != 2 {
		t.Fatalf("decrypt failure did not renegotiate: challenges=%d handshakes=%d sealed=%d", challenges, handshakes, sealed)
	}
}

func TestNTLMHandshakeRejectsWeakKeys(t *testing.T) {
	for _, test := range []struct {
		name  string
		flags uint32
	}{
		{name: "56-bit", flags: ntlmTestChallengeFlags | ntlmNegotiate56},
		{name: "40-bit", flags: ntlmTestChallengeFlags},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &ntlmTestServer{flags: test.flags}
			server := httptest.NewServer(state)
			defer server.Close()
			inner := ntlmTestHTTPTransport()
			defer inner.CloseIdleConnections()
			transport := newNTLMSealingTransport(inner)
			response, err := transport.RoundTrip(ntlmTestRequest(t, context.Background(), server.URL, "weak"))
			if response != nil {
				_ = response.Body.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "NTLMSSP_NEGOTIATE_128") {
				t.Fatalf("weak NTLM handshake error = %v", err)
			}
			challenges, handshakes, sealed := state.counts()
			if challenges != 1 || handshakes != 0 || sealed != 0 {
				t.Fatalf("weak handshake was not rejected before authenticate: challenges=%d handshakes=%d sealed=%d", challenges, handshakes, sealed)
			}
		})
	}
}
