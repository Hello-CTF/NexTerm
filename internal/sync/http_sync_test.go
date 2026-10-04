package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func callPeer(t *testing.T, server *httptest.Server, path, command, token string, headers map[string]string) (int, rpcWireResponse) {
	t.Helper()
	body, err := json.Marshal(ipc.Request{Command: command, Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(TokenHeader, token)
	}
	for name, value := range headers {
		req.Header[name] = []string{value}
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope rpcWireResponse
	_ = json.NewDecoder(response.Body).Decode(&envelope)
	return response.StatusCode, envelope
}

func TestPeerHandlerAuthorizationAndRestrictedSurface(t *testing.T) {
	first := newTestInstance(t, false, WithPlatform("lazycat"))
	second := newTestInstance(t, false)
	token, err := first.service.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	otherToken, _ := second.service.Token(context.Background())
	server := httptest.NewServer(first.service.PeerHandler())
	t.Cleanup(server.Close)

	for name, candidate := range map[string]string{"missing": "", "wrong": "not-the-token", "cross-instance": otherToken} {
		status, envelope := callPeer(t, server, "/sync/rpc", CommandDigest, candidate, nil)
		if status != http.StatusUnauthorized || envelope.OK || envelope.Error == nil || envelope.Error.Code != ipc.CodeForbidden {
			t.Fatalf("%s token status=%d envelope=%+v", name, status, envelope)
		}
	}
	status, envelope := callPeer(t, server, "/sync/rpc", CommandDigest, token, nil)
	if status != http.StatusOK || !envelope.OK {
		t.Fatalf("valid token status=%d envelope=%+v", status, envelope)
	}
	status, envelope = callPeer(t, server, "/sync/rpc", CommandDigest, "wrong", map[string]string{PlatformUserHeader: ""})
	if status != http.StatusOK || !envelope.OK {
		t.Fatalf("platform gateway identity status=%d envelope=%+v", status, envelope)
	}
	untrusted := newTestInstance(t, false)
	untrustedServer := httptest.NewServer(untrusted.service.PeerHandler())
	t.Cleanup(untrustedServer.Close)
	status, envelope = callPeer(t, untrustedServer, "/sync/rpc", CommandDigest, "wrong", map[string]string{PlatformUserHeader: "forged"})
	if status != http.StatusUnauthorized || envelope.OK {
		t.Fatalf("forged platform identity without platform trust status=%d envelope=%+v", status, envelope)
	}
	handler := first.service.PeerHandler()
	if valid, err := handler.VerifyToken(context.Background(), token); err != nil || !valid {
		t.Fatalf("handler VerifyToken = %v, %v", valid, err)
	}
	if !handler.PlatformTrusted() {
		t.Fatal("handler PlatformTrusted = false with platform configured")
	}
	if untrusted.service.PeerHandler().PlatformTrusted() {
		t.Fatal("handler PlatformTrusted = true without platform")
	}
	status, _ = callPeer(t, server, "/rpc", CommandDigest, token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("unrelated RPC surface should not be mounted, got %d", status)
	}
	status, envelope = callPeer(t, server, "/sync/rpc", "sync_token", token, nil)
	if status != http.StatusOK || envelope.OK || envelope.Error == nil || envelope.Error.Code != ipc.CodeNotFound {
		t.Fatalf("non-peer command status=%d envelope=%+v", status, envelope)
	}

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/sync/rpc", nil)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d", response.StatusCode)
	}
}

func TestNormalizeBaseHTTPRules(t *testing.T) {
	valid := map[string]string{
		" http://localhost:8080/ ":       "http://localhost:8080",
		"http://127.0.0.1":               "http://127.0.0.1",
		"http://10.0.0.8/base/":          "http://10.0.0.8/base",
		"http://192.168.1.2":             "http://192.168.1.2",
		"http://169.254.1.2":             "http://169.254.1.2",
		"http://[::1]":                   "http://[::1]",
		"http://box.local":               "http://box.local",
		"https://example.com/base/":      "https://example.com/base",
		"https://[2001:4860:4860::8888]": "https://[2001:4860:4860::8888]",
	}
	for raw, want := range valid {
		got, err := normalizeBase(raw)
		if err != nil || got != want {
			t.Fatalf("normalizeBase(%q)=%q want=%q err=%v", raw, got, want, err)
		}
	}
	invalid := []string{
		"example.com", "ftp://localhost", "http://example.com", "http://8.8.8.8",
		"http://[2001:4860:4860::8888]", "https://user@example.com", "http://localhost/?x=1",
		"https://example.com/#fragment",
	}
	for _, raw := range invalid {
		if got, err := normalizeBase(raw); err == nil {
			t.Fatalf("normalizeBase(%q) unexpectedly accepted %q", raw, got)
		}
	}
}

