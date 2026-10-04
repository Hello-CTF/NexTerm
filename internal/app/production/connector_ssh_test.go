package production

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type sshConnectorServer struct {
	listener     net.Listener
	config       *gossh.ServerConfig
	agentKey     gossh.PublicKey
	agentResult  chan error
	execCommands chan string
}

func newSSHConnectorServer(t *testing.T, customize func(*gossh.ServerConfig)) *sshConnectorServer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &gossh.ServerConfig{
		PasswordCallback: func(metadata gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
			if metadata.User() == "test" && string(password) == "secret" {
				return nil, nil
			}
			return nil, fmt.Errorf("invalid test credentials")
		},
	}
	if customize != nil {
		customize(config)
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &sshConnectorServer{listener: listener, config: config}
	go server.accept()
	t.Cleanup(func() { _ = server.listener.Close() })
	return server
}

func (s *sshConnectorServer) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *sshConnectorServer) handle(conn net.Conn) {
	serverConn, channels, requests, err := gossh.NewServerConn(conn, s.config)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer serverConn.Close()
	go gossh.DiscardRequests(requests)
	for newChannel := range channels {
		switch newChannel.ChannelType() {
		case "direct-tcpip":
			s.handleDirectTCPIP(newChannel)
		case "session":
			channel, channelRequests, err := newChannel.Accept()
			if err == nil {
				go s.handleSession(serverConn, channel, channelRequests)
			}
		default:
			_ = newChannel.Reject(gossh.UnknownChannelType, "connector test server")
		}
	}
}

func (s *sshConnectorServer) handleDirectTCPIP(newChannel gossh.NewChannel) {
	var payload struct {
		Raddr string
		Rport uint32
		Laddr string
		Lport uint32
	}
	if err := gossh.Unmarshal(newChannel.ExtraData(), &payload); err != nil {
		_ = newChannel.Reject(gossh.Prohibited, "invalid direct-tcpip payload")
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(payload.Raddr, strconv.Itoa(int(payload.Rport))))
	if err != nil {
		_ = newChannel.Reject(gossh.ConnectionFailed, err.Error())
		return
	}
	channel, channelRequests, err := newChannel.Accept()
	if err != nil {
		_ = target.Close()
		return
	}
	go gossh.DiscardRequests(channelRequests)
	go func() {
		_, _ = io.Copy(channel, target)
		_ = channel.CloseWrite()
	}()
	go func() {
		_, _ = io.Copy(target, channel)
		if tcp, ok := target.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		_ = channel.Close()
		_ = target.Close()
	}()
}

func (s *sshConnectorServer) handleSession(conn *gossh.ServerConn, channel gossh.Channel, requests <-chan *gossh.Request) {
	defer channel.Close()
	for request := range requests {
		switch request.Type {
		case "exec":
			var payload struct{ Command string }
			if err := gossh.Unmarshal(request.Payload, &payload); err != nil {
				request.Reply(false, nil)
				continue
			}
			request.Reply(true, nil)
			if s.execCommands != nil {
				select {
				case s.execCommands <- payload.Command:
				default:
				}
			}
			_, _ = channel.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{0}))
			return
		case "auth-agent-req@openssh.com":
			request.Reply(true, nil)
			go s.checkForwardedAgent(conn)
		default:
			if request.WantReply {
				request.Reply(false, nil)
			}
		}
	}
}

func (s *sshConnectorServer) checkForwardedAgent(conn *gossh.ServerConn) {
	channel, requests, err := conn.OpenChannel("auth-agent@openssh.com", nil)
	if err != nil {
		s.reportAgentResult(fmt.Errorf("open auth-agent channel: %w", err))
		return
	}
	defer channel.Close()
	go gossh.DiscardRequests(requests)
	signers, err := agent.NewClient(channel).Signers()
	if err != nil {
		s.reportAgentResult(fmt.Errorf("list forwarded agent signers: %w", err))
		return
	}
	if len(signers) != 1 || !bytes.Equal(signers[0].PublicKey().Marshal(), s.agentKey.Marshal()) {
		s.reportAgentResult(fmt.Errorf("forwarded agent signers = %d, want the single forwarded test key", len(signers)))
		return
	}
	s.reportAgentResult(nil)
}

func (s *sshConnectorServer) reportAgentResult(err error) {
	select {
	case s.agentResult <- err:
	default:
	}
}

func (s *sshConnectorServer) port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

