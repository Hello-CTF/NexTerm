package production

import (
	"slices"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestSnippetIPCValidationAndOriginalBytes(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir(), true); err != nil {
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

	response = dispatchStoreTest(dispatcher, "snippet_update", `{"id":"`+created["id"]+`","name":" 带CRLF ","body":"echo done\r\n"}`)
	if !response.OK {
		t.Fatalf("valid update failed: %+v", response.Error)
	}
	rows := listSnippets()
	if len(rows) != 1 || rows[0].Name != "带CRLF" || rows[0].Body != "echo done\r\n" {
		t.Fatalf("updated snippet = %+v", rows)
	}
}

func TestSnippetIPCGroupPlacementAndOrdering(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	const groupA, groupB = "00000000000000000000000001", "00000000000000000000000002"
	for _, id := range []string{groupA, groupB} {
		if _, err := database.GroupUpsert(ctx, id, nil, id, 0, 1, 1); err != nil {
			t.Fatal(err)
		}
	}

	create := func(args string) string {
		t.Helper()
		response := dispatchStoreTest(dispatcher, "snippet_create", args)
		var created map[string]string
		requireStoreTestResponse(t, response, &created)
		if created["id"] == "" {
			t.Fatalf("create returned no id: %+v", created)
		}
		return created["id"]
	}
	listSnippets := func() []snippetDTO {
		t.Helper()
		response := dispatchStoreTest(dispatcher, "snippet_list", `{}`)
		var rows []snippetDTO
		requireStoreTestResponse(t, response, &rows)
		return rows
	}
	names := func(rows []snippetDTO) []string {
		t.Helper()
		result := make([]string, len(rows))
		for index, row := range rows {
			result[index] = row.Name
		}
		return result
	}
	update := func(args string) {
		t.Helper()
		response := dispatchStoreTest(dispatcher, "snippet_update", args)
		if !response.OK {
			t.Fatalf("update %s failed: %+v", args, response.Error)
		}
	}

	loose := create(`{"name":"loose","body":"echo loose","sort":9}`)
	beta := create(`{"name":"beta","body":"echo beta","groupId":"` + groupA + `","sort":1}`)
	create(`{"name":"zeta","body":"echo zeta","groupId":"` + groupB + `","sort":1}`)
	alpha := create(`{"name":"alpha","body":"echo alpha","groupId":"` + groupB + `","sort":2}`)
	create(`{"name":"eta","body":"echo eta","groupId":"` + groupB + `","sort":3}`)
	tie1 := create(`{"name":"twin","body":"echo twin1","groupId":"` + groupB + `","sort":4}`)
	tie2 := create(`{"name":"twin","body":"echo twin2","groupId":"` + groupB + `","sort":4}`)

	rows := listSnippets()
	if len(rows) != 7 {
		t.Fatalf("list = %+v", rows)
	}
	if rows[0].ID != loose || rows[0].GroupID != nil {
		t.Fatalf("ungrouped snippet must sort first: %+v", rows[0])
	}
	if rows[1].ID != beta || *rows[1].GroupID != groupA {
		t.Fatalf("group g-a must follow: %+v", rows[1])
	}
	groupBRows := []snippetDTO{rows[2], rows[3], rows[4], rows[5], rows[6]}
	for _, row := range groupBRows {
		if row.GroupID == nil || *row.GroupID != groupB {
			t.Fatalf("group g-b rows not adjacent: %+v", rows)
		}
	}
	wantOrder := []string{"zeta", "alpha", "eta", "twin", "twin"}
	if got := names(groupBRows); !slices.Equal(got, wantOrder) {
		t.Fatalf("group order = %v, want %v", got, wantOrder)
	}
	tieFirst, tieSecond := tie1, tie2
	if tie2 < tie1 {
		tieFirst, tieSecond = tie2, tie1
	}
	if groupBRows[3].ID != tieFirst || groupBRows[4].ID != tieSecond {
		t.Fatalf("id tiebreak = %v then %v, want %v then %v", groupBRows[3].ID, groupBRows[4].ID, tieFirst, tieSecond)
	}

	update(`{"id":"` + alpha + `","name":"alpha","body":"echo alpha","groupId":"` + groupA + `","sort":7}`)
	rows = listSnippets()
	if rows[1].ID != beta || rows[2].ID != alpha || *rows[2].GroupID != groupA || rows[2].Sort != 7 {
		t.Fatalf("moved snippet = %+v", rows)
	}

	update(`{"id":"` + alpha + `","name":"alpha","body":"echo alpha","groupId":null}`)
	rows = listSnippets()
	if rows[0].ID != alpha || rows[0].GroupID != nil {
		t.Fatalf("null groupId must clear placement: %+v", rows[0])
	}

	update(`{"id":"` + beta + `","name":"beta","body":"echo beta","sort":5}`)
	rows = listSnippets()
	if rows[2].ID != beta || *rows[2].GroupID != groupA || rows[2].Sort != 5 {
		t.Fatalf("omitted groupId must keep placement: %+v", rows)
	}

	update(`{"id":"` + beta + `","name":"beta","body":"echo beta"}`)
	rows = listSnippets()
	if rows[2].ID != beta || rows[2].Sort != 5 {
		t.Fatalf("omitted sort must keep value: %+v", rows)
	}
	for _, row := range rows {
		if row.CreatedAt == 0 || row.UpdatedAt == 0 {
			t.Fatalf("snippet DTO missing timestamps: %+v", row)
		}
	}
}

func TestSnippetIPCMultiStepContract(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	list := func() []snippetDTO {
		t.Helper()
		var rows []snippetDTO
		requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "snippet_list", `{}`), &rows)
		return rows
	}

	// Without steps the DTO carries an empty array, never null.
	dispatchStoreTest(dispatcher, "snippet_create", `{"name":"plain","body":"echo hi"}`)
	raw := dispatchStoreTest(dispatcher, "snippet_list", `{}`)
	if !raw.OK || !strings.Contains(string(raw.Data), `"steps":[]`) || !strings.Contains(string(raw.Data), `"description":""`) {
		t.Fatalf("plain snippet list = %s", raw.Data)
	}

	var created map[string]string
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "snippet_create", `{"name":"deploy","description":"ship it","body":"ignored","steps":[`+
		`{"id":"a","name":"build","command":"make"},{"id":"b","name":"push","command":"make push","confirm":true,"interactive":true}]}`), &created)
	var multi snippetDTO
	for _, row := range list() {
		if row.ID == created["id"] {
			multi = row
		}
	}
	if multi.Description != "ship it" || multi.Body != "make\nmake push" || len(multi.Steps) != 2 || !multi.Steps[1].Confirm || !multi.Steps[1].Interactive || multi.Steps[0].Name != "build" {
		t.Fatalf("multi-step snippet = %+v", multi)
	}

	for _, args := range []string{
		`{"name":"x","steps":[{"id":"a","command":"  "}]}`,
		`{"name":"x","steps":[{"id":"a","command":"c"},{"id":"a","command":"d"}]}`,
		`{"name":"x","description":"` + strings.Repeat("d", 4097) + `","body":"c"}`,
	} {
		if response := dispatchStoreTest(dispatcher, "snippet_create", args); response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
			t.Fatalf("snippet_create %.60s = %+v, want bad_param", args, response)
		}
	}

	// An update without steps returns the snippet to a single step.
	if response := dispatchStoreTest(dispatcher, "snippet_update", `{"id":"`+created["id"]+`","name":"deploy","body":"make all"}`); !response.OK {
		t.Fatalf("update failed: %+v", response.Error)
	}
	for _, row := range list() {
		if row.ID == created["id"] && (len(row.Steps) != 0 || row.Body != "make all" || row.Description != "") {
			t.Fatalf("after update = %+v", row)
		}
	}
}

