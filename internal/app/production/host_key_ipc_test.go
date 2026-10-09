package production

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	gossh "golang.org/x/crypto/ssh"
)

type hostKeyTestServer struct {
	listener net.Listener
	signer   atomic.Pointer[gossh.Signer]
}

func newHostKeyTestServer(t *testing.T) *hostKeyTestServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &hostKeyTestServer{listener: listener}
	server.rotateHostKey(t)
	go server.accept()
	t.Cleanup(func() { _ = server.listener.Close() })
	return server
}

func (s *hostKeyTestServer) rotateHostKey(t *testing.T) gossh.PublicKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	s.signer.Store(&signer)
	return signer.PublicKey()
}

func (s *hostKeyTestServer) port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

func (s *hostKeyTestServer) accept() {
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(connection)
	}
}

func (s *hostKeyTestServer) handle(connection net.Conn) {
	config := &gossh.ServerConfig{
		PasswordCallback: func(metadata gossh.ConnMetadata, _ []byte) (*gossh.Permissions, error) {
			if metadata.User() == "test" {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown test user %q", metadata.User())
		},
	}
	config.AddHostKey(*s.signer.Load())
	serverConnection, channels, requests, err := gossh.NewServerConn(connection, config)
	if err != nil {
		_ = connection.Close()
		return
	}
	defer serverConnection.Close()
	go gossh.DiscardRequests(requests)
	for channel := range channels {
		_ = channel.Reject(gossh.UnknownChannelType, "host key test server")
	}
}

func TestProductionSessionConnectHostKeyConfirmationFlow(t *testing.T) {
	server := newHostKeyTestServer(t)
	firstKey := *server.signer.Load()
	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, server.port())

	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	detail := requireHostKeyPending(t, response, false)
	if detail.Host != "127.0.0.1" || detail.Port != server.port() {
		t.Fatalf("pending detail endpoint = %+v", detail)
	}
	if detail.KeyType != firstKey.PublicKey().Type() || detail.Fingerprint != gossh.FingerprintSHA256(firstKey.PublicKey()) {
		t.Fatalf("pending detail lost fingerprint diagnostics: %+v", detail)
	}
	if len(detail.Known) != 0 {
		t.Fatalf("unknown key reported known fingerprints: %+v", detail.Known)
	}

	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	requireHostKeyPending(t, response, false)
	if listed := knownHostTestFingerprints(t, production); len(listed) != 0 {
		t.Fatalf("rejected confirmation still persisted trust: %+v", listed)
	}

	acceptHostKeyTest(t, production, detail)
	connected := connectHostKeyTestAsset(t, production, assetID)
	if connected.ID == "" {
		t.Fatal("connect after explicit accept returned an empty session")
	}
	if listed := knownHostTestFingerprints(t, production); len(listed) != 1 || listed[0] != detail.Fingerprint {
		t.Fatalf("explicit accept was not recorded: %+v", listed)
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
	reconnected := connectHostKeyTestAsset(t, production, assetID)
	if reconnected.ID == "" {
		t.Fatal("reconnect with a persisted host key failed")
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+reconnected.ID+`"}`))

	secondKey := server.rotateHostKey(t)
	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	changed := requireHostKeyPending(t, response, true)
	if changed.Fingerprint != gossh.FingerprintSHA256(secondKey) {
		t.Fatalf("changed detail fingerprint = %+v", changed)
	}
	if len(changed.Known) != 1 || changed.Known[0].Fingerprint != detail.Fingerprint {
		t.Fatalf("changed detail lost the previous fingerprint: %+v", changed)
	}

	acceptHostKeyTest(t, production, hostKeyDetailDTO{Host: changed.Host, Port: changed.Port, KeyType: changed.KeyType, Fingerprint: detail.Fingerprint})
	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	requireHostKeyPending(t, response, true)

	acceptHostKeyTest(t, production, changed)
	replacement := connectHostKeyTestAsset(t, production, assetID)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+replacement.ID+`"}`))

	server.signer.Store(&firstKey)
	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	requireHostKeyPending(t, response, true)
}

func TestProductionSessionConnectAcceptHostKeyFlagTrustsUnknownKey(t *testing.T) {
	server := newHostKeyTestServer(t)
	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, server.port())

	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`","acceptHostKey":true}}`)
	var connected sessionInfoDTO
	requireStoreTestResponse(t, response, &connected)
	if listed := knownHostTestFingerprints(t, production); len(listed) != 1 {
		t.Fatalf("acceptHostKey connect did not persist the presented key: %+v", listed)
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
	reconnectResponse := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	requireStoreTestResponse(t, reconnectResponse, &connected)
}

