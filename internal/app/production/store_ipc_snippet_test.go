package production

import (
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestSnippetIPCValidationAndOriginalBytes(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir()); err != nil {
		t.Fatal(err)
	}

	listSnippets := func() []snippetDTO {
		t.Helper()
		response := dispatchStoreTest(dispatcher, "snippet_list", `{}`)
		var rows []snippetDTO
		requireStoreTestResponse(t, response, &rows)
		return rows
	}
	requireBadParam := func(command, args string) {
		t.Helper()
		response := dispatchStoreTest(dispatcher, command, args)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
			t.Fatalf("%s %s = %+v, want bad_param", command, args, response)
		}
	}

	// Blank or whitespace-only names and bodies are rejected on create and must
	// not write anything.
	for _, args := range []string{
		`{"name":"","body":"echo hi"}`,
		`{"name":"  \t","body":"echo hi"}`,
		`{"name":"看日志","body":""}`,
		`{"name":"看日志","body":" \t\r\n "}`,
	} {
		requireBadParam("snippet_create", args)
	}
	if rows := listSnippets(); len(rows) != 0 {
		t.Fatalf("rejected creates wrote rows: %+v", rows)
	}

	// Valid create: the name is trimmed like the frontend does, while the body
	// persists byte-for-byte — leading/trailing spaces, Tab and CR/LF included.
	response := dispatchStoreTest(dispatcher, "snippet_create", `{"name":"  看日志  ","body":"  tail -f /var/log/app.log \t\r\n"}`)
	var created map[string]string
	requireStoreTestResponse(t, response, &created)
	if created["id"] == "" {
		t.Fatalf("create returned no id: %+v", created)
	}
	originalBody := "  tail -f /var/log/app.log \t\r\n"
	if rows := listSnippets(); len(rows) != 1 || rows[0].Name != "看日志" || rows[0].Body != originalBody {
		t.Fatalf("created snippet = %+v", rows)
	}

	// Invalid updates are rejected and the stored row stays byte-identical.
	for _, args := range []string{
		`{"id":"` + created["id"] + `","name":"","body":"echo hi"}`,
		`{"id":"` + created["id"] + `","name":" \r\n ","body":"echo hi"}`,
		`{"id":"` + created["id"] + `","name":"看日志","body":""}`,
		`{"id":"` + created["id"] + `","name":"看日志","body":"\t "}`,
	} {
		requireBadParam("snippet_update", args)
	}
	if rows := listSnippets(); len(rows) != 1 || rows[0].Name != "看日志" || rows[0].Body != originalBody {
		t.Fatalf("rejected updates changed row: %+v", rows)
	}

	// Valid update round-trips the raw body bytes unchanged; the name trims.
	response = dispatchStoreTest(dispatcher, "snippet_update", `{"id":"`+created["id"]+`","name":" 带CRLF ","body":"echo done\r\n"}`)
	if !response.OK {
		t.Fatalf("valid update failed: %+v", response.Error)
	}
	rows := listSnippets()
	if len(rows) != 1 || rows[0].Name != "带CRLF" || rows[0].Body != "echo done\r\n" {
		t.Fatalf("updated snippet = %+v", rows)
	}
}
