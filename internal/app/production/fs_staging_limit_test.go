package production

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func newStagedFSTransferTestDispatcher(t *testing.T, staged *fakeStagedBlobResolver) (*ipc.Dispatcher, context.Context, string) {
	t.Helper()
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return fakeProviderTransport{kind: "ssh", fs: fslocal.New()}, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerFSCommands(dispatcher, manager, staged); err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-transfer-staged-limit", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher, ctx, connected.ID
}

func TestFSTransferIPCStagedDownloadHonorsWriteBudget(t *testing.T) {
	staged := &fakeStagedBlobResolver{dir: t.TempDir(), writeLimit: 4}
	dispatcher, ctx, sessionID := newStagedFSTransferTestDispatcher(t, staged)
	remote := filepath.Join(t.TempDir(), "remote.bin")
	if err := os.WriteFile(remote, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	reserved := filepath.Join(staged.dir, "download.bin")
	if err := os.WriteFile(reserved, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	failure := dispatchFSTransferTest(dispatcher, ctx, "fs_download", fsRequest{
		SessionID: sessionID, LocalPath: "staged-id/download.bin", RemotePath: remote, TaskID: "too-large", AttemptID: "too-large",
	}, nil)
	if failure.OK || failure.Error == nil || failure.Error.Code != ipc.CodeIO {
		t.Fatalf("oversize staged download = %+v, want io error", failure)
	}
	detail, ok := failure.Error.Detail.(map[string]any)
	if !ok || detail["resource"] != "local_staging" || detail["reason"] != "too_large" {
		t.Fatalf("oversize detail = %#v, want local_staging/too_large", failure.Error.Detail)
	}
	if info, err := os.Stat(reserved); err != nil || info.Size() != 0 {
		t.Fatalf("oversize download wrote into staging: %v, %v", info, err)
	}
	if held := staged.reserved.Load(); held != 0 {
		t.Fatalf("budgets held after the refused download = %d, want 0", held)
	}

	staged.writeLimit = 6
	response := dispatchFSTransferTest(dispatcher, ctx, "fs_download", fsRequest{
		SessionID: sessionID, LocalPath: "staged-id/download.bin", RemotePath: remote, TaskID: "fits", AttemptID: "fits",
	}, nil)
	if got := decodeFSTransferTest[int64](t, response); got != 6 {
		t.Fatalf("download within budget = %d, want 6", got)
	}
	if held := staged.reserved.Load(); held != 0 {
		t.Fatalf("budgets held after the download = %d, want 0", held)
	}
}

func TestFSTransferIPCStagedDownloadRefusedWhenQuotaExhausted(t *testing.T) {
	staged := &fakeStagedBlobResolver{dir: t.TempDir(), writeErr: errors.New("暂存空间已达上限，请等待已有传输完成或稍后重试")}
	dispatcher, ctx, sessionID := newStagedFSTransferTestDispatcher(t, staged)
	remote := filepath.Join(t.TempDir(), "remote.bin")
	if err := os.WriteFile(remote, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	failure := dispatchFSTransferTest(dispatcher, ctx, "fs_download", fsRequest{
		SessionID: sessionID, LocalPath: "staged-id/download.bin", RemotePath: remote, TaskID: "quota", AttemptID: "quota",
	}, nil)
	if failure.OK || failure.Error == nil || failure.Error.Code != ipc.CodeIO || failure.Error.Message != staged.writeErr.Error() {
		t.Fatalf("quota-exhausted download = %+v, want the quota message", failure)
	}
	detail, ok := failure.Error.Detail.(map[string]any)
	if !ok || detail["resource"] != "local_staging" || detail["reason"] != "staging_quota" {
		t.Fatalf("quota detail = %#v, want local_staging/staging_quota", failure.Error.Detail)
	}
	if _, err := os.Stat(filepath.Join(staged.dir, "download.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused download created the target: %v", err)
	}
}

func TestResolveStagedWriteTargetIsUnlimitedOnDesktop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chosen.bin")
	resolved, limit, release, err := resolveStagedWriteTarget(t.Context(), nil, path)
	if err != nil || resolved != path || limit != 0 || release == nil {
		t.Fatalf("desktop write target = %q, %d, %v; want the path unchanged, no limit and a no-op release", resolved, limit, err)
	}
	release()
}

func TestExportLogMaxBytesClampsToBudget(t *testing.T) {
	for _, tc := range []struct {
		requested int
		limit     int64
		want      int
	}{
		{requested: 0, limit: 0, want: 0},
		{requested: 100, limit: 0, want: 100},
		{requested: 0, limit: 64, want: 64},
		{requested: 100, limit: 64, want: 64},
		{requested: 10, limit: 64, want: 10},
	} {
		if got := exportLogMaxBytes(tc.requested, tc.limit); got != tc.want {
			t.Fatalf("exportLogMaxBytes(%d, %d) = %d, want %d", tc.requested, tc.limit, got, tc.want)
		}
	}
}
