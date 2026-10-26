package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// A pre-1.3.0 peer (sync protocol 2) is rejected on every object endpoint with a visible incompatibility
// error naming the required version, and every response advertises the server protocol.
func TestObjectHandlerRejectsOldPeerProtocol(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user, err := account.New(db.GORM()).CreateUser(ctx, "protocol-test-"+ids.New(), "test", "protocol-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUserID(ctx, user.ID)
	handler := NewObjectHandler(db.GORM(), nil)
	for _, path := range []string{"/sync/v2/push", "/sync/v2/delete", "/sync/v2/pull", "/sync/v2/ids", "/sync/v2/wait"} {
		for _, protocol := range []int{ProtocolVersion - 1, ProtocolVersion + 1} {
			body, err := json.Marshal(map[string]any{"protocol": protocol})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("%s protocol %d status=%d", path, protocol, recorder.Code)
			}
			if got := recorder.Header().Get(protocolHeader); got != strconv.Itoa(ProtocolVersion) {
				t.Fatalf("%s protocol header = %q", path, got)
			}
			var failure struct {
				Error *ipc.Error `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil || failure.Error == nil {
				t.Fatalf("%s failure body = %s (%v)", path, recorder.Body.String(), err)
			}
			if failure.Error.Code != ipc.CodeUnsupported || !strings.Contains(failure.Error.Message, "同步协议不兼容") {
				t.Fatalf("%s protocol %d error = %+v", path, protocol, failure.Error)
			}
			if protocol < ProtocolVersion && !strings.Contains(failure.Error.Message, "1.3.0") {
				t.Fatalf("old peer rejection does not name the required release: %q", failure.Error.Message)
			}
		}
	}
	// The current protocol passes the gate.
	request := httptest.NewRequest(http.MethodPost, "/sync/v2/ids", bytes.NewReader([]byte(`{"protocol":`+strconv.Itoa(ProtocolVersion)+`}`))).WithContext(ctx)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get(protocolHeader) != strconv.Itoa(ProtocolVersion) {
		t.Fatalf("current protocol ids: status=%d header=%q body=%s", recorder.Code, recorder.Header().Get(protocolHeader), recorder.Body.String())
	}
}

// The client turns a protocol rejection or a mismatched response into a visible incompatibility error that
// says which side must be upgraded, instead of a generic remote failure.
func TestRemoteClientReportsIncompatibleServer(t *testing.T) {
	for _, test := range []struct {
		name   string
		handle func(http.ResponseWriter)
		want   string
	}{
		{
			// A 1.2.x server: no protocol header and its own rejection of protocol 3.
			name: "pre_1_3_server",
			handle: func(w http.ResponseWriter) {
				writeSyncJSON(w, http.StatusBadRequest, ipc.Failure(ipc.NewError(ipc.CodeUnsupported, "不支持的同步协议版本 3（当前支持 2）")))
			},
			want: "请将服务端升级到 1.3.0",
		},
		{
			name: "newer_server",
			handle: func(w http.ResponseWriter) {
				w.Header().Set(protocolHeader, strconv.Itoa(ProtocolVersion+1))
				writeSyncJSON(w, http.StatusBadRequest, ipc.Failure(ipc.NewError(ipc.CodeUnsupported, "rejected")))
			},
			want: "请将此设备升级",
		},
		{
			name: "mismatched_success",
			handle: func(w http.ResponseWriter) {
				writeSyncJSON(w, http.StatusOK, IDsResponse{Protocol: ProtocolVersion - 1})
			},
			want: "请将服务端升级到 1.3.0",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { test.handle(w) }))
			t.Cleanup(server.Close)
			client, err := newRemoteClient(RemoteConfig{URL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ids(context.Background())
			if !errors.Is(err, errProtocolIncompatible) {
				t.Fatalf("ids error = %v, want protocol incompatibility", err)
			}
			requireCode(t, err, ipc.CodeUnsupported)
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), test.want)
			}
		})
	}
}

// A non-protocol rejection keeps its own error instead of being reported as an incompatibility.
func TestRemoteClientKeepsOtherErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(protocolHeader, strconv.Itoa(ProtocolVersion))
		writeSyncJSON(w, http.StatusBadRequest, ipc.Failure(ipc.NewError(ipc.CodeBadParam, "bad request")))
	}))
	t.Cleanup(server.Close)
	client, err := newRemoteClient(RemoteConfig{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ids(context.Background())
	if errors.Is(err, errProtocolIncompatible) {
		t.Fatalf("bad request was reported as a protocol mismatch: %v", err)
	}
	requireCode(t, err, ipc.CodeBadParam)
}

// An unsupported rejection from a server advertising this device's protocol is not a version mismatch: the
// server's own message is reported instead of telling the user to upgrade a device that is already current.
func TestRemoteClientPassesThroughUnsupportedFromSameProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(protocolHeader, strconv.Itoa(ProtocolVersion))
		writeSyncJSON(w, http.StatusBadRequest, ipc.Failure(ipc.NewError(ipc.CodeUnsupported, "server-side feature disabled")))
	}))
	t.Cleanup(server.Close)
	client, err := newRemoteClient(RemoteConfig{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ids(context.Background())
	if errors.Is(err, errProtocolIncompatible) {
		t.Fatalf("same-protocol rejection was reported as a protocol mismatch: %v", err)
	}
	requireCode(t, err, ipc.CodeUnsupported)
	if !strings.Contains(err.Error(), "server-side feature disabled") || strings.Contains(err.Error(), "升级") {
		t.Fatalf("error = %q, want the server's own message", err.Error())
	}
}
