package production

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	sshdaemon "github.com/Hello-CTF/NexTerm/internal/ssh"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestProductionHelperProgressEventPreservesOperationEvidence(t *testing.T) {
	deadline := time.Date(2026, time.October, 22, 12, 45, 0, 123000000, time.UTC)
	scope := ipc.HelperProgressScope{SessionID: "session-a", AssetID: "asset-a"}
	progress := sshdaemon.HelperProgress{
		OperationID: "helper-operation-a",
		Attempt:     2,
		Attempts:    3,
		Deadline:    deadline,
		Step:        4,
		Steps:       6,
		Stage:       "upload",
		Cause:       "SFTP unavailable; using exec",
		Transport:   "exec",
		Path:        "/remote/helper",
		Transferred: 128,
		Total:       512,
		Percent:     25,
	}

	event := productionHelperProgressEvent(scope, progress)
	if event.SessionID != scope.SessionID || event.AssetID != scope.AssetID {
		t.Fatalf("event scope = %q/%q, want %q/%q", event.SessionID, event.AssetID, scope.SessionID, scope.AssetID)
	}
	if event.OperationID != progress.OperationID || event.Attempt != 2 || event.Attempts != 3 {
		t.Fatalf("operation identity = %q attempt %d/%d", event.OperationID, event.Attempt, event.Attempts)
	}
	if event.Deadline != deadline.UnixMilli() {
		t.Fatalf("deadline = %d, want %d", event.Deadline, deadline.UnixMilli())
	}
	if event.Stage != "upload" || event.Cause != progress.Cause || event.Path != progress.Path || event.Transport != "exec" {
		t.Fatalf("operation evidence = %+v", event)
	}
	if event.Step != 4 || event.Steps != 6 || event.Transferred != 128 || event.Total != 512 || event.Percent != 25 {
		t.Fatalf("legacy progress fields regressed: %+v", event)
	}
}

func TestProductionHelperOperationCancelIPC(t *testing.T) {
	started := make(chan context.Context, 1)
	resolver := session.DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		session.ReportHelperOperation(ctx, "operation-ipc-cancel")
		started <- ctx
		<-ctx.Done()
		return nil, context.Cause(ctx)
	})
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)
	attachedResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "helper-cancel-channel", "client-a")
	tabID := requireAttachedTabID(t, attachedResponse)
	var deploymentCtx context.Context
	select {
	case deploymentCtx = <-started:
	case <-time.After(time.Second):
		t.Fatal("background helper deployment did not start")
	}

	response := dispatchDurableTest(t, production, "terminal_helper_operation", `{"sessionId":"`+connected.ID+`","operationId":"operation-ipc-cancel","action":"cancel"}`, "", "client-a")
	var result helperOperationDTO
	requireStoreTestResponse(t, response, &result)
	if !result.Accepted || result.Action != "cancel" || result.OperationID != "operation-ipc-cancel" || result.Compatibility != "unknown" {
		t.Fatalf("cancel result = %+v", result)
	}
	if result.Local.Known || result.Running.Known {
		t.Fatalf("cancel fabricated version evidence: %+v", result)
	}
	select {
	case <-deploymentCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("IPC did not cancel the real deployment context")
	}
	tab, err := production.Services.Sessions.Tab(tabID)
	if err != nil || tab.Info().Durable {
		t.Fatalf("ordinary tab after IPC cancellation = %+v, %v", tab, err)
	}
	transport, err := production.Services.Sessions.Transport(t.Context(), connected.ID)
	if err != nil || !transport.IsAlive() {
		t.Fatalf("IPC cancellation disconnected SSH: %v, %v", transport, err)
	}
}

func TestHelperDTOFromDaemonReportsEvidenceAndRealUpdateOutcome(t *testing.T) {
	protocol := supervisor.ProtocolVersion
	local := supervisor.RuntimeMetadata{Version: "v1.4.0", Commit: "local-commit", Protocol: protocol, SHA256: "aaaa"}
	running := supervisor.RuntimeMetadata{Version: "v1.3.9", Commit: "running-commit", Protocol: protocol, SHA256: "bbbb"}
	daemon := &sshdaemon.Daemon{
		RunningMetadata: running,
		MetadataKnown:   true,
		Update:          sshdaemon.HelperUpdateStatus{Local: local, Running: running, Updated: true},
	}
	result := helperDTOFromDaemon("session-a", "update", daemon)
	if result.Local.Release != "v1.4.0" || result.Local.Commit != "local-commit" || result.Local.Protocol == nil || *result.Local.Protocol != protocol || result.Local.SHA256 != "aaaa" {
		t.Fatalf("local metadata = %+v", result.Local)
	}
	if result.Running.Release != "v1.3.9" || result.Running.Commit != "running-commit" || result.Running.Protocol == nil || *result.Running.Protocol != protocol || result.Running.SHA256 != "bbbb" {
		t.Fatalf("running metadata = %+v", result.Running)
	}
	if result.Compatibility != "compatible" {
		t.Fatalf("release difference changed protocol compatibility: %q", result.Compatibility)
	}
	if result.Update == nil || result.Update.Status != "updated" {
		t.Fatalf("update outcome = %+v", result.Update)
	}

	daemon.MetadataKnown = false
	daemon.RunningMetadata = supervisor.RuntimeMetadata{}
	daemon.Update = sshdaemon.HelperUpdateStatus{Deferred: true, Reason: "active PTY", RolledBack: true}
	result = helperDTOFromDaemon("session-a", "update", daemon)
	if result.Running.Known || result.Compatibility != "unknown" {
		t.Fatalf("unknown running evidence was fabricated: %+v, %q", result.Running, result.Compatibility)
	}
	if result.Update == nil || result.Update.Status != "deferred" || result.Update.Detail == "" {
		t.Fatalf("deferred/rollback outcome = %+v", result.Update)
	}
}

