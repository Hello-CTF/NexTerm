package ssh

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func testLoginConfig(t *testing.T, server *testSSHServer, store HostKeyStore) Config {
	t.Helper()
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.HostKeys = store
	cfg.AutoAcceptUnknown = false
	return cfg
}

func trustServerHostKey(t *testing.T, store HostKeyStore, cfg Config, server *testSSHServer) {
	t.Helper()
	if err := store.PutHostKey(context.Background(), newHostKey(cfg.Host, cfg.Port, server.hostKey), false); err != nil {
		t.Fatal(err)
	}
}

func requireStoreHostKeys(t *testing.T, store HostKeyStore, cfg Config, want int) []HostKey {
	t.Helper()
	keys, err := store.HostKeys(context.Background(), cfg.Host, cfg.Port)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != want {
		t.Fatalf("host key store has %d keys, want %d: %+v", len(keys), want, keys)
	}
	return keys
}

func waitForTestServerIdle(t *testing.T, server *testSSHServer) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for server.active.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("test login left %d server connections open", server.active.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTestLoginSuccess(t *testing.T) {
	server := newTestSSHServer(t, nil)
	store := NewMemoryHostKeyStore()
	cfg := testLoginConfig(t, server, store)
	trustServerHostKey(t, store, cfg, server)

	if err := TestLogin(context.Background(), cfg); err != nil {
		t.Fatalf("test login: %v", err)
	}
	keys := requireStoreHostKeys(t, store, cfg, 1)
	if keys[0].Fingerprint != gossh.FingerprintSHA256(server.hostKey) {
		t.Fatalf("test login replaced the trusted key: %+v", keys)
	}
	waitForTestServerIdle(t, server)
}

func TestTestLoginPublicKeyAuth(t *testing.T) {
	_, private, signer := generateClientKey(t)
	server := newTestSSHServer(t, signer.PublicKey())
	store := NewMemoryHostKeyStore()
	cfg := testLoginConfig(t, server, store)
	block, err := gossh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Auth = AuthConfig{Method: AuthKey, KeyPEM: pem.EncodeToMemory(block)}
	trustServerHostKey(t, store, cfg, server)

	if err := TestLogin(context.Background(), cfg); err != nil {
		t.Fatalf("public key test login: %v", err)
	}
	waitForTestServerIdle(t, server)
}

func TestTestLoginKeyboardInteractiveAuth(t *testing.T) {
	server := newTestSSHServerConfig(t, nil, func(config *gossh.ServerConfig) {
		config.KeyboardInteractiveCallback = func(metadata gossh.ConnMetadata, challenge gossh.KeyboardInteractiveChallenge) (*gossh.Permissions, error) {
			if metadata.User() == "test" {
				answers, err := challenge("test", "password", []string{"password"}, []bool{false})
				if err == nil && len(answers) == 1 && answers[0] == "secret" {
					return nil, nil
				}
			}
			return nil, fmt.Errorf("interactive test auth failed")
		}
	})
	store := NewMemoryHostKeyStore()
	cfg := testLoginConfig(t, server, store)
	cfg.Auth = AuthConfig{Method: AuthKeyboardInteractive, Password: "secret"}
	trustServerHostKey(t, store, cfg, server)

	if err := TestLogin(context.Background(), cfg); err != nil {
		t.Fatalf("keyboard-interactive test login: %v", err)
	}
	waitForTestServerIdle(t, server)
}

func TestTestLoginRejectsUnknownHostKeyWithoutPersisting(t *testing.T) {
	server := newTestSSHServer(t, nil)
	store := NewMemoryHostKeyStore()
	cfg := testLoginConfig(t, server, store)
	cfg.AutoAcceptUnknown = true

	err := TestLogin(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindHostKeyPending)
	if connectErr.Op != "host key verification" {
		t.Fatalf("pending host key op = %q", connectErr.Op)
	}
	if !errors.Is(err, ErrHostKeyPending) {
		t.Fatalf("pending host key lost sentinel: %v", err)
	}
	var keyErr *HostKeyError
	if !errors.As(err, &keyErr) || !keyErr.Pending || keyErr.Presented.Fingerprint != gossh.FingerprintSHA256(server.hostKey) {
		t.Fatalf("pending host key details = %#v", err)
	}
	if got := ClassifyLoginFailure(err); got != LoginFailureHostKey {
		t.Fatalf("pending host key class = %q", got)
	}
	requireStoreHostKeys(t, store, cfg, 0)

	presented := newHostKey(cfg.Host, cfg.Port, server.hostKey)
	cfg.HostKeyApproval = &HostKeyApproval{Fingerprint: presented.Fingerprint}
	err = TestLogin(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindHostKeyPending)
	requireStoreHostKeys(t, store, cfg, 0)
	waitForTestServerIdle(t, server)
}

func TestTestLoginRejectsChangedHostKey(t *testing.T) {
	server := newTestSSHServer(t, nil)
	store := NewMemoryHostKeyStore()
	cfg := testLoginConfig(t, server, store)
	recorded := newHostKey(cfg.Host, cfg.Port, hostKeyForTest(t))
	if err := store.PutHostKey(context.Background(), recorded, false); err != nil {
		t.Fatal(err)
	}

	err := TestLogin(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindHostKeyChanged)
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("changed host key lost sentinel: %v", err)
	}
	var keyErr *HostKeyError
	if !errors.As(err, &keyErr) || keyErr.Pending || keyErr.Presented.Fingerprint != gossh.FingerprintSHA256(server.hostKey) {
		t.Fatalf("changed host key details = %#v", err)
	}
	if len(keyErr.Known) != 1 || keyErr.Known[0].Fingerprint != recorded.Fingerprint {
		t.Fatalf("changed host key known keys = %+v", keyErr.Known)
	}
	if got := ClassifyLoginFailure(err); got != LoginFailureHostKey {
		t.Fatalf("changed host key class = %q", got)
	}
	keys := requireStoreHostKeys(t, store, cfg, 1)
	if keys[0].Fingerprint != recorded.Fingerprint {
		t.Fatalf("test login overwrote the recorded key: %+v", keys)
	}
}

