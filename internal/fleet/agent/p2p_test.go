package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func TestRegisterP2PKeyRefusesEndpointsWithoutTLSVerification(t *testing.T) {
	client, err := NewEndpointClient(BaseURLEntry{URL: "https://nexterm.example.com", Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	public, _, _ := ed25519.GenerateKey(nil)
	if _, err := client.RegisterP2PKey(context.Background(), "d", "s", public); !errors.Is(err, ErrP2PKeyInsecureEndpoint) {
		t.Fatalf("err = %v", err)
	}
}
