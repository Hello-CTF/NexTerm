package production

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

// dispatchHostKeyOpenTest mirrors how the application marks an admitted open request — the owner-open
// deployment mark plus the session-open grant — so the session manager's open boundary admits the
// identity-less dispatch exactly as it does for a real desktop/open deployment, while every other
// request shape stays fail-closed.
func dispatchHostKeyOpenTest(t *testing.T, application *Production, command, args string) ipc.Response {
	t.Helper()
	ctx := owner.WithOpenAccess(session.WithOpenAccess(t.Context()))
	return application.Dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args), ClientID: "client-a"}, application.Environment("client-a"))
}

func createHostKeyOpenTestAsset(t *testing.T, application *Production, port int) string {
	t.Helper()
	response := dispatchHostKeyOpenTest(t, application, "asset_create", `{"args":{"kind":"ssh","name":"host-key-test","host":"127.0.0.1","port":`+strconv.Itoa(port)+`,"username":"test","authKind":"password","options":{"password":"host-key-test-secret"}}}`)
	var created assetDTO
	requireStoreTestResponse(t, response, &created)
	return created.ID
}

func connectHostKeyOpenTestAsset(t *testing.T, application *Production, assetID string) sessionInfoDTO {
	t.Helper()
	response := dispatchHostKeyOpenTest(t, application, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	var connected sessionInfoDTO
	requireStoreTestResponse(t, response, &connected)
	return connected
}
