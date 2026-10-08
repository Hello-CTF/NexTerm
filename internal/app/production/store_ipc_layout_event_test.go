package production

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type ipcEventRecorder struct {
	mu     sync.Mutex
	events []ipc.Event
}

func (r *ipcEventRecorder) Emit(_ context.Context, event ipc.Event) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return nil
}

func (r *ipcEventRecorder) layoutRevisions() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var revisions []int64
	for _, event := range r.events {
		if event.Event != ipc.TopicLayoutChanged {
			continue
		}
		payload, ok := event.Payload.(ipc.LayoutChangedEvent)
		if !ok {
			continue
		}
		revisions = append(revisions, payload.Revision)
	}
	return revisions
}

func (r *ipcEventRecorder) appError() (ipc.AppErrorEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range r.events {
		if event.Event != ipc.TopicAppError {
			continue
		}
		payload, ok := event.Payload.(ipc.AppErrorEvent)
		if !ok {
			continue
		}
		return payload, true
	}
	return ipc.AppErrorEvent{}, false
}

func TestLayoutPutEmitsLayoutChangedWithRevision(t *testing.T) {
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
	recorder := &ipcEventRecorder{}
	environment := ipc.Environment{Events: recorder}
	dispatch := func(args string) ipc.Response {
		return dispatcher.Dispatch(context.Background(), ipc.Request{Command: "layout_put", Args: json.RawMessage(args)}, environment)
	}

	response := dispatch(`{"args":{"data":"{\"workspaces\":[]}","revision":0}}`)
	var saved layoutSaveDTO
	requireStoreTestResponse(t, response, &saved)
	if !saved.Saved || saved.Revision != 1 {
		t.Fatalf("layout save = %+v", saved)
	}
	if revisions := recorder.layoutRevisions(); len(revisions) != 1 || revisions[0] != 1 {
		t.Fatalf("layout events = %v, want [1]", revisions)
	}

	response = dispatch(`{"args":{"data":"{\"workspaces\":[]}","revision":0}}`)
	requireStoreTestResponse(t, response, &saved)
	if saved.Saved || !saved.Conflict {
		t.Fatalf("stale layout save = %+v", saved)
	}
	if revisions := recorder.layoutRevisions(); len(revisions) != 1 {
		t.Fatalf("conflict must not emit layout://changed, got %v", revisions)
	}

	response = dispatch(`{"args":{"data":"{\"workspaces\":[]}","revision":1}}`)
	requireStoreTestResponse(t, response, &saved)
	if !saved.Saved || saved.Revision != 2 {
		t.Fatalf("second layout save = %+v", saved)
	}
	if revisions := recorder.layoutRevisions(); len(revisions) != 2 || revisions[1] != 2 {
		t.Fatalf("layout events = %v, want [1 2]", revisions)
	}

	get := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "layout_get"}, environment)
	var loaded layoutDTO
	requireStoreTestResponse(t, get, &loaded)
	if loaded.Revision != 2 {
		t.Fatalf("layout_get revision = %d", loaded.Revision)
	}
	if revisions := recorder.layoutRevisions(); len(revisions) != 2 {
		t.Fatalf("layout_get must not emit, got %v", revisions)
	}
}
