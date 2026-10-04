package ssh

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func probeConfig(t *testing.T, server *testSSHServer, store HostKeyStore) Config {
	t.Helper()
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.HostKeys = store
	return cfg
}

func TestProbeHostKeyStates(t *testing.T) {
	server := newTestSSHServer(t, nil)
	store := NewMemoryHostKeyStore()
	cfg := probeConfig(t, server, store)

	result, err := ProbeHostKey(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != HostKeyProbePending {
		t.Fatalf("unknown key state = %q", result.State)
	}
	if result.Fingerprint != gossh.FingerprintSHA256(server.hostKey) || result.KeyType != server.hostKey.Type() {
		t.Fatalf("probe fingerprint = %+v", result)
	}
	if keys, _ := store.HostKeys(context.Background(), result.Host, result.Port); len(keys) != 0 {
		t.Fatalf("probe persisted a pending key: %+v", keys)
	}

	if err := store.PutHostKey(context.Background(), newHostKey(result.Host, result.Port, server.hostKey), false); err != nil {
		t.Fatal(err)
	}
	result, err = ProbeHostKey(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != HostKeyProbeKnown {
		t.Fatalf("recorded key state = %q", result.State)
	}

	rotated := hostKeyForTest(t)
	if err := store.PutHostKey(context.Background(), newHostKey(result.Host, result.Port, rotated), true); err != nil {
		t.Fatal(err)
	}
	result, err = ProbeHostKey(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != HostKeyProbeChanged {
		t.Fatalf("rotated key state = %q", result.State)
	}
	if result.Fingerprint != gossh.FingerprintSHA256(server.hostKey) {
		t.Fatalf("changed probe fingerprint = %q", result.Fingerprint)
	}
	if len(result.Known) != 1 || result.Known[0].Fingerprint != gossh.FingerprintSHA256(rotated) {
		t.Fatalf("changed probe known keys = %+v", result.Known)
	}
	keys, _ := store.HostKeys(context.Background(), result.Host, result.Port)
	if len(keys) != 1 || keys[0].Fingerprint != gossh.FingerprintSHA256(rotated) {
		t.Fatalf("probe overwrote the trust store: %+v", keys)
	}
}

func TestProbeHostKeyClassifiedFailures(t *testing.T) {
	store := NewMemoryHostKeyStore()

	cfg := Config{Host: "127.0.0.1", Port: closedTCPPort(t), User: "test", HostKeys: store, ConnectTimeout: 2 * time.Second}
	_, err := ProbeHostKey(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindRefused)

	cfg = Config{Host: "nonexistent.invalid.", Port: 22, User: "test", HostKeys: store, ConnectTimeout: 5 * time.Second}
	_, err = ProbeHostKey(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindDNS)

	listener := stallingTCPListener(t)
	cfg = Config{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "test", HostKeys: store, ConnectTimeout: 300 * time.Millisecond}
	_, err = ProbeHostKey(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindTimeout)

	server := newTestSSHServer(t, nil)
	cfg = probeConfig(t, server, nil)
	_, err = ProbeHostKey(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindConfig)
}

func TestProbeHostKeySanitizesProxyCredentials(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := probeConfig(t, server, NewMemoryHostKeyStore())
	cfg.ProxyURL = "socks5://proxyuser:topsecret@127.0.0.1:1"
	_, err := ProbeHostKey(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindRefused)
	if connectErr.Proxy != "socks5://127.0.0.1:1" {
		t.Fatalf("probe proxy endpoint = %q", connectErr.Proxy)
	}
	if strings.Contains(connectErr.Error(), "topsecret") || strings.Contains(connectErr.Error(), "proxyuser") {
		t.Fatalf("probe leaked proxy credentials: %v", connectErr)
	}
}

func TestProbeHostKeyThroughJumpChain(t *testing.T) {
	jump := newTestSSHServer(t, nil)
	target := newTestSSHServer(t, nil)
	store := NewMemoryHostKeyStore()
	jumpCfg := probeConfig(t, jump, store)
	jumpCfg.AutoAcceptUnknown = false
	targetCfg := probeConfig(t, target, store)
	targetCfg.AutoAcceptUnknown = false
	targetCfg.Jump = &jumpCfg

	result, err := ProbeHostKey(context.Background(), targetCfg)
	if err == nil {
		t.Fatal("probe through an unconfirmed jump host key succeeded")
	}
	var keyErr *HostKeyError
	if !errors.As(err, &keyErr) || keyErr.Presented.Port != jumpCfg.Port {
		t.Fatalf("probe did not surface the jump hop host key: %v", err)
	}

	if err := store.PutHostKey(context.Background(), newHostKey(jumpCfg.Host, jumpCfg.Port, jump.hostKey), false); err != nil {
		t.Fatal(err)
	}
	result, err = ProbeHostKey(context.Background(), targetCfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != HostKeyProbePending || result.Port != targetCfg.Port || result.Fingerprint != gossh.FingerprintSHA256(target.hostKey) {
		t.Fatalf("probe through jump = %+v", result)
	}
	if keys, _ := store.HostKeys(context.Background(), targetCfg.Host, targetCfg.Port); len(keys) != 0 {
		t.Fatalf("probe persisted the target key: %+v", keys)
	}

	if err := store.PutHostKey(context.Background(), newHostKey(targetCfg.Host, targetCfg.Port, target.hostKey), false); err != nil {
		t.Fatal(err)
	}
	result, err = ProbeHostKey(context.Background(), targetCfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != HostKeyProbeKnown {
		t.Fatalf("recorded target through jump state = %q", result.State)
	}
}

func TestProbeThroughJumpClassifiedFailures(t *testing.T) {
	jump := newTestSSHServer(t, nil)
	target := newTestSSHServer(t, nil)
	store := NewMemoryHostKeyStore()
	if err := store.PutHostKey(context.Background(), newHostKey(jumpHost(t, jump), jumpPort(t, jump), jump.hostKey), false); err != nil {
		t.Fatal(err)
	}

	jumpCfg := probeConfig(t, jump, store)
	jumpCfg.Auth = AuthConfig{Method: AuthPassword, Password: "wrong"}
	targetCfg := probeConfig(t, target, store)
	targetCfg.Jump = &jumpCfg
	_, err := ProbeHostKey(context.Background(), targetCfg)
	connectErr := requireConnectError(t, err, ErrorKindAuth)
	if connectErr.Host != jumpCfg.Host || connectErr.Port != jumpCfg.Port {
		t.Fatalf("jump auth failure hop = %+v", connectErr)
	}

	jumpCfg = probeConfig(t, jump, store)
	dead := deadEndpoint(t)
	targetCfg = probeConfig(t, target, store)
	targetCfg.Host, targetCfg.Port = dead.host, dead.port
	targetCfg.Jump = &jumpCfg
	_, err = ProbeHostKey(context.Background(), targetCfg)
	connectErr = requireConnectError(t, err, ErrorKindRefused)
	if connectErr.Op != "jump dial" || connectErr.Jump != endpointString(jumpCfg.Host, jumpCfg.Port) {
		t.Fatalf("jump dial failure context = %+v", connectErr)
	}
	if connectErr.Host != dead.host || connectErr.Port != dead.port {
		t.Fatalf("jump dial failure lost the target endpoint: %+v", connectErr)
	}

	err = ProbeReachability(context.Background(), targetCfg, 2*time.Second)
	connectErr = requireConnectError(t, err, ErrorKindRefused)
	if connectErr.Op != "jump dial" {
		t.Fatalf("reachability through jump op = %q", connectErr.Op)
	}
}

func TestProbeReachability(t *testing.T) {
	server := newTestSSHServer(t, nil)
	host, port := splitAddress(t, server.listener.Addr().String())
	if err := ProbeReachability(context.Background(), Config{Host: host, Port: port}, 2*time.Second); err != nil {
		t.Fatalf("reachability against live server: %v", err)
	}
	err := ProbeReachability(context.Background(), Config{Host: host, Port: closedTCPPort(t)}, 2*time.Second)
	connectErr := requireConnectError(t, err, ErrorKindRefused)
	if connectErr.Op != "dial" {
		t.Fatalf("reachability op = %q", connectErr.Op)
	}
	err = ProbeReachability(context.Background(), Config{Host: host, Port: port, ProxyURL: "socks5://127.0.0.1:1"}, 2*time.Second)
	connectErr = requireConnectError(t, err, ErrorKindRefused)
	if connectErr.Op != "proxy connect" || connectErr.Proxy == "" {
		t.Fatalf("proxied reachability context = %+v", connectErr)
	}
	if err := ProbeReachability(context.Background(), Config{Host: " ", Port: 22}, time.Second); err == nil {
		t.Fatal("invalid endpoint reachability succeeded")
	}
}