func TestTestLoginAcceptsApprovedLedgerKeyWithoutPersisting(t *testing.T) {
	server := newTestSSHServer(t, nil)
	store := &ledgerHostKeyStore{MemoryHostKeyStore: NewMemoryHostKeyStore()}
	cfg := testLoginConfig(t, server, store)
	store.approved = []HostKey{newHostKey(cfg.Host, cfg.Port, server.hostKey)}

	if err := TestLogin(context.Background(), cfg); err != nil {
		t.Fatalf("ledger-approved test login: %v", err)
	}
	requireStoreHostKeys(t, store, cfg, 0)
	waitForTestServerIdle(t, server)
}

func TestTestLoginReportsChangedAgainstApprovedLedgerKey(t *testing.T) {
	server := newTestSSHServer(t, nil)
	store := &ledgerHostKeyStore{MemoryHostKeyStore: NewMemoryHostKeyStore()}
	cfg := testLoginConfig(t, server, store)
	store.approved = []HostKey{newHostKey(cfg.Host, cfg.Port, hostKeyForTest(t))}

	err := TestLogin(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindHostKeyChanged)
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("ledger mismatch lost the changed sentinel: %v", err)
	}
	var keyErr *HostKeyError
	if !errors.As(err, &keyErr) || keyErr.Pending || keyErr.Presented.Fingerprint != gossh.FingerprintSHA256(server.hostKey) {
		t.Fatalf("ledger mismatch details = %#v", err)
	}
	if len(keyErr.Known) != 1 || keyErr.Known[0].Fingerprint != store.approved[0].Fingerprint {
		t.Fatalf("ledger mismatch known keys = %+v", keyErr.Known)
	}
	if got := ClassifyLoginFailure(err); got != LoginFailureHostKey {
		t.Fatalf("ledger mismatch class = %q", got)
	}
	requireStoreHostKeys(t, store, cfg, 0)
	waitForTestServerIdle(t, server)
}