func TestProductionSSHJumpAssetConnectVerifiesEveryHop(t *testing.T) {
	jump := newSSHConnectorServer(t, nil)
	target := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "hop-password", "secret")

	jumpAssetID := createConnectorTestAsset(t, production, "jump", "127.0.0.1", jump.port(), "password", credID, "")
	targetAssetID := createConnectorTestAsset(t, production, "target", "127.0.0.1", target.port(), "password", credID,
		`{"jumpAssetId":"`+jumpAssetID+`"}`)

	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+targetAssetID+`"}}`)
	jumpPending := requireHostKeyPending(t, response, false)
	if jumpPending.Port != jump.port() {
		t.Fatalf("first pending endpoint = %+v, want the jump hop", jumpPending)
	}
	acceptHostKeyTest(t, production, jumpPending)

	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+targetAssetID+`"}}`)
	targetPending := requireHostKeyPending(t, response, false)
	if targetPending.Port != target.port() {
		t.Fatalf("second pending endpoint = %+v, want the target", targetPending)
	}
	acceptHostKeyTest(t, production, targetPending)

	connected := connectHostKeyTestAsset(t, production, targetAssetID)
	if listed := knownHostTestFingerprints(t, production); len(listed) != 2 {
		t.Fatalf("both hops must be recorded, got %+v", listed)
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))

	reconnected := connectHostKeyTestAsset(t, production, targetAssetID)
	if reconnected.ID == "" {
		t.Fatal("reconnect through the jump host failed")
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+reconnected.ID+`"}`))
}

func TestProductionSSHJumpAssetFailureNamesTheFailingHop(t *testing.T) {
	jump := newSSHConnectorServer(t, nil)
	target := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	wrongCredID := createConnectorTestCredential(t, production, "wrong-hop-password", "wrong")
	credID := createConnectorTestCredential(t, production, "right-password", "secret")

	jumpAssetID := createConnectorTestAsset(t, production, "jump", "127.0.0.1", jump.port(), "password", wrongCredID,
		`{"autoAcceptUnknownHost":true}`)
	targetAssetID := createConnectorTestAsset(t, production, "target", "127.0.0.1", target.port(), "password", credID,
		`{"jumpAssetId":"`+jumpAssetID+`","autoAcceptUnknownHost":true}`)

	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+targetAssetID+`"}}`)
	if response.OK || response.Error == nil {
		t.Fatalf("connect with a bad jump credential = %+v", response)
	}
	if !strings.Contains(response.Error.Message, strconv.Itoa(jump.port())) {
		t.Fatalf("error does not name the failing jump hop: %+v", response.Error)
	}
	if strings.Contains(response.Error.Message, "wrong") {
		t.Fatalf("error leaks the jump credential: %+v", response.Error)
	}
}

func TestProductionSSHJumpAssetCycleAndValidation(t *testing.T) {
	target := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "cycle-password", "secret")

	selfID := createConnectorTestAsset(t, production, "self", "127.0.0.1", target.port(), "password", credID, "")
	setConnectorTestAssetOptions(t, production, selfID, `{"jumpAssetId":"`+selfID+`"}`)
	requireConnectorConnectError(t, production, selfID, "cycle")

	firstID := createConnectorTestAsset(t, production, "first", "127.0.0.1", target.port(), "password", credID, "")
	secondID := createConnectorTestAsset(t, production, "second", "127.0.0.1", target.port(), "password", credID, "")
	setConnectorTestAssetOptions(t, production, firstID, `{"jumpAssetId":"`+secondID+`"}`)
	setConnectorTestAssetOptions(t, production, secondID, `{"jumpAssetId":"`+firstID+`"}`)
	requireConnectorConnectError(t, production, firstID, "cycle")

	missingID := createConnectorTestAsset(t, production, "missing-hop", "127.0.0.1", target.port(), "password", credID,
		`{"jumpAssetId":"01J0NEXTERMMISSINGASSET000001"}`)
	requireConnectorConnectError(t, production, missingID, "does not exist")

	winrmResponse := dispatchHostKeyTest(t, production, "asset_create",
		`{"args":{"kind":"winrm","name":"winrm-hop","host":"127.0.0.1","port":5985,"username":"test"}}`)
	var winrmAsset assetDTO
	requireStoreTestResponse(t, winrmResponse, &winrmAsset)
	wrongKindID := createConnectorTestAsset(t, production, "wrong-kind-hop", "127.0.0.1", target.port(), "password", credID,
		`{"jumpAssetId":"`+winrmAsset.ID+`"}`)
	requireConnectorConnectError(t, production, wrongKindID, "must be an SSH asset")

	deletedID := createConnectorTestAsset(t, production, "deleted-hop", "127.0.0.1", target.port(), "password", credID, "")
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "asset_delete", `{"id":"`+deletedID+`"}`))
	goneID := createConnectorTestAsset(t, production, "gone-hop", "127.0.0.1", target.port(), "password", credID,
		`{"jumpAssetId":"`+deletedID+`"}`)
	requireConnectorConnectError(t, production, goneID, "deleted")
}

