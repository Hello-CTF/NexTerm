package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestRunGetReturnsOnlyTheOwnersExactRun(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	owner := ipc.WithUserID(context.Background(), "owner-a")
	conversation, err := database.ConvCreate(owner, "chat", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunInsert(owner, store.RunRow{
		ID: "job-a", ConversationID: conversation.ID, Status: store.RunStatusCompleted,
		Answer: "finished", Turns: 2, TokensIn: 3, TokensOut: 4,
	}); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{runs: database}
	run, err := runner.RunGet(owner, "job-a")
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != "job-a" || run.ConversationID != conversation.ID || run.Status != store.RunStatusCompleted || run.Answer != "finished" {
		t.Fatalf("unexpected run: %+v", run)
	}
	if _, err := runner.RunGet(ipc.WithUserID(context.Background(), "owner-b"), "job-a"); err == nil {
		t.Fatal("another account read the run")
	}
	dispatcher := ipc.NewDispatcher()
	if err := runner.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	request := ipc.Request{Command: "ai_run_get", Args: json.RawMessage(`{"jobId":"job-a"}`)}
	response := dispatcher.Dispatch(owner, request, ipc.Environment{})
	if !response.OK {
		t.Fatalf("owner IPC query failed: %+v", response.Error)
	}
	var ipcRun RunDTO
	if err := json.Unmarshal(response.Data, &ipcRun); err != nil {
		t.Fatal(err)
	}
	if ipcRun.ID != run.ID || ipcRun.Answer != run.Answer {
		t.Fatalf("owner IPC run = %+v, want %+v", ipcRun, run)
	}
	if response := dispatcher.Dispatch(ipc.WithUserID(context.Background(), "owner-b"), request, ipc.Environment{}); response.OK {
		t.Fatal("another account read the run through IPC")
	}
}