func TestTestLoginAuthFailure(t *testing.T) {
	server := newTestSSHServer(t, nil)
	store := NewMemoryHostKeyStore()
	cfg := testLoginConfig(t, server, store)
	cfg.Auth.Password = "wrong-password"
	trustServerHostKey(t, store, cfg, server)

	err := TestLogin(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindAuth)
	if connectErr.Op != "authentication" {
		t.Fatalf("auth failure op = %q", connectErr.Op)
	}
	if got := ClassifyLoginFailure(err); got != LoginFailureAuth {
		t.Fatalf("auth failure class = %q", got)
	}
	waitForTestServerIdle(t, server)
}

func TestTestLoginAuthSetupFailure(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testLoginConfig(t, server, NewMemoryHostKeyStore())
	cfg.Auth = AuthConfig{Method: AuthKey, KeyPEM: []byte("not a key")}

	err := TestLogin(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindAuth)
	if connectErr.Op != "authentication setup" {
		t.Fatalf("auth setup failure op = %q", connectErr.Op)
	}
	if got := ClassifyLoginFailure(err); got != LoginFailureAuth {
		t.Fatalf("auth setup failure class = %q", got)
	}
}

func TestTestLoginNetworkFailureClasses(t *testing.T) {
	store := NewMemoryHostKeyStore()
	auth := AuthConfig{Method: AuthPassword, Password: "secret"}

	cfg := Config{Host: "127.0.0.1", Port: closedTCPPort(t), User: "test", Auth: auth, HostKeys: store, ConnectTimeout: 2 * time.Second}
	err := TestLogin(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindRefused)
	if got := ClassifyLoginFailure(err); got != LoginFailureNetwork {
		t.Fatalf("refused class = %q", got)
	}

	listener := stallingTCPListener(t)
	cfg = Config{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "test", Auth: auth, HostKeys: store, ConnectTimeout: 300 * time.Millisecond}
	err = TestLogin(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindTimeout)
	if got := ClassifyLoginFailure(err); got != LoginFailureNetwork {
		t.Fatalf("timeout class = %q", got)
	}

	useNXDOMAINResolver(t)
	cfg = Config{Host: "nonexistent.invalid.", Port: 22, User: "test", Auth: auth, HostKeys: store, ConnectTimeout: 5 * time.Second}
	err = TestLogin(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindDNS)
	if got := ClassifyLoginFailure(err); got != LoginFailureNetwork {
		t.Fatalf("dns class = %q", got)
	}
}

func TestTestLoginConfigFailure(t *testing.T) {
	err := TestLogin(context.Background(), Config{Host: " ", Port: 22, User: "test", HostKeys: NewMemoryHostKeyStore()})
	requireConnectError(t, err, ErrorKindConfig)
	if got := ClassifyLoginFailure(err); got != LoginFailureConfig {
		t.Fatalf("config class = %q", got)
	}

	err = TestLogin(context.Background(), Config{Host: "example.test", Port: 22, User: "test"})
	connectErr := requireConnectError(t, err, ErrorKindConfig)
	if connectErr.Op != "configuration" {
		t.Fatalf("config op = %q", connectErr.Op)
	}

	store := NewMemoryHostKeyStore()
	jumpCfg := Config{Host: "jump.example", Port: 70000, User: "test", HostKeys: store}
	cfg := Config{Host: "target.example", Port: 22, User: "test", HostKeys: store, Jump: &jumpCfg}
	err = TestLogin(context.Background(), cfg)
	connectErr = requireConnectError(t, err, ErrorKindConfig)
	if connectErr.Host != "jump.example" || connectErr.Port != 70000 {
		t.Fatalf("jump config failure endpoint = %+v", connectErr)
	}
}

