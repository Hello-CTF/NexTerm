package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

func snippetBodyHash(body string) string {
	digest := sha256.Sum256([]byte(body))
	return hex.EncodeToString(digest[:])
}

// keptSnippetSteps decides what a received snippet payload (which never carries steps) does to the local
// steps: they stay while the new body still equals their joined commands, and are dropped once an older
// client edited the commands. The description belongs to the steps; a single-step snippet keeps its own.
func keptSnippetSteps(local store.SnippetRow, newBody string) (description, steps string) {
	decoded, err := store.DecodeSnippetSteps(local.Steps)
	if err != nil || len(decoded) == 0 {
		if err != nil {
			return "", ""
		}
		return local.Description, ""
	}
	if store.SnippetStepsBody(decoded) == newBody {
		return local.Description, local.Steps
	}
	return "", ""
}

// snippetStepsObjectFromRow builds the companion of a multi-step snippet; ok is false for a single-step one.
func snippetStepsObjectFromRow(row store.SnippetRow) (object snippetStepsObject, ok bool, err error) {
	steps, err := store.DecodeSnippetSteps(row.Steps)
	if err != nil || len(steps) == 0 {
		return snippetStepsObject{}, false, err
	}
	object = snippetStepsObject{
		ID: store.SnippetStepsObjectID(row.ID), SnippetID: row.ID, BodyHash: snippetBodyHash(row.Body),
		Description: row.Description, UpdatedAt: row.UpdatedAt,
	}
	for _, step := range steps {
		object.Steps = append(object.Steps, snippetStepObject(step))
	}
	return object, true, nil
}

func (e *Engine) collectSnippetStepsObjects(ctx context.Context, c *localCollection) error {
	snippets, err := e.store.SnippetList(ctx)
	if err != nil {
		return err
	}
	for _, snippet := range snippets {
		object, ok, err := snippetStepsObjectFromRow(snippet)
		if err != nil {
			c.report.warnf("片段 %s 的步骤数据已损坏，本次不同步步骤：%v", snippet.ID, err)
			continue
		}
		if !ok {
			continue
		}
		payload, err := marshalObject(object)
		if err != nil {
			return err
		}
		c.objects[object.ID] = localObject{KindSnippetSteps, payload}
	}
	return nil
}

// validated checks a received companion and returns its steps; the body hash must be the one of the
// steps' joined commands, which is what a multi-step snippet's body always is.
func (payload snippetStepsObject) validated() ([]store.SnippetStep, error) {
	if err := store.EnsureID(payload.SnippetID); err != nil {
		return nil, err
	}
	if payload.ID != store.SnippetStepsObjectID(payload.SnippetID) {
		return nil, errors.New("步骤对象 ID 与片段不匹配")
	}
	steps := make([]store.SnippetStep, len(payload.Steps))
	for i, step := range payload.Steps {
		steps[i] = store.SnippetStep(step)
	}
	if len(steps) == 0 {
		return nil, errors.New("步骤为空")
	}
	if err := store.ValidateSnippetSteps(steps); err != nil {
		return nil, err
	}
	if err := store.ValidateSnippetDescription(payload.Description); err != nil {
		return nil, err
	}
	if payload.BodyHash != snippetBodyHash(store.SnippetStepsBody(steps)) {
		return nil, errors.New("步骤与其内容哈希不一致")
	}
	return steps, nil
}

type snippetStepsDecision int

const (
	// stepsApply: the companion belongs to the local snippet's current body and may be written.
	stepsApply snippetStepsDecision = iota
	// stepsIgnore: the companion can never apply (deleted, or the commands were edited elsewhere since).
	stepsIgnore
	// stepsRetry: the snippet version the companion was made for has not arrived yet.
	stepsRetry
)

// decideSnippetSteps places a received companion against the local snippet and the deletion markers.
func (e *Engine) decideSnippetSteps(ctx context.Context, payload snippetStepsObject) (snippetStepsDecision, store.SnippetRow, error) {
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		return stepsIgnore, store.SnippetRow{}, err
	} else if found && tombstone.DeletedAt >= payload.UpdatedAt {
		return stepsIgnore, store.SnippetRow{}, nil
	}
	local, err := e.store.SnippetGet(ctx, payload.SnippetID)
	if isNotFound(err) {
		if tombstone, found, tombstoneErr := e.syncTombstoneGet(ctx, payload.SnippetID); tombstoneErr != nil {
			return stepsIgnore, store.SnippetRow{}, tombstoneErr
		} else if found && tombstone.Kind == KindSnippet && tombstone.DeletedAt >= payload.UpdatedAt {
			return stepsIgnore, store.SnippetRow{}, nil
		}
		return stepsRetry, store.SnippetRow{}, nil
	}
	if err != nil {
		return stepsIgnore, store.SnippetRow{}, err
	}
	if snippetBodyHash(local.Body) != payload.BodyHash {
		if local.UpdatedAt < payload.UpdatedAt {
			return stepsRetry, local, nil
		}
		return stepsIgnore, local, nil
	}
	return stepsApply, local, nil
}

