package production

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const crossAssetProductionCommand = "touch /tmp/nexterm-cross-asset-confirmation"

type crossAssetConfirmationScript struct {
	assetID  string
	sendKeys bool
}

func (s *crossAssetConfirmationScript) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
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
	writer.Header().Set("Content-Type", "text/event-stream")
	var results []tools.Output
	for _, message := range parsed.Messages {
		if message.Role == "tool" {
			var result tools.Output
			if err := json.Unmarshal([]byte(spawnMessageText(message.Content)), &result); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			results = append(results, result)
		}
	}
	if len(results) == 0 {
		input := tools.TerminalCreateArgs{AssetID: s.assetID}
		if !s.sendKeys {
			input.Command = crossAssetProductionCommand
		}
		args, _ := json.Marshal(input)
		spawnWriteToolCall(writer, "create-prod", "terminal_create", string(args))
		return
	}
	if s.sendKeys && len(results) == 1 && results[0].Terminal != nil {
		args, _ := json.Marshal(tools.SendKeysArgs{TabID: results[0].Terminal.Handle, Keys: crossAssetProductionCommand, Enter: true})
		spawnWriteToolCall(writer, "send-prod", "send_keys", string(args))
		return
	}
	spawnWriteContent(writer, "done")
}

func TestProductionCrossAssetConfirmationBindsActualTarget(t *testing.T) {
	for _, sendKeys := range []bool{false, true} {
		name := "startup command"
		if sendKeys {
			name = "send keys to created tab handle"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			database, err := store.OpenInMemory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			staging, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "staging", Tags: "env:staging"})
			if err != nil {
				t.Fatal(err)
			}
			production, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "production", Tags: "env:production"})
			if err != nil {
				t.Fatal(err)
			}
			provider := httptest.NewServer(&crossAssetConfirmationScript{assetID: production.ID, sendKeys: sendKeys})
			t.Cleanup(provider.Close)
			services, transport := composeTakeoverRuntime(t, database, provider.URL, staging.ID, false)
			if _, err := services.Sessions.Connect(ctx, session.Asset{ID: production.ID, Kind: session.KindSSH}); err != nil {
				t.Fatal(err)
			}
			tab, err := services.Sessions.TabFor(ctx, "tab-takeover")
			if err != nil {
				t.Fatal(err)
			}
			stream := &agent.SliceStream{}
			response, err := services.Agent.Start(ctx, agent.ChatArgs{Message: "operate on the target", Scope: tools.Scope{SessionID: tab.SessionID, TabID: tab.ID}}, agent.StaticStream(stream))
			if err != nil {
				t.Fatal(err)
			}
			approve := func(callID string) {
				t.Helper()
				var confirmation agent.Event
				deadline := time.Now().Add(5 * time.Second)
				for confirmation.ID == "" {
					events, closed := stream.Snapshot()
					for _, event := range events {
						if event.Type == "confirmRequired" && event.ID == callID {
							confirmation = event
							break
						}
					}
					if confirmation.ID == "" {
						if closed || time.Now().After(deadline) {
							t.Fatalf("missing confirmation %s: %+v", callID, events)
						}
						time.Sleep(10 * time.Millisecond)
					}
				}
				snapshot, err := services.Agent.HITLSnapshot(response.JobID)
				if err != nil {
					t.Fatal(err)
				}
				if len(snapshot.Pending) != 1 || snapshot.Pending[0].AssetID != production.ID {
					t.Fatalf("pending target = %+v, want production asset %s", snapshot.Pending, production.ID)
				}
				if len(confirmation.RequiredEnvironmentNames) != 1 || confirmation.RequiredEnvironmentNames[0] != "production" {
					t.Fatalf("production challenge = %v", confirmation.RequiredEnvironmentNames)
				}
				answer := agent.Confirmation{JobID: response.JobID, CallID: callID, Nonce: confirmation.Nonce, Decision: "allow"}
				if err := services.Agent.Confirm(answer); !errors.Is(err, hitl.ErrEnvironmentNamesMismatch) {
					t.Fatalf("bare cross-asset approval = %v", err)
				}
				if strings.Contains(string(transport.written()), crossAssetProductionCommand) {
					t.Fatal("production command reached the terminal before the challenge was answered")
				}
				answer.EnvironmentNames = []string{"production"}
				if err := services.Agent.Confirm(answer); err != nil {
					t.Fatal(err)
				}
			}
			approve("create-prod")
			if sendKeys {
				approve("send-prod")
			}
			waitOutcomeClosed(t, stream)
			if !strings.Contains(string(transport.written()), crossAssetProductionCommand) {
				t.Fatal("approved production command did not reach the actual terminal")
			}
		})
	}
}