func TestClientNeverFollowsRedirects(t *testing.T) {
	tests := []struct {
		location string
		code     ipc.Code
	}{
		{location: "/sys/login?next=%2Fsync%2Frpc", code: ipc.CodeForbidden},
		{location: "/not-nexterm", code: ipc.CodeInternal},
	}
	for _, test := range tests {
		t.Run(test.location, func(t *testing.T) {
			var followed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/sync/rpc" {
					followed.Add(1)
					return
				}
				http.Redirect(w, r, test.location, http.StatusFound)
			}))
			t.Cleanup(server.Close)
			instance := newTestInstance(t, false)
			token := "token"
			if _, err := instance.service.LinkSet(context.Background(), LinkPatch{URL: server.URL, Token: &token}); err != nil {
				t.Fatal(err)
			}
			_, err := NewClient(instance.service).RemoteDigest(context.Background())
			requireCode(t, err, test.code)
			if followed.Load() != 0 {
				t.Fatal("redirect was followed")
			}
			link, _ := instance.service.LinkGet(context.Background())
			if link.LastError == "" {
				t.Fatal("failed probe did not persist lastError")
			}
		})
	}
}

func TestClientInsecureTLSOptIn(t *testing.T) {
	instance := newTestInstance(t, false)
	token, _ := instance.service.Token(context.Background())
	server := httptest.NewTLSServer(instance.service.PeerHandler())
	t.Cleanup(server.Close)
	insecure := false
	if _, err := instance.service.LinkSet(context.Background(), LinkPatch{
		URL: server.URL, Token: &token, Insecure: &insecure,
	}); err != nil {
		t.Fatal(err)
	}
	client := NewClient(instance.service)
	_, err := client.RemoteDigest(context.Background())
	requireCode(t, err, ipc.CodeDisconnected)

	insecure = true
	if _, err := instance.service.LinkSet(context.Background(), LinkPatch{URL: server.URL, Insecure: &insecure}); err != nil {
		t.Fatal(err)
	}
	digest, err := client.RemoteDigest(context.Background())
	if err != nil || digest.Protocol != ProtocolVersion {
		t.Fatalf("insecure TLS opt-in digest=%+v err=%v", digest, err)
	}
}

