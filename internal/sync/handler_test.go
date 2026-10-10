package sync

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestObjectHandlerRequiresUserIdentity(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler := NewObjectHandler(db.DB(), db.Backend())
	for _, path := range []string{"/sync/v2/push", "/sync/v2/pull", "/sync/v2/ids"} {
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{"protocol":2}`)))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s without identity: status=%d", path, recorder.Code)
		}
	}
}

func TestObjectHandlerProtocolGateAndConflict(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user, err := account.New(db.DB()).CreateUser(ctx, "handler-test-"+ids.New(), "test", "handler-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	userID := user.ID
	ctx = WithUserID(ctx, userID)
	handler := NewObjectHandler(db.DB(), db.Backend())

	post := func(path string, payload any) *httptest.ResponseRecorder {
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	response := post("/sync/v2/push", map[string]any{"protocol": 1, "known_head": "", "objects": []any{}})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("protocol 1 push status=%d", response.Code)
	}
	response = post("/sync/v2/push", PushRequest{Protocol: ProtocolVersion, KnownHead: "stale", Objects: []WireObject{{ID: "obj-1", Blob: []byte("x")}}})
	if response.Code != http.StatusConflict {
		t.Fatalf("stale head push status=%d", response.Code)
	}

	response = post("/sync/v2/push", PushRequest{Protocol: ProtocolVersion, KnownHead: genesisHead(userID), Objects: []WireObject{{ID: "obj-1", Blob: []byte("x")}}})
	if response.Code != http.StatusOK {
		t.Fatalf("genesis push status=%d body=%s", response.Code, response.Body.String())
	}
	var pushed PushResponse
	if err := json.Unmarshal(response.Body.Bytes(), &pushed); err != nil {
		t.Fatal(err)
	}
	if pushed.Applied != 1 || pushed.MaxSeq != 1 || pushed.Head == genesisHead(userID) {
		t.Fatalf("push response=%+v", pushed)
	}

	response = post("/sync/v2/pull", PullRequest{Protocol: ProtocolVersion, SinceSeq: 0})
	if response.Code != http.StatusOK {
		t.Fatalf("pull status=%d", response.Code)
	}
	var pulled PullResponse
	if err := json.Unmarshal(response.Body.Bytes(), &pulled); err != nil {
		t.Fatal(err)
	}
	if len(pulled.Objects) != 1 || pulled.Objects[0].ID != "obj-1" || !pulled.CursorDone {
		t.Fatalf("pull response=%+v", pulled)
	}

	response = post("/sync/v2/ids", IDsRequest{Protocol: ProtocolVersion})
	if response.Code != http.StatusOK {
		t.Fatalf("ids status=%d", response.Code)
	}
	var ids IDsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &ids); err != nil {
		t.Fatal(err)
	}
	if len(ids.Entries) != 1 || ids.Entries[0].BlobHash != hashBlobHex([]byte("x")) {
		t.Fatalf("ids response=%+v", ids)
	}

	response = post("/sync/v2/ids", IDsRequest{Protocol: ProtocolVersion, KnownHead: pushed.Head})
	var unchanged IDsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &unchanged); err != nil {
		t.Fatal(err)
	}
	if !unchanged.Unchanged || len(unchanged.Entries) != 0 || unchanged.Head != pushed.Head || unchanged.MaxSeq != 1 {
		t.Fatalf("known_head response=%+v", unchanged)
	}
	response = post("/sync/v2/ids", IDsRequest{Protocol: ProtocolVersion, KnownHead: "stale"})
	ids = IDsResponse{}
	if err := json.Unmarshal(response.Body.Bytes(), &ids); err != nil {
		t.Fatal(err)
	}
	if ids.Unchanged || len(ids.Entries) != 1 {
		t.Fatalf("stale known_head response=%+v", ids)
	}
}

func TestObjectHandlerGzipRequestAndResponse(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user, err := account.New(db.DB()).CreateUser(ctx, "gzip-test-"+ids.New(), "test", "handler-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUserID(ctx, user.ID)
	handler := NewObjectHandler(db.DB(), db.Backend())
	payload, err := json.Marshal(PushRequest{Protocol: ProtocolVersion, KnownHead: genesisHead(user.ID), Objects: []WireObject{{ID: "obj-1", Blob: make([]byte, 2048)}}})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/sync/v2/push", bytes.NewReader(compressed.Bytes())).WithContext(ctx)
	request.Header.Set("Content-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Encoding") != "" {
		t.Fatalf("small response should stay uncompressed: status=%d headers=%v", recorder.Code, recorder.Header())
	}

	pull := func(acceptEncoding string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/sync/v2/pull", bytes.NewReader([]byte(`{"protocol":2}`))).WithContext(ctx)
		request.Header.Set("Accept-Encoding", acceptEncoding)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	recorder = pull("gzip")
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("large response should use gzip: status=%d headers=%v", recorder.Code, recorder.Header())
	}
	reader, err := gzip.NewReader(recorder.Body)
	if err != nil {
		t.Fatal(err)
	}
	var response PullResponse
	if err := json.NewDecoder(reader).Decode(&response); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if len(response.Objects) != 1 || len(response.Objects[0].Blob) != 2048 {
		t.Fatalf("gzip pull response=%+v", response)
	}
	if recorder = pull("gzip;q=0"); recorder.Header().Get("Content-Encoding") != "" {
		t.Fatalf("gzip;q=0 must stay uncompressed: headers=%v", recorder.Header())
	}
}

func TestSyncResponseWriterSkipsGzipWhenLarger(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := newSyncResponseWriter(recorder, true)
	payload := make([]byte, 2048)
	state := uint32(1)
	for i := range payload {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		payload[i] = byte(state)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if recorder.Header().Get("Content-Encoding") != "" || !bytes.Equal(recorder.Body.Bytes(), payload) {
		t.Fatalf("larger gzip must fall back to identity: headers=%v body=%d", recorder.Header(), recorder.Body.Len())
	}
}

// M165: /sync/v2 链路的 cursor 身份注入(WithUserID)与 /rpc 链路共享同一 ipc key, 行为不变。
func TestWithUserIDSharesIPCKey(t *testing.T) {
	ctx := WithUserID(context.Background(), "u-1")
	if userID, ok := UserIDFromContext(ctx); !ok || userID != "u-1" {
		t.Fatalf("sync UserIDFromContext = %q %v", userID, ok)
	}
	if userID, ok := ipc.UserIDFromContext(ctx); !ok || userID != "u-1" {
		t.Fatalf("ipc UserIDFromContext = %q %v", userID, ok)
	}
}