// validateSnippetStepsApply makes the browser retry a companion whose snippet has not been applied yet.
func (e *Engine) validateSnippetStepsApply(ctx context.Context, canonical []byte) string {
	var payload snippetStepsObject
	if err := unmarshalObject(canonical, &payload); err != nil {
		return err.Error()
	}
	if _, err := payload.validated(); err != nil {
		return err.Error()
	}
	decision, _, err := e.decideSnippetSteps(ctx, payload)
	if err != nil {
		return "无法检查片段：" + err.Error()
	}
	if decision == stepsRetry {
		return "片段 " + payload.SnippetID + " 尚未同步，稍后重试"
	}
	return ""
}

// applySnippetStepsObject writes a companion's steps onto the snippet it was made for. It never creates a
// snippet and never touches a body: a companion for a body the snippet no longer has is ignored, which is
// how a command edit by an older client downgrades the snippet to single-step for good.
func (e *Engine) applySnippetStepsObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload snippetStepsObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("片段步骤数据已损坏：%v", err)
		return false, false
	}
	steps, err := payload.validated()
	if err != nil {
		report.warnf("片段 %s 的步骤不合法，已跳过：%v", payload.SnippetID, err)
		return false, false
	}
	decision, local, err := e.decideSnippetSteps(ctx, payload)
	if err != nil {
		report.warnf("无法检查片段 %s 的步骤：%v", payload.SnippetID, err)
		return false, false
	}
	switch decision {
	case stepsRetry:
		// Not recorded as reconciled: the next round pulls the companion again once the snippet is here.
		report.markUnresolvedRef(payload.ID)
		return false, false
	case stepsIgnore:
		return false, false
	}
	localSteps, err := store.DecodeSnippetSteps(local.Steps)
	if err != nil {
		localSteps = nil
	}
	if len(localSteps) > 0 {
		if local.Description == payload.Description && sameSnippetSteps(localSteps, steps) {
			return false, true
		}
		if payload.UpdatedAt < local.UpdatedAt {
			return false, false
		}
	}
	encoded, err := marshalObject(steps)
	if err != nil {
		report.warnf("片段 %s 的步骤编码失败：%v", payload.SnippetID, err)
		return false, false
	}
	row := local
	row.Description, row.Steps = payload.Description, string(encoded)
	if err := e.snippetUpsert(ctx, row, &local); err != nil {
		if errors.Is(err, store.ErrWriteConflict) {
			report.markUnresolvedRef(payload.ID)
		} else {
			report.warnf("片段 %s 的步骤应用失败：%v", payload.SnippetID, err)
		}
		return false, false
	}
	marker := query.Use(e.store.GORM()).SyncTombstone
	if _, err := marker.WithContext(ctx).Where(marker.ID.Eq(payload.ID)).Where(ownerConditions(ctx, marker.TableName())...).Delete(); err != nil {
		report.warnf("片段 %s 的步骤删除标记清理失败：%v", payload.SnippetID, err)
	}
	report.markChanged(KindSnippet)
	return true, false
}

func sameSnippetSteps(left, right []store.SnippetStep) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// applySnippetStepsTombstone drops the steps of the snippet a companion belongs to, unless the snippet was
// edited after the marker. The companion ID cannot be inverted, so the snippet is found by deriving it.
func (e *Engine) applySnippetStepsTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	snippets, err := e.store.SnippetList(ctx)
	if err != nil {
		report.warnf("无法检查片段步骤 %s：%v", objectID, err)
		return false, false
	}
	var target *store.SnippetRow
	for i := range snippets {
		if snippets[i].Steps != "" && store.SnippetStepsObjectID(snippets[i].ID) == objectID {
			target = &snippets[i]
			break
		}
	}
	if target != nil && target.UpdatedAt > deletedAt {
		return false, false
	}
	if err := e.syncTombstonePut(ctx, objectID, KindSnippetSteps, deletedAt); err != nil {
		report.warnf("片段步骤 %s 的删除标记记录失败：%v", objectID, err)
		return false, false
	}
	if target == nil {
		return true, false
	}
	row := *target
	row.Description, row.Steps = "", ""
	if err := e.snippetUpsert(ctx, row, target); err != nil {
		report.warnf("片段 %s 的步骤清除失败：%v", target.ID, err)
		return false, false
	}
	report.markChanged(KindSnippet)
	return true, false
}
