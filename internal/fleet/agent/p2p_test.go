package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestChannelDeliversP2PRevokeAndKeepsRunning(t *testing.T) {
	fake := newWSFake(t, func(conn *websocket.Conn, hello HelloMessage) error {
		for _, message := range []serverMessage{
			{Type: "hello_ok"},
			{Type: "p2p_revoke", GrantID: "g-1", Reason: "session_revoked"},
			{Type: "p2p_revoke"},
			{Type: "revoke"},
		} {
			if err := writeServerMessage(conn, message); err != nil {
				return err
			}
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	channel, err := DialChannel(ctx, wsURL(fake.server.URL), testHello(), DialPolicy{}, time.Hour, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	type revocation struct{ grantID, reason string }
	received := make(chan revocation, 4)
	err = channel.Run(ctx, channelHandlers{onP2PRevoke: func(grantID, reason string) { received <- revocation{grantID, reason} }})
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("Run = %v, want the channel to keep running until revoke", err)
	}
	close(received)
	var got []revocation
	for item := range received {
		got = append(got, item)
	}
	if len(got) != 1 || got[0] != (revocation{"g-1", "session_revoked"}) {
		t.Fatalf("revocations = %+v", got)
	}
}

func TestLoadOrCreateP2PKeyPersistsOneKey(t *testing.T) {
	path := P2PKeyPath(t.TempDir())
	created, err := LoadOrCreateP2PKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("key mode = %v", info.Mode().Perm())
		}
	}
	loaded, err := LoadOrCreateP2PKey(path)
	if err != nil || !loaded.Equal(created) {
		t.Fatalf("reload = %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("key directory holds %d entries, want only the key", len(entries))
	}
	if err := os.WriteFile(path, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateP2PKey(path); err == nil {
		t.Fatal("a corrupt key file was accepted")
	}
	if data, _ := os.ReadFile(path); string(data) != "not a key\n" {
		t.Fatal("a corrupt key file was replaced")
	}
}

// Plain HTTP and unverified TLS endpoints neither register the device key nor fetch the trusted grant keys,
// and send nothing to the server.
func TestP2PRefusesEndpointsWithoutTLSVerification(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "unexpected", http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "unexpected", http.StatusTeapot)
	}))
	t.Cleanup(tlsServer.Close)
	public, _, _ := ed25519.GenerateKey(nil)
	for name, entry := range map[string]BaseURLEntry{
		"plain http":     {URL: server.URL},
		"unverified tls": {URL: tlsServer.URL, Insecure: true},
	} {
		client, err := NewEndpointClient(entry)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.RegisterP2PKey(context.Background(), "d", "s", public); !errors.Is(err, ErrP2PKeyInsecureEndpoint) {
			t.Fatalf("%s: register err = %v", name, err)
		}
		if _, err := client.P2PConfig(context.Background(), "d", "s"); !errors.Is(err, ErrP2PKeyInsecureEndpoint) {
			t.Fatalf("%s: config err = %v", name, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d requests reached the server", n)
	}
}

func TestP2PEndpointVerified(t *testing.T) {
	for _, tc := range []struct {
		url      string
		insecure bool
		want     bool
	}{
		{"https://nx.example.com", false, true},
		{"https://nx.example.com", true, false},
		{"http://nx.example.com", false, false},
		{"http://127.0.0.1:8080", false, false},
		{"://bad", false, false},
	} {
		if got := P2PEndpointVerified(tc.url, tc.insecure); got != tc.want {
			t.Errorf("P2PEndpointVerified(%q, %v) = %v", tc.url, tc.insecure, got)
		}
	}
}