func TestProductionSSHKeyboardInteractiveAsset(t *testing.T) {
	server := newSSHConnectorServer(t, func(config *gossh.ServerConfig) {
		config.KeyboardInteractiveCallback = func(metadata gossh.ConnMetadata, challenge gossh.KeyboardInteractiveChallenge) (*gossh.Permissions, error) {
			answers, err := challenge("", "", []string{"Password: ", "One-time code: "}, []bool{false, false})
			if err != nil {
				return nil, err
			}
			for _, answer := range answers {
				if answer != "secret" {
					return nil, fmt.Errorf("keyboard-interactive rejected")
				}
			}
			return nil, nil
		}
	})
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "interactive-password", "secret")
	assetID := createConnectorTestAsset(t, production, "interactive", "127.0.0.1", server.port(), "keyboard-interactive", credID,
		`{"autoAcceptUnknownHost":true}`)
	connected := connectHostKeyTestAsset(t, production, assetID)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
}

func TestProductionSSHKeyboardInteractiveFailureDoesNotLeakSecret(t *testing.T) {
	server := newSSHConnectorServer(t, func(config *gossh.ServerConfig) {
		config.KeyboardInteractiveCallback = func(metadata gossh.ConnMetadata, challenge gossh.KeyboardInteractiveChallenge) (*gossh.Permissions, error) {
			if _, err := challenge("", "", []string{"Password: "}, []bool{false}); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("keyboard-interactive rejected")
		}
	})
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "leaky-password", "top-secret-value")
	assetID := createConnectorTestAsset(t, production, "leaky", "127.0.0.1", server.port(), "keyboard-interactive", credID,
		`{"autoAcceptUnknownHost":true}`)
	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	if response.OK || response.Error == nil {
		t.Fatalf("connect = %+v", response)
	}
	if strings.Contains(response.Error.Message, "top-secret-value") {
		t.Fatalf("error leaks the credential: %+v", response.Error)
	}
}

func TestProductionSSHCertificateAsset(t *testing.T) {
	_, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caSigner, err := gossh.NewSignerFromKey(caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	_, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := gossh.NewSignerFromKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	server := newSSHConnectorServer(t, func(config *gossh.ServerConfig) {
		config.PublicKeyCallback = func(metadata gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			cert, ok := key.(*gossh.Certificate)
			if !ok || cert.CertType != gossh.UserCert {
				return nil, fmt.Errorf("certificates only")
			}
			return nil, nil
		}
	})

	keyBlock, err := gossh.MarshalPrivateKey(clientPrivate, "test")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	keyPath := directory + "/id_ed25519"
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(keyBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	cert := &gossh.Certificate{
		Key:             clientSigner.PublicKey(),
		CertType:        gossh.UserCert,
		KeyId:           "production-test",
		ValidPrincipals: []string{"test"},
		ValidBefore:     gossh.CertTimeInfinity,
	}
	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatal(err)
	}
	certPath := directory + "/id_ed25519-cert.pub"
	if err := os.WriteFile(certPath, gossh.MarshalAuthorizedKey(cert), 0o600); err != nil {
		t.Fatal(err)
	}

	production := newHostKeyTestProduction(t)
	assetID := createConnectorTestAsset(t, production, "cert", "127.0.0.1", server.port(), "key", "",
		`{"certPath":`+strconv.Quote(certPath)+`,"autoAcceptUnknownHost":true}`)
	setConnectorTestAssetKeyPath(t, production, assetID, keyPath)
	connected := connectHostKeyTestAsset(t, production, assetID)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
}

func TestProductionSSHProxyCommandAsset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("proxy command test uses sh -c; Windows runs compile-only coverage")
	}
	server := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "proxy-password", "secret")
	command := "NEXTERM_SSH_PROXY_HELPER=1 " + strconv.Quote(os.Args[0]) + " -test.run=^TestProductionSSHProxyCommandHelper$ %h %p"
	assetID := createConnectorTestAsset(t, production, "proxy-command", "127.0.0.1", server.port(), "password", credID,
		`{"proxyCommand":`+strconv.Quote(command)+`,"autoAcceptUnknownHost":true}`)
	connected := connectHostKeyTestAsset(t, production, assetID)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
}

func TestProductionSSHProxyCommandHelper(t *testing.T) {
	if os.Getenv("NEXTERM_SSH_PROXY_HELPER") != "1" {
		t.Skip("helper process for proxy command tests")
	}
	args := os.Args
	host, port := args[len(args)-2], args[len(args)-1]
	conn, err := net.Dial("tcp", net.JoinHostPort(host, port))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	_, _ = io.Copy(os.Stdout, conn)
	os.Exit(0)
}

