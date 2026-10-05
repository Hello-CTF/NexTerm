package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	syncservice "github.com/ProbiusOfficial/NexTerm/internal/sync"
)

func TestExposedServerTokenCommandsRequireAdminIdentity(t *testing.T) {
	ctx := context.Background()
	config, fixture := newRealSyncConfig(t, false)
	config.Options.Auth = AuthOn
	_, httpServer := newTestHTTP(t, config)

	issued, err := fixture.service.TokenIssue(ctx, syncservice.TokenIssueRequest{ClientID: "ordinary-client"})
	if err != nil {
		t.Fatal(err)
	}

	management := []string{
		syncservice.CommandToken, syncservice.CommandTokenList, syncservice.CommandTokenIssue,
		syncservice.CommandTokenRotate, syncservice.CommandTokenRevoke,
	}
	for _, command := range management {
		status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", command, map[string]string{TokenHeader: issued.Secret})
		if status != http.StatusOK || body.OK || body.Error == nil || body.Error.Code != ipc.CodeForbidden {
			t.Fatalf("non-admin %s status=%d body=%+v", command, status, body)
		}
	}

	status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", syncservice.CommandDigest, map[string]string{TokenHeader: issued.Secret})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("non-admin peer command status=%d body=%+v", status, body)
	}

	status, body = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", syncservice.CommandToken, map[string]string{TokenHeader: fixture.token})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("admin reveal status=%d body=%+v", status, body)
	}
	var revealed string
	if err := json.Unmarshal(body.Data, &revealed); err != nil || revealed != fixture.token {
		t.Fatalf("admin reveal=%q err=%v", revealed, err)
	}

	if err := fixture.service.TokenRevoke(ctx, issued.Token.ID); err != nil {
		t.Fatal(err)
	}
	for _, command := range append(management, syncservice.CommandDigest) {
		status, _ := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", command, map[string]string{TokenHeader: issued.Secret})
		if status != http.StatusUnauthorized {
			t.Fatalf("revoked non-admin %s status=%d", command, status)
		}
	}
	status, _ = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", syncservice.CommandToken, map[string]string{TokenHeader: fixture.token})
	if status != http.StatusOK {
		t.Fatalf("admin token disturbed by client revocation: status=%d", status)
	}
}

func TestExposedServerDerivesTokenIdentityFromSyncRPCHandler(t *testing.T) {
	config, fixture := newRealSyncConfig(t, false)
	config.Tokens = nil
	config.SyncRPC = fixture.service.PeerHandler()
	config.Options.Auth = AuthOn
	_, httpServer := newTestHTTP(t, config)

	status, body := postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", syncservice.CommandToken, map[string]string{TokenHeader: fixture.token})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("admin reveal through derived verifier status=%d body=%+v", status, body)
	}
	issued, err := fixture.service.TokenIssue(context.Background(), syncservice.TokenIssueRequest{ClientID: "ordinary-client"})
	if err != nil {
		t.Fatal(err)
	}
	status, body = postRPC(t, httpServer.Client(), httpServer.URL+"/rpc", syncservice.CommandToken, map[string]string{TokenHeader: issued.Secret})
	if status != http.StatusOK || body.OK || body.Error == nil || body.Error.Code != ipc.CodeForbidden {
		t.Fatalf("non-admin reveal through derived verifier status=%d body=%+v", status, body)
	}
}
