package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestObjectHandlerRequiresUserIdentity(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler := NewObjectHandler(db.DB())
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
	handler := NewObjectHandler(db.DB())

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
	if len(pulled.Objects) != 1 || pulled.Objects[0].ID != "obj-1" || !pulled.Done {
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
}