func writeOfflineHelperFixture(t *testing.T, protocol int, verified bool) (string, sshdaemon.UploadSource) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "offline-helper")
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	initial, err := sshdaemon.InspectExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	cgo := "0"
	if initial.CGO {
		cgo = "1"
	}
	manifest := map[string]any{
		"schema_version":           1,
		"kind":                     "go-binary",
		"version":                  initial.Version,
		"commit":                   initial.Commit,
		"goarm":                    initial.GOARM,
		"sha256":                   initial.Digest,
		"binary_metadata_verified": verified,
		"platform":                 map[string]any{"os": initial.GOOS, "arch": helperManifestArchitecture(initial), "cgo": cgo, "goarm": initial.GOARM},
		"artifact":                 map[string]any{"size_bytes": len(content), "sha256": initial.Digest},
	}
	if verified {
		manifest["protocol"] = protocol
		manifest["metadata_host"] = map[string]any{"os": initial.GOOS, "arch": helperManifestArchitecture(initial)}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".manifest.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := sshdaemon.InspectExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, source
}

func TestHelperOfflineTrustRequiresActualSHAAndVerifiedProtocol(t *testing.T) {
	for _, test := range []struct {
		name          string
		protocol      int
		verified      bool
		compatibility string
	}{
		{name: "compatible", protocol: supervisor.ProtocolVersion, verified: true, compatibility: "compatible"},
		{name: "incompatible", protocol: supervisor.ProtocolVersion + 1, verified: true, compatibility: "incompatible"},
		{name: "unknown", protocol: supervisor.ProtocolVersion, verified: false, compatibility: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			production, _ := newRemoteRecoveryHarness(t, &recordingDurableResolver{err: errors.New("no SSH resolution expected")})
			connected := connectRemoteSSHAsset(t, production)
			connectedSession, err := production.Services.Sessions.Session(connected.ID)
			if err != nil {
				t.Fatal(err)
			}
			assetID := connectedSession.Asset().ID
			service := &productionHelperService{sessions: production.Services.Sessions}
			path, source := writeOfflineHelperFixture(t, test.protocol, test.verified)

			inspected, err := service.inspectOffline(t.Context(), connected.ID, path, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if inspected.Offline == nil || inspected.Offline.SHA256 != source.Digest || inspected.Offline.Compatibility != test.compatibility || inspected.Offline.Trusted {
				t.Fatalf("offline inspection = %+v", inspected.Offline)
			}
			resolver := &sshdaemon.Resolver{}
			service.configureResolver(resolver, assetID)
			if resolver.HelperExecutable != "" {
				t.Fatal("inspection trusted the raw helper without confirmation")
			}
			if _, err := service.inspectOffline(t.Context(), connected.ID, path, "incorrect-sha", true); err == nil {
				t.Fatal("incorrect SHA was trusted")
			}

			confirmed, err := service.inspectOffline(t.Context(), connected.ID, path, source.Digest, true)
			if test.compatibility == "compatible" {
				if err != nil {
					t.Fatalf("compatible offline helper was rejected: %v", err)
				}
				if !confirmed.Accepted || confirmed.Offline == nil || !confirmed.Offline.Trusted {
					t.Fatalf("offline confirmation = %+v", confirmed)
				}
				service.configureResolver(resolver, assetID)
				if resolver.HelperExecutable != path {
					t.Fatalf("resolver helper = %q, want confirmed path %q", resolver.HelperExecutable, path)
				}
			} else {
				if err == nil {
					t.Fatal("unknown or incompatible protocol was trusted")
				}
				service.configureResolver(resolver, assetID)
				if resolver.HelperExecutable != "" {
					t.Fatal("rejected offline helper changed resolver configuration")
				}
			}
		})
	}
}
