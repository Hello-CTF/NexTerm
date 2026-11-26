package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	fleetserver "github.com/Hello-CTF/NexTerm/internal/fleet/server"
	"github.com/Hello-CTF/NexTerm/internal/relaymux"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/pion/stun/v4"
)

func newTestFleet(t *testing.T, relay fleetserver.RelaySettings) *fleetserver.Service {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	fleet, err := fleetserver.New(fleetserver.Config{DB: database.GORM(), Accounts: account.New(database.GORM())}, fleetserver.WithRelay(relay))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fleet.Close() })
	return fleet
}

// With the QUIC relay on, the STUN address carries both STUN and QUIC; the self-signed key persists in the data
// directory, so a restart keeps the pin.
func TestStartUDPServicesSharesTheSTUNSocket(t *testing.T) {
	dataDir := t.TempDir()
	relay := fleetserver.RelaySettings{QUIC: true}
	fleet := newTestFleet(t, relay)
	var logs bytes.Buffer
	stop, bound := startUDPServices(fleetserver.P2PSettings{Enabled: true, STUNListen: []string{"127.0.0.1:0"}}, relay, fleet, dataDir, slog.New(slog.NewTextHandler(&logs, nil)))
	defer stop()
	if len(bound) != 1 {
		t.Fatalf("bound = %v: %s", bound, logs.String())
	}
	address := bound[0].String()

	keyPath := filepath.Join(dataDir, fleetserver.RelayQUICKeyFile)
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("relay key mode = %v", info.Mode().Perm())
	}
	cert, err := relaymux.LoadOrCreatePinnedCertificate(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	client, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest, stun.Fingerprint)
	target, _ := net.ResolveUDPAddr("udp", address)
	answered := false
	buffer := make([]byte, 1500)
	for try := 0; try < 5 && !answered; try++ {
		_, _ = client.WriteTo(request.Raw, target)
		_ = client.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := client.ReadFrom(buffer)
		answered = err == nil && stun.IsMessage(buffer[:n])
	}
	if !answered {
		t.Fatalf("no STUN answer on %s: %s", address, logs.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := relaymux.DialQUIC(ctx, address, relaymux.DialConfig{Pin: cert.Pin})
	if err != nil {
		t.Fatalf("QUIC on the STUN socket: %v", err)
	}
	_ = session.Close()
	stop()

	// A restart over the same data directory presents the same key.
	stopAgain, again := startUDPServices(fleetserver.P2PSettings{Enabled: true, STUNListen: []string{"127.0.0.1:0"}}, relay, newTestFleet(t, relay), dataDir, slog.New(slog.NewTextHandler(&logs, nil)))
	defer stopAgain()
	if len(again) != 1 {
		t.Fatalf("bound after restart = %v", again)
	}
	session, err = relaymux.DialQUIC(ctx, again[0].String(), relaymux.DialConfig{Pin: cert.Pin})
	if err != nil {
		t.Fatalf("pin after restart: %v", err)
	}
	_ = session.Close()
}

// With STUN off the relay listens on its own addresses and answers no STUN there.
func TestStartUDPServicesRelayWithoutSTUN(t *testing.T) {
	relay := fleetserver.RelaySettings{QUIC: true, QUICListen: []string{"127.0.0.1:0"}}
	stop, bound := startUDPServices(fleetserver.P2PSettings{Enabled: false}, relay, newTestFleet(t, relay), t.TempDir(), slog.New(slog.DiscardHandler))
	defer stop()
	if len(bound) != 1 {
		t.Fatalf("bound = %v", bound)
	}
	address := bound[0].String()
	client, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	target, _ := net.ResolveUDPAddr("udp", address)
	_, _ = client.WriteTo(stun.MustBuild(stun.TransactionID, stun.BindingRequest, stun.Fingerprint).Raw, target)
	_ = client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := client.ReadFrom(make([]byte, 1500)); err == nil {
		t.Fatal("the relay-only socket answered STUN")
	}
	// Nothing listens without any address.
	stopNone, none := startUDPServices(fleetserver.P2PSettings{Enabled: false}, fleetserver.RelaySettings{QUIC: true}, newTestFleet(t, relay), t.TempDir(), slog.New(slog.DiscardHandler))
	stopNone()
	if len(none) != 0 {
		t.Fatalf("bound without addresses = %v", none)
	}
}