func TestSnippetMigrationStatusCountsOnlyTheCallersLeftoverTemplates(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	for _, row := range []store.RunbookTemplateRow{
		{OwnerID: "owner-a", ID: "tpl-a1", Name: "a1", Steps: "[]", Version: 1, CreatedAt: 1, UpdatedAt: 1},
		{OwnerID: "owner-a", ID: "tpl-a2", Name: "a2", Steps: "[]", Version: 1, CreatedAt: 1, UpdatedAt: 1},
		{OwnerID: "owner-b", ID: "tpl-b1", Name: "b1", Steps: "[]", Version: 1, CreatedAt: 1, UpdatedAt: 1},
	} {
		if err := database.GORM().Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	count := func(owner string) int64 {
		t.Helper()
		response := dispatcher.Dispatch(ipc.WithUserID(ctx, owner), ipc.Request{Command: "snippet_migration_status", Args: []byte(`{}`)}, ipc.Environment{})
		var status struct {
			Unmigrated int64 `json:"unmigratedRunbookTemplates"`
		}
		requireStoreTestResponse(t, response, &status)
		return status.Unmigrated
	}
	if a, b, none := count("owner-a"), count("owner-b"), count("owner-c"); a != 2 || b != 1 || none != 0 {
		t.Fatalf("leftover templates a=%d b=%d c=%d", a, b, none)
	}
}
