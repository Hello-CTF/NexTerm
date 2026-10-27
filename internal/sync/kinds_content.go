package sync

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// stateRuleObject is a session state rule. The owner's rule order is local:
// a rule another device adds follows this device's rules.
type stateRuleObject struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	AssetID   string `json:"assetId,omitempty"`
	Region    string `json:"region"`
	Lines     int32  `json:"lines,omitempty"`
	Pattern   string `json:"pattern"`
	State     string `json:"state"`
	Keys      string `json:"keys,omitempty"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type runbookTemplateStepObject struct {
	ID          string `json:"id"`
	SnippetID   string `json:"snippetId,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
}

// runbookTemplateObject is a runbook template with its step command snapshots.
type runbookTemplateObject struct {
	ID          string                      `json:"id"`
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Steps       []runbookTemplateStepObject `json:"steps"`
	Version     int32                       `json:"version"`
	CreatedAt   int64                       `json:"createdAt"`
	UpdatedAt   int64                       `json:"updatedAt"`
}

// skillObject is a user override of a built-in AI skill or a custom skill;
// built-in skill content never syncs. The object ID is skill/<name>.
type skillObject struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Body        *string `json:"body,omitempty"`
	Disabled    bool    `json:"disabled,omitempty"`
	UpdatedAt   int64   `json:"updatedAt"`
}

func (p *stateRuleObject) payloadID() string       { return p.ID }
func (p *runbookTemplateObject) payloadID() string { return p.ID }
func (p *skillObject) payloadID() string           { return p.ID }

