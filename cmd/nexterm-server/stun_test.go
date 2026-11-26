package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	fleetserver "github.com/Hello-CTF/NexTerm/internal/fleet/server"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestStartSTUNServerSkipsUnbindableAddressesVisibly(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	fleet, err := fleetserver.New(fleetserver.Config{DB: database.GORM(), Accounts: account.New(database.GORM())})
	if err != nil {
		t.Fatal(err)
	}
	busy, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	if server := startSTUNServer(fleetserver.P2PSettings{Enabled: false, STUNListen: []string{"127.0.0.1:0"}}, fleet, logger); server != nil {
		t.Fatal("responder started with direct connections off")
	}
	server := startSTUNServer(fleetserver.P2PSettings{Enabled: true, STUNListen: []string{busy.LocalAddr().String(), "127.0.0.1:0"}}, fleet, logger)
	if server == nil {
		t.Fatal("responder not started")
	}
	defer server.Close()
	if ports := server.Ports(); len(ports) != 1 {
		t.Fatalf("ports = %v", ports)
	}
	if !strings.Contains(logs.String(), "STUN listener not started") {
		t.Fatalf("skipped address not logged: %s", logs.String())
	}
	if server := startSTUNServer(fleetserver.P2PSettings{Enabled: true, STUNListen: []string{busy.LocalAddr().String()}}, fleet, logger); server != nil {
		t.Fatal("a responder without any listener was returned")
	}
}
