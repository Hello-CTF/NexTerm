package production

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type workbenchFixture struct {
	ctx        context.Context
	database   *store.Store
	dispatcher *ipc.Dispatcher
	web        string
	db         string
	local      string
	running    string
	foreign    string
	webAsset   string
}

// newWorkbenchFixture seeds owner-a's conversations: a finished one on web-01 with a tool call, a file change and an
// approved confirmation, a failed one on db-01, one without a host, and one still running on web-01; owner-b has one
// conversation of its own on web-01.
func newWorkbenchFixture(t *testing.T) workbenchFixture {
	t.Helper()
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	database.SetAIHistoryShared(false)
	asset, err := database.AssetCreate(ctx, store.AssetInput{Name: "web-01", Kind: "ssh"})
	if err != nil {
		t.Fatal(err)
	}
	webAsset := asset.ID
	create := func(ctx context.Context, title string, scope map[string]any) string {
		row, err := database.ConvCreate(ctx, title, map[string]any{"scope": scope})
		if err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	insert := func(ctx context.Context, conversationID, role string, content map[string]any) {
		if err := database.MsgInsert(ctx, conversationID, role, content, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	run := func(ctx context.Context, id, conversationID, status string) {
		row := store.RunRow{ID: id, ConversationID: conversationID, Status: status, Source: "chat"}
		if status != store.RunStatusRunning {
			finished := int64(1)
			row.FinishedAt = &finished
		}
		if err := database.RunInsert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	event := func(runID, eventType string, payload map[string]any) {
		if _, err := database.RunAppendEvent(ctx, runID, eventType, func(seq uint64) ([]byte, error) {
			payload["seq"] = seq
			return json.Marshal(payload)
		}); err != nil {
			t.Fatal(err)
		}
	}
	f := workbenchFixture{ctx: ctx, database: database, webAsset: webAsset}
	f.web = create(ctx, "Restart nginx", map[string]any{"assetId": webAsset, "sessionId": "s1"})
	insert(ctx, f.web, "user", map[string]any{"role": "user", "content": "please restart nginx", "jobId": "run-web"})
	insert(ctx, f.web, "toolCall", map[string]any{"type": "toolCall", "id": "call-1", "name": "exec_commands", "args": map[string]any{"commands": []string{"systemctl restart nginx"}}})
	insert(ctx, f.web, "fileChange", map[string]any{"type": "fileChange", "id": "call-2", "path": "/etc/nginx/nginx.conf", "before": "a\nworker 1\nc\n", "after": "a\nworker 4\nc\n"})
	insert(ctx, f.web, "toolResult", map[string]any{"type": "toolResult", "id": "call-1", "tool": "exec_commands", "ok": true, "summary": "restarted", "exitCode": 0})
	insert(ctx, f.web, "skillCatalog", map[string]any{"type": "skillCatalog", "content": "SECRET CATALOG"})
	insert(ctx, f.web, "assistant", map[string]any{"role": "assistant", "content": "nginx is running again"})
	run(ctx, "run-web", f.web, store.RunStatusCompleted)
	event("run-web", "confirmRequired", map[string]any{"id": "call-1", "tool": "exec_commands", "rendered": "systemctl restart nginx", "risk": "write"})
	event("run-web", "delta", map[string]any{"text": "x"})
	event("run-web", "interactionResolved", map[string]any{"callId": "call-1", "decision": "allow"})

	f.db = create(ctx, "Check replication", map[string]any{"assetId": "01JWORKBENCHASSET000000DB1"})
	insert(ctx, f.db, "user", map[string]any{"role": "user", "content": "check replication lag"})
	run(ctx, "run-db", f.db, store.RunStatusFailed)

	f.local = create(ctx, "Local question", map[string]any{"sessionId": "s2"})
	insert(ctx, f.local, "user", map[string]any{"role": "user", "content": "what is my shell"})
	run(ctx, "run-local", f.local, store.RunStatusCompleted)

	f.running = create(ctx, "Upgrade packages", map[string]any{"assetId": webAsset})
	insert(ctx, f.running, "user", map[string]any{"role": "user", "content": "apt upgrade"})
	run(ctx, "run-running", f.running, store.RunStatusRunning)

	other := ipc.WithUserID(context.Background(), "owner-b")
	f.foreign = create(other, "Foreign nginx", map[string]any{"assetId": webAsset})
	insert(other, f.foreign, "user", map[string]any{"role": "user", "content": "restart nginx"})

	f.dispatcher = ipc.NewDispatcher()
	if err := registerAIWorkbenchCommands(f.dispatcher, database, nil); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f workbenchFixture) call(t *testing.T, ctx context.Context, command string, request any, out any) ipc.Response {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response := f.dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: raw}, ipc.Environment{})
	if response.OK && out != nil {
		if err := json.Unmarshal(response.Data, out); err != nil {
			t.Fatal(err)
		}
	}
	return response
}

func summaryIDs(items []aiConversationSummaryDTO) []string {
	ids := make([]string, len(items))
	for index, item := range items {
		ids[index] = item.ID
	}
	slices.Sort(ids)
	return ids
}

func sortedIDs(ids ...string) []string {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	return ids
}

func TestAIConversationOverviewFiltersAndOwnership(t *testing.T) {
	f := newWorkbenchFixture(t)
	var all aiConversationOverviewResult
	if response := f.call(t, f.ctx, "ai_conversation_overview", map[string]any{}, &all); !response.OK {
		t.Fatalf("overview = %+v", response.Error)
	}
	if all.Total != 4 || !slices.Equal(summaryIDs(all.Items), sortedIDs(f.web, f.db, f.local, f.running)) {
		t.Fatalf("overview = %+v, want owner-a's four conversations only", all)
	}
	hosts := map[string]int64{}
	for _, host := range all.Hosts {
		hosts[host.AssetID] = host.Count
	}
	if hosts[f.webAsset] != 2 || hosts[""] != 1 || len(hosts) != 3 {
		t.Fatalf("host counts = %+v", all.Hosts)
	}

	for _, test := range []struct {
		name    string
		request map[string]any
		want    []string
	}{
		{name: "host", request: map[string]any{"assetId": f.webAsset}, want: []string{f.web, f.running}},
		{name: "unlinked", request: map[string]any{"unlinked": true}, want: []string{f.local}},
		{name: "running", request: map[string]any{"status": "running"}, want: []string{f.running}},
		{name: "failed", request: map[string]any{"status": "failed"}, want: []string{f.db}},
		{name: "completed on host", request: map[string]any{"status": "completed", "assetId": f.webAsset}, want: []string{f.web}},
		{name: "text", request: map[string]any{"query": "NGINX"}, want: []string{f.web}},
		{name: "message text", request: map[string]any{"query": "replication lag"}, want: []string{f.db}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var result aiConversationOverviewResult
			if response := f.call(t, f.ctx, "ai_conversation_overview", test.request, &result); !response.OK {
				t.Fatalf("overview = %+v", response.Error)
			}
			if !slices.Equal(summaryIDs(result.Items), sortedIDs(test.want...)) || result.Total != len(test.want) {
				t.Fatalf("overview = %+v, want %v", result.Items, test.want)
			}
		})
	}

	var page aiConversationOverviewResult
	f.call(t, f.ctx, "ai_conversation_overview", map[string]any{"limit": 3, "offset": 3}, &page)
	if page.Total != 4 || len(page.Items) != 1 {
		t.Fatalf("second page = %+v", page)
	}
	if response := f.call(t, f.ctx, "ai_conversation_overview", map[string]any{"status": "sleeping"}, nil); response.OK {
		t.Fatal("an unknown status must be refused")
	}

	var foreign aiConversationOverviewResult
	f.call(t, ipc.WithUserID(context.Background(), "owner-b"), "ai_conversation_overview", map[string]any{"assetId": f.webAsset}, &foreign)
	if !slices.Equal(summaryIDs(foreign.Items), []string{f.foreign}) {
		t.Fatalf("owner-b overview = %+v", foreign.Items)
	}
}

func TestAIConversationApprovals(t *testing.T) {
	f := newWorkbenchFixture(t)
	var approvals []aiConversationApprovalDTO
	if response := f.call(t, f.ctx, "ai_conversation_approvals", map[string]any{"conversationId": f.web}, &approvals); !response.OK {
		t.Fatalf("approvals = %+v", response.Error)
	}
	if len(approvals) != 1 || approvals[0].Tool != "exec_commands" || approvals[0].Decision != "allow" || approvals[0].ResolvedAt == nil ||
		approvals[0].Rendered != "systemctl restart nginx" {
		t.Fatalf("approvals = %+v", approvals)
	}
	if response := f.call(t, f.ctx, "ai_conversation_approvals", map[string]any{"conversationId": f.foreign}, nil); response.OK {
		t.Fatal("a foreign conversation's approvals must not be readable")
	}
}

func TestAIConversationExportArchive(t *testing.T) {
	f := newWorkbenchFixture(t)
	var preview aiConversationExportResult
	request := map[string]any{"timeZone": "UTC", "includeJson": true, "dryRun": true}
	if response := f.call(t, f.ctx, "ai_conversation_export", request, &preview); !response.OK {
		t.Fatalf("dry run = %+v", response.Error)
	}
	if preview.Matched != 4 || preview.Exported != 3 || preview.Skipped.Running != 1 || preview.Files != 6 || preview.Path != "" {
		t.Fatalf("dry run = %+v", preview)
	}

	path := filepath.Join(t.TempDir(), "ai.zip")
	delete(request, "dryRun")
	request["path"] = path
	request["labels"] = map[string]string{"user": "User", "unlinked": "No host", "decision.allow": "Allowed once"}
	var result aiConversationExportResult
	if response := f.call(t, f.ctx, "ai_conversation_export", request, &result); !response.OK {
		t.Fatalf("export = %+v", response.Error)
	}
	if result.Exported != 3 || result.Path != path || result.Bytes == 0 {
		t.Fatalf("export = %+v", result)
	}
	names, contents := readZipEntries(t, path)
	if names[0] != "manifest.json" {
		t.Fatalf("entries = %v, want the manifest first", names)
	}
	var manifest aiConversationManifest
	if err := json.Unmarshal([]byte(contents["manifest.json"]), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Conversations) != 3 || len(manifest.Skipped) != 1 || manifest.Skipped[0].ID != f.running || manifest.Skipped[0].Reason != "running" {
		t.Fatalf("manifest = %+v", manifest)
	}
	var webMarkdown, webJSON string
	for _, entry := range manifest.Conversations {
		for _, name := range entry.Files {
			if _, ok := contents[name]; !ok {
				t.Fatalf("manifest names %s, missing from %v", name, names)
			}
		}
		switch entry.ID {
		case f.web:
			if entry.Host != "web-01" || !strings.HasPrefix(entry.Files[0], "web-01/") {
				t.Fatalf("web entry = %+v", entry)
			}
			webMarkdown, webJSON = contents[entry.Files[0]], contents[entry.Files[1]]
		case f.local:
			if !strings.HasPrefix(entry.Files[0], "No host/") {
				t.Fatalf("unlinked entry = %+v", entry)
			}
		}
	}
	for _, want := range []string{
		"# Restart nginx", "## User", "please restart nginx", "nginx is running again", "`exec_commands`", "restarted",
		"```diff", "-worker 1", "+worker 4", "Allowed once", "systemctl restart nginx",
	} {
		if !strings.Contains(webMarkdown, want) {
			t.Errorf("markdown lacks %q:\n%s", want, webMarkdown)
		}
	}
	if strings.Contains(webMarkdown, "SECRET CATALOG") {
		t.Error("the skill catalog sent to the model must not be exported")
	}
	var raw struct {
		Messages []json.RawMessage `json:"messages"`
		Runs     []map[string]any  `json:"runs"`
	}
	if err := json.Unmarshal([]byte(webJSON), &raw); err != nil || len(raw.Messages) != 6 || len(raw.Runs) != 1 {
		t.Fatalf("json = %v, %d messages, %d runs", err, len(raw.Messages), len(raw.Runs))
	}

	tarPath := filepath.Join(t.TempDir(), "ai.tar.gz")
	if response := f.call(t, f.ctx, "ai_conversation_export", map[string]any{"format": "tar.gz", "path": tarPath, "assetId": f.webAsset}, &result); !response.OK {
		t.Fatalf("tar export = %+v", response.Error)
	}
	tarNames, _ := readTarGzEntries(t, tarPath)
	if len(tarNames) != 2 || result.Exported != 1 || result.Skipped.Running != 1 {
		t.Fatalf("host tar = %v, %+v", tarNames, result)
	}

	if response := f.call(t, ipc.WithUserID(context.Background(), "owner-b"), "ai_conversation_export",
		map[string]any{"ids": []string{f.web}, "dryRun": true}, &result); !response.OK || result.Matched != 0 {
		t.Fatalf("owner-b export of owner-a's conversation = %+v, %+v", response.Error, result)
	}
}

func TestAIConversationExportMarkdown(t *testing.T) {
	f := newWorkbenchFixture(t)
	path := filepath.Join(t.TempDir(), "one.md")
	var result aiConversationExportResult
	if response := f.call(t, f.ctx, "ai_conversation_export", map[string]any{"format": "md", "ids": []string{f.running}, "path": path}, &result); !response.OK {
		t.Fatalf("markdown export = %+v", response.Error)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Exported != 1 || !strings.Contains(string(data), "# Upgrade packages") || !strings.Contains(string(data), "运行中") {
		t.Fatalf("markdown = %+v\n%s", result, data)
	}
	if response := f.call(t, f.ctx, "ai_conversation_export", map[string]any{"format": "md", "ids": []string{f.web, f.db}, "path": path}, nil); response.OK {
		t.Fatal("a bare Markdown export of several conversations must be refused")
	}
	if response := f.call(t, f.ctx, "ai_conversation_export", map[string]any{"ids": []string{f.foreign}, "path": path}, nil); response.OK {
		t.Fatal("exporting nothing must be refused")
	}
}

func TestUnifiedDiff(t *testing.T) {
	got := unifiedDiff("f", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n", "1\n2\nthree\n4\n5\n6\n7\n8\n9\nten\n")
	want := "--- a/f\n+++ b/f\n@@ -1,10 +1,10 @@\n 1\n 2\n-3\n+three\n 4\n 5\n 6\n 7\n 8\n 9\n-10\n+ten\n"
	if got != want {
		t.Fatalf("diff =\n%s\nwant\n%s", got, want)
	}
	far := unifiedDiff("f", strings.Repeat("x\n", 20)+"a\n", "b\n"+strings.Repeat("x\n", 20))
	if strings.Count(far, "@@ ") != 2 {
		t.Fatalf("distant changes must form two hunks:\n%s", far)
	}
}