func stateRulePayload(row store.StateRuleRow) stateRuleObject {
	return stateRuleObject{
		ID: row.ID, Name: row.Name, Scope: row.Scope, AssetID: row.AssetID, Region: row.Region,
		Lines: row.RegionLines, Pattern: row.Pattern, State: row.TargetState, Keys: row.KeySequence,
		Enabled: row.Enabled, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func runbookTemplatePayload(template store.RunbookTemplate) runbookTemplateObject {
	steps := make([]runbookTemplateStepObject, len(template.Steps))
	for i, step := range template.Steps {
		steps[i] = runbookTemplateStepObject{
			ID: step.ID, SnippetID: step.SnippetID, Name: step.Name, Description: step.Description, Command: step.Command,
		}
	}
	return runbookTemplateObject{
		ID: template.ID, Name: template.Name, Description: template.Description, Steps: steps,
		Version: template.Version, CreatedAt: template.CreatedAt, UpdatedAt: template.UpdatedAt,
	}
}

func runbookTemplateFromPayload(payload runbookTemplateObject) store.RunbookTemplate {
	steps := make([]store.RunbookTemplateStep, len(payload.Steps))
	for i, step := range payload.Steps {
		steps[i] = store.RunbookTemplateStep{
			ID: step.ID, SnippetID: step.SnippetID, Name: step.Name, Description: step.Description, Command: step.Command,
		}
	}
	return store.RunbookTemplate{
		ID: payload.ID, Name: payload.Name, Description: payload.Description, Steps: steps,
		Version: payload.Version, CreatedAt: payload.CreatedAt, UpdatedAt: payload.UpdatedAt,
	}
}

func skillPayload(override skills.Override) skillObject {
	return skillObject{
		ID: skills.SyncObjectID(override.Name), Name: override.Name, Description: override.Description,
		Body: override.Body, Disabled: override.Disabled, UpdatedAt: override.UpdatedAt,
	}
}

// setSkills installs the skill manager synced overrides go through.
func (e *Engine) setSkills(manager *skills.Manager) {
	e.skillsMu.Lock()
	defer e.skillsMu.Unlock()
	e.skills = manager
}

// skillManager returns the installed skill manager, building one over the
// store when none was installed.
func (e *Engine) skillManager(ctx context.Context) (*skills.Manager, error) {
	e.skillsMu.Lock()
	defer e.skillsMu.Unlock()
	if e.skills != nil {
		return e.skills, nil
	}
	manager, err := skills.NewManager(ctx, e.store, skills.WithShared(e.aiShared))
	if err != nil {
		return nil, err
	}
	e.skills = manager
	return manager, nil
}

func (e *Engine) collectStateRuleObjects(ctx context.Context, c *localCollection) error {
	rows, err := e.store.StateRuleList(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		payload, err := marshalObject(stateRulePayload(row))
		if err != nil {
			return err
		}
		c.objects[row.ID] = localObject{KindStateRule, payload}
	}
	return nil
}

func (e *Engine) collectRunbookTemplateObjects(ctx context.Context, c *localCollection) error {
	templates, err := e.store.RunbookTemplateList(ctx)
	if err != nil {
		return err
	}
	for _, template := range templates {
		payload, err := marshalObject(runbookTemplatePayload(template))
		if err != nil {
			return err
		}
		c.objects[template.ID] = localObject{KindRunbookTemplate, payload}
	}
	return nil
}

func (e *Engine) collectSkillObjects(ctx context.Context, c *localCollection) error {
	if !e.aiSharedAccess(ctx) {
		return nil
	}
	manager, err := e.skillManager(ctx)
	if err != nil {
		return err
	}
	overrides, err := manager.Overrides(ctx)
	if err != nil {
		return err
	}
	for _, override := range overrides {
		payload, err := marshalObject(skillPayload(override))
		if err != nil {
			return err
		}
		c.objects[skills.SyncObjectID(override.Name)] = localObject{KindSkill, payload}
	}
	return nil
}

// tombstoneWins reports whether the object's local tombstone is at least as
// new as revision, so an older live copy must not resurrect it.
func (e *Engine) tombstoneWins(ctx context.Context, objectID string, revision int64, report *SyncReport, what string) (bool, bool) {
	tombstone, found, err := e.syncTombstoneGet(ctx, objectID)
	if err != nil {
		report.warnf("无法检查%s %s 的删除标记：%v", what, objectID, err)
		return false, false
	}
	return found && tombstone.DeletedAt >= revision, true
}

func (e *Engine) applyStateRuleObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload stateRuleObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("状态规则对象数据损坏：%v", err)
		return false, false
	}
	local, exists, err := e.store.StateRuleGet(ctx, payload.ID)
	if err != nil {
		report.warnf("无法检查状态规则 %s: %v", payload.ID, err)
		return false, false
	}
	if deleted, ok := e.tombstoneWins(ctx, payload.ID, payload.UpdatedAt, report, "状态规则"); !ok || deleted {
		return false, false
	}
	var expected *store.StateRuleRow
	if exists {
		localPayload, err := marshalObject(stateRulePayload(local))
		if err != nil {
			report.warnf("状态规则 %s 本机数据编码失败：%v", payload.ID, err)
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
	_, err = e.store.StateRuleSyncPut(ctx, store.StateRuleInput{
		ID: payload.ID, Name: payload.Name, Scope: payload.Scope, AssetID: payload.AssetID, Region: payload.Region,
		Lines: payload.Lines, Pattern: payload.Pattern, State: payload.State, Keys: payload.Keys,
		Enabled: payload.Enabled, UpdatedAt: payload.UpdatedAt,
	}, payload.CreatedAt, expected)
	switch {
	case err == nil:
		return true, false
	case errors.Is(err, store.ErrStateRuleAssetMissing):
		if tombstone, found, tombstoneErr := e.syncTombstoneGet(ctx, payload.AssetID); tombstoneErr == nil && found && tombstone.Kind == KindAsset {
			report.warnf("状态规则 %s 的主机 %s 已删除，规则未应用", payload.ID, payload.AssetID)
			return false, false
		}
		// The host may arrive in a later round; stay unreconciled until then.
		report.markUnresolvedRef(payload.ID)
		report.warnf("状态规则 %s 的主机 %s 尚未同步，稍后重试", payload.ID, payload.AssetID)
	case errors.Is(err, store.ErrWriteConflict):
		report.markUnresolvedRef(payload.ID)
	default:
		report.warnf("状态规则 %s 应用失败: %v", payload.ID, err)
	}
	return false, false
}

func (e *Engine) applyStateRuleTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	applied, err := e.store.StateRuleSyncDelete(ctx, objectID, deletedAt)
	if err != nil {
		report.warnf("应用状态规则 %s 的删除标记失败：%v", objectID, err)
		return false, false
	}
	return applied, false
}

func (e *Engine) applyRunbookTemplateObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload runbookTemplateObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("Runbook 模板对象数据损坏：%v", err)
		return false, false
	}
	local, err := e.store.RunbookTemplateGet(ctx, payload.ID)
	exists := err == nil
	if err != nil && !isNotFound(err) {
		report.warnf("无法检查 Runbook 模板 %s: %v", payload.ID, err)
		return false, false
	}
	if deleted, ok := e.tombstoneWins(ctx, payload.ID, payload.UpdatedAt, report, " Runbook 模板"); !ok || deleted {
		return false, false
	}
	var expected *store.RunbookTemplate
	if exists {
		localPayload, err := marshalObject(runbookTemplatePayload(local))
		if err != nil {
			report.warnf("Runbook 模板 %s 本机数据编码失败：%v", payload.ID, err)
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
	err = e.store.RunbookTemplateSyncPut(ctx, runbookTemplateFromPayload(payload), expected)
	switch {
	case err == nil:
		return true, false
	case errors.Is(err, store.ErrWriteConflict):
		report.markUnresolvedRef(payload.ID)
	default:
		report.warnf("Runbook 模板 %s 应用失败: %v", payload.ID, err)
	}
	return false, false
}

func (e *Engine) applyRunbookTemplateTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	applied, err := e.store.RunbookTemplateSyncDelete(ctx, objectID, deletedAt)
	if err != nil {
		report.warnf("应用 Runbook 模板 %s 的删除标记失败：%v", objectID, err)
		return false, false
	}
	return applied, false
}