func TestProductionSSHAgentForwardingOption(t *testing.T) {
	server := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "forward-password", "secret")
	assetID := createConnectorTestAsset(t, production, "forward-agent", "127.0.0.1", server.port(), "password", credID,
		`{"forwardAgent":true,"autoAcceptUnknownHost":true}`)
	t.Setenv("SSH_AUTH_SOCK", "")
	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	if response.OK || response.Error == nil {
		t.Fatalf("connect = %+v", response)
	}
	if !strings.Contains(response.Error.Message, "agent socket") {
		t.Fatalf("missing-socket error is unclear: %+v", response.Error)
	}
}

func TestProductionSSHAgentForwardingInitialCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SSH agent integration uses SSH_AUTH_SOCK over a Unix socket; Windows runs the compile-only coverage")
	}
	server := newSSHConnectorServer(t, nil)
	private, signer := generateConnectorTestKey(t)
	server.agentKey = signer.PublicKey()
	server.agentResult = make(chan error, 1)
	server.execCommands = make(chan string, 1)
	socket := serveConnectorTestAgent(t, private)

	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "forward-exec-password", "secret")
	assetID := createConnectorTestAsset(t, production, "forward-exec", "127.0.0.1", server.port(), "password", credID,
		`{"forwardAgent":true,"agentSocket":`+strconv.Quote(socket)+`,"initialCommand":"probe-agent","autoAcceptUnknownHost":true}`)
	connected := connectHostKeyTestAsset(t, production, assetID)

	select {
	case command := <-server.execCommands:
		if command != "probe-agent" {
			t.Fatalf("initial command = %q, want probe-agent", command)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initial command did not run over the forwarded-agent asset")
	}
	select {
	case err := <-server.agentResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe the forwarded agent on the InitialCommand exec session")
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
}

func generateConnectorTestKey(t *testing.T) (ed25519.PrivateKey, gossh.Signer) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return private, signer
}

func serveConnectorTestAgent(t *testing.T, private ed25519.PrivateKey) string {
	t.Helper()
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: private}); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp("", "nxagent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "a.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				agent.ServeAgent(keyring, conn)
				_ = conn.Close()
			}()
		}
	}()
	return socket
}

func initConnectorTestVault(t *testing.T, production *Production) {
	t.Helper()
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "vault_init_master", `{"password":"connector-test-master"}`))
}

func createConnectorTestCredential(t *testing.T, production *Production, name, secret string) string {
	t.Helper()
	response := dispatchHostKeyTest(t, production, "vault_set_credential",
		`{"args":{"name":"`+name+`","kind":"password","secret":"`+secret+`"}}`)
	var saved map[string]string
	requireStoreTestResponse(t, response, &saved)
	return saved["id"]
}

func createConnectorTestAsset(t *testing.T, production *Production, name, host string, port int, authKind, credID, options string) string {
	t.Helper()
	args := `{"kind":"ssh","name":"` + name + `","host":"` + host + `","port":` + strconv.Itoa(port) +
		`,"username":"test","authKind":"` + authKind + `"`
	if credID != "" {
		args += `,"credId":"` + credID + `"`
	}
	if options != "" {
		args += `,"options":` + options
	}
	args += `}`
	response := dispatchHostKeyTest(t, production, "asset_create", `{"args":`+args+`}`)
	var created assetDTO
	requireStoreTestResponse(t, response, &created)
	return created.ID
}

func setConnectorTestAssetOptions(t *testing.T, production *Production, id, options string) {
	t.Helper()
	response := dispatchHostKeyTest(t, production, "asset_update", `{"args":{"id":"`+id+`","options":`+options+`}}`)
	var updated assetDTO
	requireStoreTestResponse(t, response, &updated)
}

func setConnectorTestAssetKeyPath(t *testing.T, production *Production, id, keyPath string) {
	t.Helper()
	response := dispatchHostKeyTest(t, production, "asset_update", `{"args":{"id":"`+id+`","keyPath":`+strconv.Quote(keyPath)+`}}`)
	var updated assetDTO
	requireStoreTestResponse(t, response, &updated)
}

func requireConnectorConnectError(t *testing.T, production *Production, assetID, contains string) {
	t.Helper()
	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	if response.OK || response.Error == nil {
		t.Fatalf("connect = %+v, want an error containing %q", response, contains)
	}
	if !strings.Contains(response.Error.Message, contains) {
		t.Fatalf("connect error = %+v, want it to contain %q", response.Error, contains)
	}
}