func TestGoToGoHTTPPushPullAndTokenRotation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	source := newTestInstance(t, true)
	target := newTestInstance(t, true)
	targetToken, _ := target.service.Token(ctx)
	server := httptest.NewServer(target.service.PeerHandler())
	t.Cleanup(server.Close)
	if _, err := source.service.LinkSet(ctx, LinkPatch{URL: server.URL, Token: &targetToken}); err != nil {
		t.Fatal(err)
	}
	client := NewClient(source.service)

	groupID, credentialID := ids.New(), ids.New()
	putTestGroup(t, source, groupID, nil, "http")
	putTestCredential(t, source, credentialID, "http secret", "password", "http-secret-中文")
	asset := putTestAsset(t, source, store.AssetRow{
		GroupID: &groupID, CredID: &credentialID, Name: "before push", Host: testPtr("10.9.8.7"),
		Port: testPtr(int32(2222)), Username: testPtr("root"), AuthKind: testPtr("password"),
		OptionsJSON: `{"keepalive":30}`, CreatedAt: 1, UpdatedAt: 2,
	})
	digest, err := client.RemoteDigest(ctx)
	if err != nil || len(digest.Assets) != 0 {
		t.Fatalf("remote digest=%+v err=%v", digest, err)
	}
	verified, _ := source.service.LinkGet(ctx)
	if verified.VerifiedAt == 0 || verified.LastError != "" {
		t.Fatalf("successful probe status not saved: %+v", verified)
	}

	report, err := client.Push(ctx, PushRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil || report.AssetsCreated != 1 || report.CredsCreated != 1 || report.GroupsCreated != 1 {
		t.Fatalf("HTTP push report=%+v err=%v", report, err)
	}
	remote, _ := target.db.AssetGet(ctx, asset.ID)
	if remote.Name != "before push" || remote.GroupID == nil || *remote.GroupID != groupID ||
		remote.CredID == nil || *remote.CredID != credentialID || remote.OptionsJSON != `{"keepalive":30}` {
		t.Fatalf("HTTP push changed fields: %+v", remote)
	}
	remoteCredential, _ := target.db.CredentialGetRow(ctx, credentialID)
	secret, err := target.vault.DecryptCredentialString(ctx, remoteCredential)
	if err != nil || secret != "http-secret-中文" {
		t.Fatalf("HTTP destination secret=%q err=%v", secret, err)
	}
	report, err = client.Push(ctx, PushRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil || report.AssetsUpdated != 1 || report.AssetsCreated != 0 {
		t.Fatalf("idempotent HTTP push report=%+v err=%v", report, err)
	}

	remote.Name = "pulled from target"
	remote.UpdatedAt += 1000
	putTestAsset(t, target, remote)
	report, err = client.Pull(ctx, PullRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil || report.AssetsUpdated != 1 {
		t.Fatalf("HTTP pull report=%+v err=%v", report, err)
	}
	local, _ := source.db.AssetGet(ctx, asset.ID)
	if local.Name != "pulled from target" {
		t.Fatalf("HTTP pull did not apply remote row: %+v", local)
	}

	local.Name = "source-before-delete"
	local.UpdatedAt = 100
	putTestAsset(t, source, local)
	remote.Name = "target-edit-200"
	remote.UpdatedAt = 200
	putTestAsset(t, target, remote)
	if err := source.db.AssetDelete(ctx, asset.ID); err != nil {
		t.Fatal(err)
	}
	deletedSource, _ := source.db.AssetGet(ctx, asset.ID)
	if deletedSource.UpdatedAt != 100 || deletedSource.DeletedAt == nil {
		t.Fatalf("real delete fixture advanced updated_at or lost deleted_at: %+v", deletedSource)
	}
	report, err = client.Push(ctx, PushRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil || report.SkippedNewer != 0 || report.AssetsUpdated != 1 {
		t.Fatalf("HTTP delete-versus-edit push report=%+v err=%v", report, err)
	}
	remote, _ = target.db.AssetGet(ctx, asset.ID)
	if remote.DeletedAt == nil || remote.UpdatedAt != 100 {
		t.Fatalf("HTTP push did not accept newer tombstone: %+v", remote)
	}

	remote.DeletedAt = nil
	remote.Name = "remote-live-200"
	remote.UpdatedAt = 200
	putTestAsset(t, target, remote)
	report, err = client.Pull(ctx, PullRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil || report.SkippedNewer != 1 || report.CredsUpdated != 0 || report.AssetsUpdated != 0 {
		t.Fatalf("HTTP older-live pull report=%+v err=%v", report, err)
	}
	local, _ = source.db.AssetGet(ctx, asset.ID)
	if local.DeletedAt == nil {
		t.Fatal("HTTP pull resurrected a newer local tombstone with an older live row")
	}

	remote.Name = "remote-resurrect-newer"
	remote.UpdatedAt = *local.DeletedAt + 100
	putTestAsset(t, target, remote)
	report, err = client.Pull(ctx, PullRequest{AssetIDs: []string{asset.ID}, WithCredentials: true})
	if err != nil || report.SkippedNewer != 0 || report.AssetsUpdated != 1 {
		t.Fatalf("HTTP newer-live pull report=%+v err=%v", report, err)
	}
	local, _ = source.db.AssetGet(ctx, asset.ID)
	if local.DeletedAt != nil || local.Name != "remote-resurrect-newer" {
		t.Fatalf("HTTP newer live row did not resurrect: %+v", local)
	}

	newToken, _ := target.service.RotateToken(ctx)
	_, err = client.RemoteDigest(ctx)
	requireCode(t, err, ipc.CodeForbidden)
	failed, _ := source.service.LinkGet(ctx)
	if failed.LastError == "" || failed.VerifiedAt == 0 || failed.VerifiedAt != verified.VerifiedAt {
		t.Fatalf("failed probe status=%+v previous verified=%+v", failed, verified.VerifiedAt)
	}
	if _, err := source.service.LinkSet(ctx, LinkPatch{URL: server.URL, Token: &newToken}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RemoteDigest(ctx); err != nil {
		t.Fatalf("rotated token should work after link update: %v", err)
	}
}

func TestEmptyPushDoesNotMakeHTTPRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	instance := newTestInstance(t, false)
	token := "unused"
	if _, err := instance.service.LinkSet(context.Background(), LinkPatch{URL: server.URL, Token: &token}); err != nil {
		t.Fatal(err)
	}
	report, err := NewClient(instance.service).Push(context.Background(), PushRequest{
		AssetIDs: []string{store.BuiltinLocalAssetID}, WithCredentials: true,
	})
	counters := report.GroupsCreated + report.GroupsUpdated + report.AssetsCreated + report.AssetsUpdated +
		report.CredsCreated + report.CredsUpdated + report.SkippedNewer + report.Refused
	if err != nil || len(report.Warnings) != 0 || counters != 0 || requests.Load() != 0 {
		t.Fatalf("empty push report=%+v requests=%d err=%v", report, requests.Load(), err)
	}
}

func TestRedirectLoginDiagnosticIsActionable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/sys/login", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	instance := newTestInstance(t, false)
	token := "token"
	_, _ = instance.service.LinkSet(context.Background(), LinkPatch{URL: server.URL, Token: &token})
	_, err := NewClient(instance.service).RemoteDigest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "登录") {
		t.Fatalf("login redirect error=%v", err)
	}
}