func TestTestLoginThroughJump(t *testing.T) {
	jump := newTestSSHServer(t, nil)
	target := newTestSSHServer(t, nil)
	store := NewMemoryHostKeyStore()
	jumpCfg := testLoginConfig(t, jump, store)
	targetCfg := testLoginConfig(t, target, store)
	targetCfg.Jump = &jumpCfg

	err := TestLogin(context.Background(), targetCfg)
	requireConnectError(t, err, ErrorKindHostKeyPending)
	var keyErr *HostKeyError
	if !errors.As(err, &keyErr) || keyErr.Presented.Port != jumpCfg.Port {
		t.Fatalf("jump host key pending did not name the jump hop: %v", err)
	}

	trustServerHostKey(t, store, jumpCfg, jump)
	err = TestLogin(context.Background(), targetCfg)
	requireConnectError(t, err, ErrorKindHostKeyPending)
	if !errors.As(err, &keyErr) || keyErr.Presented.Port != targetCfg.Port {
		t.Fatalf("target host key pending did not name the target: %v", err)
	}

	jumpCfg.Auth.Password = "wrong-password"
	trustServerHostKey(t, store, targetCfg, target)
	err = TestLogin(context.Background(), targetCfg)
	connectErr := requireConnectError(t, err, ErrorKindAuth)
	if connectErr.Host != jumpCfg.Host || connectErr.Port != jumpCfg.Port || connectErr.Jump != "" {
		t.Fatalf("jump auth failure hop = %+v", connectErr)
	}
	if got := ClassifyLoginFailure(err); got != LoginFailureAuth {
		t.Fatalf("jump auth failure class = %q", got)
	}

	jumpCfg.Auth.Password = "secret"
	if err := TestLogin(context.Background(), targetCfg); err != nil {
		t.Fatalf("test login through jump: %v", err)
	}
	waitForTestServerIdle(t, jump)
	waitForTestServerIdle(t, target)
}

func TestTestLoginJumpChainBoundedByRootTimeout(t *testing.T) {
	target := newTestSSHServer(t, nil)
	store := NewMemoryHostKeyStore()
	listener := stallingTCPListener(t)

	jumpCfg := Config{
		Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "test",
		Auth: AuthConfig{Method: AuthPassword, Password: "secret"}, HostKeys: store,
	}
	targetCfg := testLoginConfig(t, target, store)
	targetCfg.Jump = &jumpCfg
	targetCfg.ConnectTimeout = 300 * time.Millisecond

	started := time.Now()
	err := TestLogin(context.Background(), targetCfg)
	elapsed := time.Since(started)
	connectErr := requireConnectError(t, err, ErrorKindTimeout)
	if connectErr.Host != jumpCfg.Host || connectErr.Port != jumpCfg.Port {
		t.Fatalf("stalling jump failure hop = %+v", connectErr)
	}
	if got := ClassifyLoginFailure(err); got != LoginFailureNetwork {
		t.Fatalf("stalling jump failure class = %q", got)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("jump chain test login took %v, want the 300ms root timeout to bound the chain", elapsed)
	}
}

func TestClassifyLoginFailure(t *testing.T) {
	sentinel := errors.New("vault locked")
	tests := []struct {
		name string
		err  error
		want LoginFailureClass
	}{
		{"nil", nil, ""},
		{"vault", &VaultError{Err: sentinel}, LoginFailureVault},
		{"wrapped vault", fmt.Errorf("resolve credential: %w", &VaultError{Err: sentinel}), LoginFailureVault},
		{"bare host key", &HostKeyError{Pending: true, Presented: HostKey{Host: "example.test", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "SHA256:abc"}}, LoginFailureHostKey},
		{"config", newConnectError(ErrorKindConfig, "configuration", "example.test", 22, "", errors.New("bad config")), LoginFailureConfig},
		{"host key pending", newConnectError(ErrorKindHostKeyPending, "host key verification", "example.test", 22, "", ErrHostKeyPending), LoginFailureHostKey},
		{"host key changed", newConnectError(ErrorKindHostKeyChanged, "host key verification", "example.test", 22, "", ErrHostKeyChanged), LoginFailureHostKey},
		{"auth", newConnectError(ErrorKindAuth, "authentication", "example.test", 22, "", errors.New("unable to authenticate")), LoginFailureAuth},
		{"refused", newConnectError(ErrorKindRefused, "dial", "example.test", 22, "", errors.New("refused")), LoginFailureNetwork},
		{"timeout", newConnectError(ErrorKindTimeout, "handshake", "example.test", 22, "", context.DeadlineExceeded), LoginFailureNetwork},
		{"canceled", newConnectError(ErrorKindCanceled, "dial", "example.test", 22, "", context.Canceled), LoginFailureNetwork},
		{"unknown", errors.New("boom"), LoginFailureNetwork},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyLoginFailure(test.err); got != test.want {
				t.Fatalf("ClassifyLoginFailure() = %q, want %q", got, test.want)
			}
		})
	}

	if !errors.Is(&VaultError{Err: sentinel}, sentinel) {
		t.Fatal("VaultError does not unwrap to its cause")
	}
	if got := (&VaultError{Err: sentinel}).Error(); got != "SSH credential vault: vault locked" {
		t.Fatalf("VaultError.Error() = %q", got)
	}
}

