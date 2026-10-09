package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// 两个大 snippet 的远端新 revision 分布在两页时, 引擎必须在推送前耗尽分页并完成 LWW,
// 不得用本地旧对象覆盖未拉取页的新数据; 收敛后空闲同步零推送。
func TestEngineDrainsPaginationBeforePush(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	bigBody := strings.Repeat("x", 50<<20)
	snippet1 := ids.New()
	snippet2 := ids.New()
	putDeviceSnippet(t, deviceA, snippet1, "大片段一", bigBody, 100)
	putDeviceSnippet(t, deviceA, snippet2, "大片段二", bigBody, 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// B 把两个对象都更新到更高 revision 并推送; 两个密文的线上成本之和超过单页预算。
	future := ids.NowMS() + 3600_000
	putDeviceSnippet(t, deviceB, snippet1, "大片段一-新版", "new-"+bigBody, future)
	putDeviceSnippet(t, deviceB, snippet2, "大片段二-新版", "new-"+bigBody, future)
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// A 同步: 必须耗尽两页并完成 LWW, 不得用本地旧对象覆盖远端新 revision。
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	snippetRows, err := deviceA.db.SnippetList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{}
	for _, row := range snippetRows {
		contents[row.Name] = row.Body
	}
	if contents["大片段一-新版"] != "new-"+bigBody || contents["大片段二-新版"] != "new-"+bigBody {
		t.Fatal("deviceA must keep the remote new revisions of both large snippets")
	}
	if len(contents) != 2 {
		t.Fatalf("deviceA snippets=%v, want exactly the two new revisions", contents)
	}

	// 收敛后空闲同步零推送。
	reportA := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if reportA.Pulled+reportA.Applied+reportA.Pushed != 0 {
		t.Fatalf("deviceA idle sync not quiescent: %+v", reportA)
	}
	reportB := syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if reportB.Pulled+reportB.Applied+reportB.Pushed != 0 {
		t.Fatalf("deviceB idle sync not quiescent: %+v", reportB)
	}
}