func (e *Engine) applySkillObject(ctx context.Context, objectID string, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload skillObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("AI 技能对象数据损坏：%v", err)
		return false, false
	}
	if objectID != skills.SyncObjectID(payload.Name) {
		report.warnf("AI 技能对象 %s 与技能名称 %q 不一致", objectID, payload.Name)
		return false, false
	}
	manager, err := e.skillManager(ctx)
	if err != nil {
		report.warnf("AI 技能 %s 无法应用：%v", payload.Name, err)
		return false, false
	}
	override := skills.Override{
		Name: payload.Name, Description: payload.Description, Body: payload.Body,
		Disabled: payload.Disabled, UpdatedAt: payload.UpdatedAt,
	}
	if err := manager.ValidateSynced(override); err != nil {
		report.warnf("AI 技能 %s 未应用：%v", payload.Name, err)
		return false, false
	}
	if deleted, ok := e.tombstoneWins(ctx, objectID, payload.UpdatedAt, report, " AI 技能"); !ok || deleted {
		return false, false
	}
	overrides, err := manager.Overrides(ctx)
	if err != nil {
		report.warnf("无法检查 AI 技能 %s: %v", payload.Name, err)
		return false, false
	}
	var expected *skills.Override
	for index := range overrides {
		if overrides[index].Name != payload.Name {
			continue
		}
		local := overrides[index]
		localPayload, err := marshalObject(skillPayload(local))
		if err != nil {
			report.warnf("AI 技能 %s 本机数据编码失败：%v", payload.Name, err)
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
	err = manager.ApplySynced(ctx, override, expected)
	switch {
	case err == nil:
		if err := e.clearTombstone(ctx, objectID); err != nil {
			report.warnf("AI 技能 %s 的删除标记清除失败：%v", payload.Name, err)
		}
		return true, false
	case errors.Is(err, skills.ErrSyncConflict):
		report.markUnresolvedRef(objectID)
	default:
		report.warnf("AI 技能 %s 应用失败: %v", payload.Name, err)
	}
	return false, false
}

func (e *Engine) applySkillTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	name, ok := strings.CutPrefix(objectID, skills.SyncObjectPrefix)
	if !ok {
		report.warnf("AI 技能删除标记 %s 的 ID 不受支持", objectID)
		return false, false
	}
	manager, err := e.skillManager(ctx)
	if err != nil {
		report.warnf("应用 AI 技能 %s 的删除标记失败：%v", name, err)
		return false, false
	}
	removed, err := manager.RemoveSynced(ctx, name, deletedAt)
	if err != nil {
		report.warnf("应用 AI 技能 %s 的删除标记失败：%v", name, err)
		return false, false
	}
	if !removed {
		return false, false
	}
	if err := e.syncTombstonePut(ctx, objectID, KindSkill, deletedAt); err != nil {
		report.warnf("AI 技能 %s 的删除标记记录失败：%v", name, err)
		return false, false
	}
	return true, false
}
