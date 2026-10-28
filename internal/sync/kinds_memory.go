package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
)

// memoryObject is one AI memory entry. Content is redacted with the
// write-time rules before it leaves the device and again when it lands.
type memoryObject struct {
	ID        string `json:"id"`
	Topic     string `json:"topic"`
	Content   string `json:"content"`
	Redacted  bool   `json:"redacted,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

func (p *memoryObject) payloadID() string { return p.ID }

func memoryPayload(entry memory.Entry) memoryObject {
	content, redacted := memory.RedactText(entry.Content)
	return memoryObject{
		ID: entry.ID, Topic: entry.Topic, Content: content, Redacted: entry.Redacted || redacted,
		CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
	}
}

func memoryDisplayName(_ string, plaintext []byte) string {
	var payload memoryObject
	if err := json.Unmarshal(plaintext, &payload); err == nil {
		return payload.Topic
	}
	return ""
}

// memoryScope is the memory scope the AI runtime uses for the caller: the
// shared local scope on the desktop and in open access, the account otherwise.
func (e *Engine) memoryScope(ctx context.Context) (*memory.Store, memory.Scope, error) {
	entries, err := memory.NewStore(e.store.GORM())
	if err != nil {
		return nil, memory.Scope{}, err
	}
	if e.aiShared || owner.OpenAccess(ctx) {
		return entries, memory.Scope{Tenant: "local", Subject: "default"}, nil
	}
	ownerID, err := owner.ID(ctx, false)
	if err != nil {
		return nil, memory.Scope{}, err
	}
	return entries, memory.Scope{Tenant: "user", Subject: ownerID}, nil
}

func (e *Engine) collectMemoryObjects(ctx context.Context, c *localCollection) error {
	if !e.aiSharedAccess(ctx) {
		return nil
	}
	entries, scope, err := e.memoryScope(ctx)
	if err != nil {
		return err
	}
	list, err := entries.Entries(ctx, scope)
	if err != nil {
		return err
	}
	for _, entry := range list {
		payload, err := marshalObject(memoryPayload(entry))
		if err != nil {
			return err
		}
		c.objects[entry.ID] = localObject{KindMemory, payload}
	}
	return nil
}

func (e *Engine) applyMemoryObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload memoryObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("AI 记忆数据已损坏：%v", err)
		return false, false
	}
	entries, scope, err := e.memoryScope(ctx)
	if err != nil {
		report.warnf("AI 记忆 %s 无法应用：%v", payload.ID, err)
		return false, false
	}
	if deleted, ok := e.tombstoneWins(ctx, payload.ID, payload.UpdatedAt, report, " AI 记忆"); !ok || deleted {
		return false, false
	}
	local, err := entries.Get(ctx, scope, payload.ID)
	exists := err == nil
	if err != nil && !errors.Is(err, memory.ErrNotFound) {
		report.warnf("无法检查 AI 记忆 %s: %v", payload.ID, err)
		return false, false
	}
	var expected *memory.Entry
	if exists {
		localPayload, err := marshalObject(memoryPayload(local))
		if err != nil {
			report.warnf("AI 记忆 %s 本机数据编码失败：%v", payload.ID, err)
			return false, false
		}
		if bytes.Equal(plaintext, localPayload) {
			return false, true
		}
		if !remoteWins(payload.UpdatedAt, local.UpdatedAt, plaintext, localPayload) {
			return false, false
		}
		expected = &local
	}
	err = entries.PutSynced(ctx, scope, memory.Entry{
		ID: payload.ID, Topic: payload.Topic, Content: payload.Content, Redacted: payload.Redacted,
		CreatedAt: payload.CreatedAt, UpdatedAt: payload.UpdatedAt,
	}, expected)
	switch {
	case err == nil:
		if err := e.clearTombstone(ctx, payload.ID); err != nil {
			report.warnf("AI 记忆 %s 的删除标记清除失败：%v", payload.ID, err)
		}
		return true, false
	case errors.Is(err, memory.ErrSyncConflict):
		report.markUnresolvedRef(payload.ID)
	default:
		report.warnf("AI 记忆 %s 应用失败: %v", payload.ID, err)
	}
	return false, false
}

func (e *Engine) applyMemoryTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	entries, scope, err := e.memoryScope(ctx)
	if err != nil {
		report.warnf("应用 AI 记忆 %s 的删除标记失败：%v", objectID, err)
		return false, false
	}
	removed, err := entries.DeleteSynced(ctx, scope, objectID, deletedAt)
	if err != nil {
		report.warnf("应用 AI 记忆 %s 的删除标记失败：%v", objectID, err)
		return false, false
	}
	if !removed {
		return false, false
	}
	if err := e.syncTombstonePut(ctx, objectID, KindMemory, deletedAt); err != nil {
		report.warnf("AI 记忆 %s 的删除标记记录失败：%v", objectID, err)
		return false, false
	}
	return true, false
}
