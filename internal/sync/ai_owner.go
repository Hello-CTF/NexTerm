package sync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

func (e *Engine) aiOwnerID(ctx context.Context) (string, error) {
	return owner.ID(ctx, e.aiShared)
}

func (e *Engine) aiSharedAccess(ctx context.Context) bool {
	if e.aiShared || owner.OpenAccess(ctx) {
		return true
	}
	_, err := e.aiOwnerID(ctx)
	return err == nil
}

func aiKindRequiresOwner(kind string) bool {
	return kind == KindAIProfile || kind == KindAIPermission || kind == KindAIGrant
}

func aiObjectRequiresOwner(object ApplyObject) bool {
	if aiKindRequiresOwner(object.Kind) {
		return true
	}
	if object.Kind != KindTombstone {
		return false
	}
	var payload tombstoneObject
	return json.Unmarshal(object.Payload, &payload) == nil && aiKindRequiresOwner(payload.TargetKind)
}

// preflightAIObjects rejects ownership and explicit foreign-target failures before any object in the batch can write.
// Missing targets are deliberately allowed: an asset or device may arrive in a later sync round.
func (e *Engine) preflightAIObjects(ctx context.Context, objects []ApplyObject) error {
	needsOwner := false
	for _, object := range objects {
		if aiObjectRequiresOwner(object) {
			needsOwner = true
			break
		}
	}
	if !needsOwner {
		return nil
	}
	if _, err := e.aiOwnerID(ctx); err != nil {
		return err
	}
	if e.aiShared || owner.OpenAccess(ctx) {
		return nil
	}
	for _, object := range objects {
		deviceID, ok := aiGrantTargetID(object)
		if !ok || deviceID == "" {
			continue
		}
		if err := e.validateAIGrantTarget(ctx, deviceID); err != nil {
			return err
		}
	}
	return nil
}

func aiGrantTargetID(object ApplyObject) (string, bool) {
	if object.Kind == KindAIGrant {
		var payload struct {
			DeviceID string `json:"deviceId"`
		}
		if json.Unmarshal(object.Payload, &payload) != nil {
			return "", false
		}
		return payload.DeviceID, true
	}
	if object.Kind != KindTombstone {
		return "", false
	}
	var payload tombstoneObject
	if json.Unmarshal(object.Payload, &payload) != nil || payload.TargetKind != KindAIGrant {
		return "", false
	}
	for _, prefix := range []string{aiGrantDevicePrefix, aiGrantModePrefix} {
		if strings.HasPrefix(object.ID, prefix) {
			return strings.TrimPrefix(object.ID, prefix), true
		}
	}
	return "", false
}

func foreignAIGrantTarget(deviceID string) error {
	return ipc.NewError(ipc.CodeForbidden, "授权目标不属于当前用户: "+deviceID)
}

// validateAIGrantTarget rejects only a target that is known to belong to another account.
// Owner-scoped asset reads intentionally remain unnecessary here; a missing row is not foreign.
func (e *Engine) validateAIGrantTarget(ctx context.Context, deviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" || e.aiShared || owner.OpenAccess(ctx) {
		return nil
	}
	userID, err := e.aiOwnerID(ctx)
	if err != nil {
		return err
	}
	if userID == "" {
		return nil
	}
	asset := store.AssetRow{}
	err = e.store.GORM().WithContext(ctx).Where("id = ?", deviceID).First(&asset).Error
	switch {
	case err == nil:
		if asset.OwnerID != userID {
			return foreignAIGrantTarget(deviceID)
		}
		return nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if !strings.HasPrefix(deviceID, aiGrantDevicePrefix) {
		return nil
	}
	device := store.UserDevice{}
	deviceID = strings.TrimPrefix(deviceID, aiGrantDevicePrefix)
	err = e.store.GORM().WithContext(ctx).Where("id = ?", deviceID).First(&device).Error
	if err == nil {
		if device.UserID != userID {
			return foreignAIGrantTarget(deviceID)
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}