func TestDescribeLoginFailure(t *testing.T) {
	pending := &HostKeyError{Pending: true, Presented: HostKey{Host: "h", Port: 22, Fingerprint: "SHA256:abc"}}
	changed := &HostKeyError{Presented: HostKey{Host: "h", Port: 22, Fingerprint: "SHA256:new"}}
	secret := "hunter2-secret"
	cases := []struct {
		name     string
		err      error
		portOnly bool
		kind     string
		contains []string
	}{
		{"nil", nil, false, "", nil},
		{"pending", newConnectError(ErrorKindHostKeyPending, "host key verification", "h", 22, "", pending), false, "host_key_pending", []string{"SHA256:abc", "不会自动信任"}},
		{"changed", newConnectError(ErrorKindHostKeyChanged, "host key verification", "h", 22, "", changed), false, "host_key_changed", []string{"SHA256:new", "不一致"}},
		{"auth", newConnectError(ErrorKindAuth, "authentication", "h", 22, "", errors.New("ssh: unable to authenticate "+secret)), false, "auth", []string{"已连通", "被拒绝"}},
		{"key unreadable", newConnectError(ErrorKindAuth, "authentication setup", "h", 22, "", errors.New("read SSH private key: open /x/"+secret+": no such file")), false, "credential", []string{"私钥文件"}},
		{"passphrase", newConnectError(ErrorKindAuth, "authentication setup", "h", 22, "", errors.New("parse encrypted SSH private key: x")), false, "credential", []string{"口令"}},
		{"agent", newConnectError(ErrorKindAuth, "authentication setup", "h", 22, "", errors.New("SSH agent socket is not configured")), false, "credential", []string{"Agent"}},
		{"refused ssh", newConnectError(ErrorKindRefused, "dial", "h", 22, "", errors.New("refused")), false, "refused", []string{"拒绝连接"}},
		{"refused port", newConnectError(ErrorKindRefused, "dial", "h", 3306, "", errors.New("refused")), true, "refused", []string{"h:3306", "拒绝了连接"}},
		{"timeout port", newConnectError(ErrorKindTimeout, "dial", "h", 3306, "", errors.New("t")), true, "timeout", []string{"超时"}},
		{"canceled", newConnectError(ErrorKindCanceled, "dial", "h", 22, "", context.Canceled), false, "canceled", []string{"已取消"}},
		{"unclassified", errors.New("boom " + secret), false, "internal", []string{"重试"}},
	}
	for _, tc := range cases {
		kind, message := DescribeLoginFailure(tc.err, tc.portOnly)
		if kind != tc.kind {
			t.Fatalf("%s: kind = %q, want %q", tc.name, kind, tc.kind)
		}
		for _, want := range tc.contains {
			if !strings.Contains(message, want) {
				t.Fatalf("%s: message %q lacks %q", tc.name, message, want)
			}
		}
		if strings.Contains(message, secret) {
			t.Fatalf("%s: message leaks underlying error text: %q", tc.name, message)
		}
	}
}
