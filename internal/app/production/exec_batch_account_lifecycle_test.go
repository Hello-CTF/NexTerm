package production

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

func TestExecBatchProductionAccountCleanupSSH(t *testing.T) {
	fixture := newExecBatchSSHFixture(t)
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(ctx context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return transportssh.Connect(ctx, transportssh.Config{
			Host: "127.0.0.1", Port: fixture.port(), User: "test",
			Auth:     transportssh.AuthConfig{Method: transportssh.AuthPassword, Password: "secret"},
			HostKeys: transportssh.NewMemoryHostKeyStore(), AutoAcceptUnknown: true,
		})
	})})
	t.Cleanup(func() { _ = manager.Close() })
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	application, err := NewProductionWithServices(Config{}, ProductionServices{Sessions: manager, Store: storage})
	if err != nil {
		t.Fatal(err)
	}
	if application.Services.ExecBatches == nil {
		t.Fatal("production did not retain the shared exec batch service")
	}
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	connect := func(ctx context.Context, assetID string) string {
		t.Helper()
		asset, err := storage.AssetCreate(ctx, store.AssetInput{Kind: session.KindSSH, Name: assetID})
		if err != nil {
			t.Fatal(err)
		}
		connected, err := manager.ConnectFor(ctx, session.Asset{ID: asset.ID, Name: assetID, Kind: session.KindSSH})
		if err != nil {
			t.Fatal(err)
		}
		return connected.ID
	}
	aFirst := connect(ownerA, "asset-a-first")
	aQueued := connect(ownerA, "asset-a-queued")
	bTarget := connect(ownerB, "asset-b")

	executeDoneA := make(chan ipc.Response, 1)
	executeDoneB := make(chan ipc.Response, 1)
	go func() {
		executeDoneA <- dispatchExecBatchTest(application.Dispatcher, ownerA, "exec_batch_execute", execbatch.Request{
			BatchID: "account-a-logout", SessionIDs: []string{aFirst, aQueued}, Command: "hold-exit", MaxConcurrent: 1,
		})
	}()
	go func() {
		executeDoneB <- dispatchExecBatchTest(application.Dispatcher, ownerB, "exec_batch_execute", execbatch.Request{
			BatchID: "account-b-still-running", SessionIDs: []string{bTarget}, Command: "hold-exit",
		})
	}()
	for range 2 {
		select {
		case command := <-fixture.requests:
			if command != "hold-exit" {
				t.Fatalf("SSH command = %q", command)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("long-running commands did not reach SSH")
		}
	}

	application.CleanupAccount(context.Background(), "owner-a", "session-a")
	select {
	case response := <-executeDoneA:
		batch := decodeExecBatchTest[execbatch.Batch](t, response)
		if len(batch.Results) != 2 {
			t.Fatalf("owner A results = %+v", batch.Results)
		}
		for _, result := range batch.Results {
			if result.Status != execbatch.StatusCanceled {
				t.Fatalf("owner A result after account cleanup = %+v", result)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner A execute RPC did not stop after account cleanup")
	}
	if got := fixture.execCount.Load(); got != 2 {
		t.Fatalf("SSH exec count = %d, want only owner A's first target and owner B", got)
	}

	fixture.signalExit()
	select {
	case response := <-executeDoneB:
		batch := decodeExecBatchTest[execbatch.Batch](t, response)
		if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusSuccess {
			t.Fatalf("owner B was affected by owner A cleanup: %+v", batch)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner B did not finish normally after the remote exit")
	}
}

func TestExecBatchProductionAccountCleanupWithoutServiceIsNoop(t *testing.T) {
	var application *Production
	application.CleanupAccount(context.Background(), "owner-a", "session-a")
	(&Production{}).CleanupAccount(context.Background(), "owner-a", "session-a")
}
