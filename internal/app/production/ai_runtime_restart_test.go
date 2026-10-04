package production

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type restartScriptServer struct {
	mu       sync.Mutex
	requests int
}

func (s *restartScriptServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	var parsed spawnScriptRequest
	if err := json.Unmarshal(body, &parsed); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests++
	s.mu.Unlock()

	writer.Header().Set("Content-Type", "text/event-stream")
	if spawnHasRole(&parsed, "tool") {
		spawnWriteContent(writer, "done")
		return
	}
	spawnWriteToolCall(writer, "call-ask-1", "ask_user", `{"question":"继续？","options":["继续","停止"]}`)
}

func composeRestartRuntime(t *testing.T, database *store.Store, providerURL string) (*ProductionServices, string) {
	t.Helper()
	ctx := context.Background()
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profileManager.Save(ctx, profiles.Profile{
		BaseURL: providerURL, APIKey: "test-key", Model: "test-model",
		Temperature: 0, ContextWindow: 32768, Stream: true,
	}); err != nil {
		t.Fatal(err)
	}
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "server"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return &outcomeFakeTransport{}, nil
		}),
	})
	connected, err := sessions.Connect(ctx, session.Asset{ID: asset.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions}
	if err := composeAIRuntime(ctx, services, "test-client"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		services.closeAIRuntime()
		_ = sessions.Close()
	})
	return services, connected.ID
}

func waitRestartRunStatus(t *testing.T, database *store.Store, jobID, status string) store.RunRow {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		row, err := database.RunGet(context.Background(), jobID)
		if err == nil && row.Status == status {
			return row
		}
		time.Sleep(10 * time.Millisecond)
	}
	row, _ := database.RunGet(context.Background(), jobID)
	t.Fatalf("run %s did not reach status %q: %+v", jobID, status, row)
	return store.RunRow{}
}

func TestProductionRestartRecoversAndResumesPendingQuestion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	script := &restartScriptServer{}
	providerServer := httptest.NewServer(script)
	t.Cleanup(providerServer.Close)

	firstStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	first, sessionID := composeRestartRuntime(t, firstStore, providerServer.URL)
	stream := &agent.SliceStream{}
	response, err := first.Agent.Start(ctx, agent.ChatArgs{Message: "go", Scope: tools.Scope{SessionID: sessionID}}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	question := waitOutcomeEvent(t, stream, "questionRequired")
	if question.Nonce == "" || question.RequestID == "" {
		t.Fatalf("questionRequired missing binding: %+v", question)
	}
	first.closeAIRuntime()
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	second, _ := composeRestartRuntime(t, secondStore, providerServer.URL)

	row, err := secondStore.RunGet(ctx, response.JobID)
	if err != nil || row.Status != store.RunStatusInterrupted || row.FinishedAt != nil {
		t.Fatalf("recovered row = %+v err=%v", row, err)
	}
	snapshot, err := second.Agent.HITLSnapshot(response.JobID)
	if err != nil || snapshot.Terminal != nil || len(snapshot.Pending) != 1 {
		t.Fatalf("recovered snapshot = %+v err=%v", snapshot, err)
	}

	resumed := &agent.SliceStream{}
	answer := agent.Answer{JobID: response.JobID, CallID: question.ID, Nonce: question.Nonce, Text: "继续"}
	if err := second.Agent.AnswerStream(ctx, answer, agent.StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	events := waitOutcomeClosed(t, resumed)
	done, failed := 0, 0
	for _, event := range events {
		switch event.Type {
		case "done":
			done++
		case "error":
			failed++
		}
	}
	if done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
	completed := waitRestartRunStatus(t, secondStore, response.JobID, store.RunStatusCompleted)
	if completed.Answer != "done" {
		t.Fatalf("completed row = %+v", completed)
	}

	journal, err := secondStore.RunEventsAfter(ctx, response.JobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	questionSeq, doneSeq := uint64(0), uint64(0)
	for index, event := range journal {
		if event.Seq != uint64(index+1) {
			t.Fatalf("journal event %d has seq %d", index, event.Seq)
		}
		switch event.Type {
		case "questionRequired":
			questionSeq = event.Seq
		case "done":
			doneSeq = event.Seq
		}
	}
	if questionSeq == 0 || doneSeq == 0 || doneSeq <= questionSeq {
		t.Fatalf("journal does not span the restart: question=%d done=%d", questionSeq, doneSeq)
	}
	if !strings.Contains(journal[questionSeq-1].PayloadJSON, question.Nonce) {
		t.Fatalf("question payload missing nonce: %+v", journal[questionSeq-1])
	}
}
