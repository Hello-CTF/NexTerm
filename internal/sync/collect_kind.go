package sync

import (
	"context"
	"sort"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// CommandCollectKindObjects returns the local payloads of one kind that the browser collects generically.
const CommandCollectKindObjects = "sync_collect_kind_objects"

type CollectKindObjectsRequest struct {
	Kind string `json:"kind"`
}

// CollectKindObject is one local object in its canonical payload form: the browser seals Payload as is,
// so its hash and revision match what the Go engine produces for the same object.
type CollectKindObject struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	UpdatedAt int64  `json:"updatedAt"`
	Payload   string `json:"payload"`
}

// CollectKindObjectsResult carries the collected objects and the warnings of objects left out.
type CollectKindObjectsResult struct {
	Objects  []CollectKindObject `json:"objects"`
	Warnings []string            `json:"warnings"`
}

// CollectKindObjects collects the caller's objects of a kind registered for generic browser collection,
// sorted by ID. Opt-in gating stays with the caller, as for the kind-specific collect commands.
func (s *Service) CollectKindObjects(ctx context.Context, request CollectKindObjectsRequest) (CollectKindObjectsResult, error) {
	spec, ok := lookupKind(request.Kind)
	if !ok || !spec.browserCollect {
		return CollectKindObjectsResult{}, ipc.NewError(ipc.CodeBadParam, "该同步对象种类不支持通用收集")
	}
	userID, _ := UserIDFromContext(ctx)
	if spec.collectNeedsUser && userID == "" {
		return CollectKindObjectsResult{Objects: []CollectKindObject{}, Warnings: []string{}}, nil
	}
	collection := &localCollection{objects: map[string]localObject{}, report: &SyncReport{}, optIn: allKindOptIn(), userID: userID}
	if err := spec.collect(s.engine, ctx, collection); err != nil {
		return CollectKindObjectsResult{}, err
	}
	result := make([]CollectKindObject, 0, len(collection.objects))
	for id, object := range collection.objects {
		result = append(result, CollectKindObject{
			ID: id, Kind: object.kind, Name: compareName(id, object.kind, object.plaintext),
			UpdatedAt: localRevision(object.kind, object.plaintext), Payload: string(object.plaintext),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	warnings := append([]string{}, collection.report.Warnings...)
	return CollectKindObjectsResult{Objects: result, Warnings: warnings}, nil
}
