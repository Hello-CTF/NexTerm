//go:build darwin || linux

package production

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	gossh "golang.org/x/crypto/ssh"
)

func TestProductionAttachTabMissingTabReturnsNotFoundAndInvalidTabBadParam(t *testing.T) {
	dataDir := durableTestDataDir(t)
	production := newSupervisorTestProduction(t, dataDir, &bridgeTestFactory{}, nil)
	connectLocalDurableTest(t, production)

	cases := []struct {
		name string
		id   string
		want ipc.Code
	}{
		{"missing durable tab", ids.New(), ipc.CodeNotFound},
		{"missing nondurable tab", strings.Repeat("a1b2", 8), ipc.CodeNotFound},
		{"malformed tab id", "not-a-supervisor-id", ipc.CodeBadParam},
		{"empty tab id", "", ipc.CodeBadParam},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			channelID := "attach-tab-codes-" + strings.Repeat("x", index+1)
			response := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tc.id+`","replayBytes":1024}`, channelID, "client-a")
			if response.OK || response.Error == nil || response.Error.Code != tc.want {
				t.Fatalf("attach tab %q = %+v, want %v", tc.id, response, tc.want)
			}
		})
	}
}

func TestProductionAttachTabRecoversDurableTabAfterRestart(t *testing.T) {
	dataDir := durableTestDataDir(t)
	first := newSupervisorTestProduction(t, dataDir, &bridgeTestFactory{}, nil)
	connected := connectLocalDurableTest(t, first)
	attachResponse := dispatchDurableTest(t, first, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "attach-tab-recovery-channel", "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := newSupervisorTestProduction(t, dataDir, &bridgeTestFactory{}, nil)
	recoveredResponse := dispatchDurableTest(t, second, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":65536}`, "attach-tab-recovery-channel", "client-a")
	var recovered attachedTabDTO
	requireStoreTestResponse(t, recoveredResponse, &recovered)
	if recovered.TabID != tabID || recovered.SessionID == "" {
		t.Fatalf("recovered tab = %+v, want tab %s reattached", recovered, tabID)
	}
	requireProductionNull(t, dispatchDurableTest(t, second, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
}

func TestProductionSessionReconnectDoesNotAutoTrustChangedHostKey(t *testing.T) {
	server := newHostKeyTestServer(t)
	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, server.port())

	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	pending := requireHostKeyPending(t, response, false)
	acceptHostKeyTest(t, production, pending)
	connected := connectHostKeyTestAsset(t, production, assetID)

	rotated := server.rotateHostKey(t)
	reconnectResponse := dispatchHostKeyTest(t, production, "session_reconnect", `{"sessionId":"`+connected.ID+`"}`)
	var started bool
	requireStoreTestResponse(t, reconnectResponse, &started)
	if !started {
		t.Fatal("session_reconnect did not start")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if listed := knownHostTestFingerprints(t, production); len(listed) != 1 || listed[0] != pending.Fingerprint {
			t.Fatalf("reconnect auto-trusted a changed host key: %+v", listed)
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))

	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	changed := requireHostKeyPending(t, response, true)
	if changed.Fingerprint != gossh.FingerprintSHA256(rotated) {
		t.Fatalf("changed detail fingerprint = %+v", changed)
	}
	if listed := knownHostTestFingerprints(t, production); len(listed) != 1 || listed[0] != pending.Fingerprint {
		t.Fatalf("reconnect attempts altered the trust ledger: %+v", listed)
	}
}
