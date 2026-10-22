package production

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/takeover"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// guardProbeScript serves one send_keys tool call carrying a
// confirmation-worthy write, then completes on the next model turn.
type guardProbeScript struct {
	keys string
}

func (s *guardProbeScript) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
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
	if !spawnHasRole(&parsed, "tool") {
		spawnWriteToolCall(writer, "call-keys-1", "send_keys", `{"keys":`+strconv.Quote(s.keys)+`,"enter":true}`)
		return
	}
	spawnWriteToolCall(writer, "call-done-1", "done", `{"summary":"finished","success":true}`)
}

// The production composition applies the guard mode bound to the tab's real
// asset: global silent never auto-allows an asset bound to read_only, global
// read_write never lets an asset bound to unattended skip the gate, a mode
// bound to another asset changes nothing, and a denied write never reaches
// the terminal.
func TestProductionTakeoverAppliesRealAssetGuardMode(t *testing.T) {
	const probe = "touch /tmp/nexterm-guard-probe"
	for _, test := range []struct {
		name        string
		global      guard.Mode
		boundAsset  string
		boundMode   guard.Mode
		wantConfirm bool
	}{
		{"global silent yields to asset read_only", guard.Silent, "asset-bound", guard.ReadOnly, false},
		{"global read_write yields to asset unattended", guard.ReadWrite, "asset-bound", guard.Unattended, false},
		{"mode bound to another asset leaves the tab on the global mode", guard.ReadWrite, "asset-other", guard.ReadOnly, true},
		{"unbound asset keeps the global mode", guard.ReadWrite, "", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "data.db")
			provider := httptest.NewServer(&guardProbeScript{keys: probe})
			t.Cleanup(provider.Close)
			database, err := store.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "server"})
			if err != nil {
				t.Fatal(err)
			}
			bound := asset.ID
			if test.boundAsset == "asset-other" {
				other, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "other"})
				if err != nil {
					t.Fatal(err)
				}
				bound = other.ID
			}
			services, transport := composeTakeoverRuntime(t, database, provider.URL, asset.ID, false)
			if err := services.Guard.Set(ctx, guard.Config{Mode: test.global}); err != nil {
				t.Fatal(err)
			}
			if test.boundAsset != "" {
				if err := services.Guard.SetDeviceMode(ctx, bound, test.boundMode); err != nil {
					t.Fatal(err)
				}
			}

			stream := &agent.SliceStream{}
			token := enterTakeoverWithPrompt(t, services, transport)
			response, err := services.Takeover.Run(ctx, takeover.RunArgs{TabID: "tab-takeover", Token: token, Instruction: "probe the guard"}, agent.StaticStream(stream))
			if err != nil {
				t.Fatal(err)
			}
			if test.wantConfirm {
				confirmation := waitOutcomeEvent(t, stream, "confirmRequired")
				if err := services.Takeover.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "deny"}); err != nil {
					t.Fatal(err)
				}
			} else {
				event := waitOutcomeEvent(t, stream, "toolResult")
				if event.OK || !strings.Contains(event.Text, "权限策略已拒绝") {
					t.Fatalf("denied write surfaced as %+v", event)
				}
			}
			waitOutcomeClosed(t, stream)
			if written := string(transport.written()); strings.Contains(written, probe) {
				t.Fatalf("denied write reached the terminal: %q", written)
			}
		})
	}
}

// The composed runner resolves HITL asset evidence and the production
// challenge from the real session-backed asset, never from client claims.
func TestProductionRunnerResolvesRealAssetEvidenceAndChallenge(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "prod", Tags: "env:production"})
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(&execEvidenceScript{assetID: asset.ID})
	t.Cleanup(provider.Close)
	services, _ := composeTakeoverRuntime(t, database, provider.URL, asset.ID, false)

	conversation, err := services.Store.ConvCreate(ctx, "prod", map[string]any{"scope": tools.Scope{TabID: "tab-takeover"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := services.Store.RunInsert(ctx, store.RunRow{ID: "run-evidence", ConversationID: conversation.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	resolved, err := aiRunAssetResolver(services)(ctx, "run-evidence", "exec_commands")
	if err != nil || resolved != asset.ID {
		t.Fatalf("asset evidence = %q err=%v, want the real session asset %s", resolved, err, asset.ID)
	}
	names, err := aiRequiredEnvironmentNames(services)(ctx, asset.ID)
	if err != nil || len(names) != 1 || names[0] != "production" {
		t.Fatalf("production challenge = %v err=%v", names, err)
	}

	tab, err := services.Sessions.TabFor(ctx, "tab-takeover")
	if err != nil {
		t.Fatal(err)
	}
	stream := &agent.SliceStream{}
	response, err := services.Agent.Start(ctx, agent.ChatArgs{Message: "run it", Scope: tools.Scope{SessionID: tab.SessionID}}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitOutcomeEvent(t, stream, "confirmRequired")
	if len(confirmation.RequiredEnvironmentNames) != 1 || confirmation.RequiredEnvironmentNames[0] != "production" {
		t.Fatalf("confirmRequired names = %v", confirmation.RequiredEnvironmentNames)
	}
	snapshot, err := services.Agent.HITLSnapshot(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pending) != 1 || snapshot.Pending[0].AssetID != asset.ID {
		t.Fatalf("pending interrupt evidence = %+v, want the real asset %s", snapshot.Pending, asset.ID)
	}
	if err := services.Agent.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow", EnvironmentNames: []string{"production"}}); err != nil {
		t.Fatal(err)
	}
	events := waitOutcomeClosed(t, stream)
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
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
}

type execEvidenceScript struct {
	assetID string
}

func (s *execEvidenceScript) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
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
	if !spawnHasRole(&parsed, "tool") {
		spawnWriteToolCall(writer, "call-exec-1", "exec_commands", `{"commands":["echo one"],"asset_ids":["`+s.assetID+`"]}`)
		return
	}
	spawnWriteContent(writer, "done")
}

// The composed runtime releases the store before the test's TempDir cleanup
// runs: the helper owns every resource it composes, so by the time cleanup
// order reaches the TempDir removal point the data.db handle is closed
// (Ping fails) and the file is removable — Windows refuses to unlink open
// files, which is what this coverage pins down.
func TestComposeTakeoverRuntimeReleasesStoreBeforeTempDirCleanup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// Registered before the helper: LIFO cleanup order runs this assertion
	// after the helper's own cleanup, at the same point TempDir removal runs.
	t.Cleanup(func() {
		sqlDB, err := database.GORM().DB()
		if err != nil {
			t.Fatalf("sql handle: %v", err)
		}
		if err := sqlDB.PingContext(context.Background()); err == nil {
			t.Fatal("store still open after the composed runtime cleanup")
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("data.db still held after the composed runtime cleanup: %v", err)
		}
	})
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "server"})
	if err != nil {
		t.Fatal(err)
	}
	composeTakeoverRuntime(t, database, "http://127.0.0.1:1", asset.ID, false)
}