func TestProductionSessionConnectHostKeyRotationBetweenConfirmAndReconnect(t *testing.T) {
	server := newHostKeyTestServer(t)
	firstKey := *server.signer.Load()
	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, server.port())

	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	confirmed := requireHostKeyPending(t, response, false)
	acceptHostKeyTest(t, production, confirmed)

	rotated := server.rotateHostKey(t)
	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	repending := requireHostKeyPending(t, response, false)
	if repending.Fingerprint != gossh.FingerprintSHA256(rotated) {
		t.Fatalf("rotated key was not surfaced for confirmation: %+v", repending)
	}
	if listed := knownHostTestFingerprints(t, production); len(listed) != 1 || listed[0] != confirmed.Fingerprint {
		t.Fatalf("rotated key overwrote the confirmed ledger: %+v", listed)
	}

	server.signer.Store(&firstKey)
	connected := connectHostKeyTestAsset(t, production, assetID)
	if connected.ID == "" {
		t.Fatal("connect with the confirmed key failed after a rotation attempt")
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
}

type hostKeyKnownDetailDTO struct {
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
}

type hostKeyDetailDTO struct {
	Host        string                  `json:"host"`
	Port        int                     `json:"port"`
	KeyType     string                  `json:"keyType"`
	Fingerprint string                  `json:"fingerprint"`
	Changed     bool                    `json:"changed"`
	Known       []hostKeyKnownDetailDTO `json:"known"`
}

func newHostKeyTestProduction(t *testing.T) *Production {
	t.Helper()
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{},
		},
		DataDir: t.TempDir(), Desktop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	return production
}

func createHostKeyTestAsset(t *testing.T, production *Production, port int) string {
	t.Helper()
	response := dispatchHostKeyTest(t, production, "asset_create", `{"args":{"kind":"ssh","name":"host-key-test","host":"127.0.0.1","port":`+strconv.Itoa(port)+`,"username":"test","authKind":"password","options":{"password":"host-key-test-secret"}}}`)
	var created assetDTO
	requireStoreTestResponse(t, response, &created)
	return created.ID
}

func connectHostKeyTestAsset(t *testing.T, production *Production, assetID string) sessionInfoDTO {
	t.Helper()
	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	var connected sessionInfoDTO
	requireStoreTestResponse(t, response, &connected)
	return connected
}

func requireHostKeyPending(t *testing.T, response ipc.Response, changed bool) hostKeyDetailDTO {
	t.Helper()
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeHostKeyPending {
		t.Fatalf("response = %+v, want host_key_pending", response)
	}
	encoded, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatalf("host key detail is not serializable: %v", err)
	}
	var detail hostKeyDetailDTO
	if err := json.Unmarshal(encoded, &detail); err != nil {
		t.Fatalf("host key detail is malformed: %v", err)
	}
	if detail.Changed != changed {
		t.Fatalf("detail changed = %v, want %v: %+v", detail.Changed, changed, detail)
	}
	if detail.Host == "" || detail.Port == 0 || detail.KeyType == "" || detail.Fingerprint == "" {
		t.Fatalf("host key detail is incomplete: %+v", detail)
	}
	return detail
}

func acceptHostKeyTest(t *testing.T, production *Production, detail hostKeyDetailDTO) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"host": detail.Host, "port": detail.Port, "keyType": detail.KeyType, "fingerprint": detail.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "known_host_accept", string(payload)))
}

func knownHostTestFingerprints(t *testing.T, production *Production) []string {
	t.Helper()
	response := dispatchHostKeyTest(t, production, "known_host_list", `null`)
	var rows []struct {
		Fingerprint string `json:"fingerprint"`
	}
	requireStoreTestResponse(t, response, &rows)
	fingerprints := make([]string, len(rows))
	for index, row := range rows {
		fingerprints[index] = row.Fingerprint
	}
	return fingerprints
}

func requireProductionNullHostKey(t *testing.T, response ipc.Response) {
	t.Helper()
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response)
	}
}

func dispatchHostKeyTest(t *testing.T, production *Production, command, args string) ipc.Response {
	t.Helper()
	return production.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: command, Args: json.RawMessage(args), ClientID: "client-a",
	}, production.Environment("client-a"))
}
