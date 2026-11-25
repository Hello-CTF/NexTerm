package account

import (
	"context"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

// memoryUserTenant is the memory scope tenant of server accounts; the subject is the user ID.
const memoryUserTenant = "user"

// BeginDeleteUser is the first, reversible step of an administrator deleting an account: it checks the
// target exists and is not the last active superadmin, then disables it and revokes its login sessions,
// so no new work can be admitted for the account while its live state is stopped and PurgeUser runs.
// The blocked listeners run after the commit, while the account's device rows still exist.
func (a *Accounts) BeginDeleteUser(ctx context.Context, userID string) (*User, error) {
	if !ids.Valid(userID) {
		return nil, ipc.NewError(ipc.CodeNotFound, "用户不存在")
	}
	var target *User
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		appUser := query.Use(tx).AppUser
		row, err := appUser.WithContext(ctx).Where(appUser.ID.Eq(userID)).First()
		if err == gorm.ErrRecordNotFound {
			return ipc.NewError(ipc.CodeNotFound, "用户不存在")
		}
		if err != nil {
			return dbError(err)
		}
		if row.Role == string(RoleSuperadmin) && row.State == string(StateActive) {
			others, err := appUser.WithContext(ctx).Where(
				appUser.Role.Eq(string(RoleSuperadmin)),
				appUser.State.Eq(string(StateActive)),
				appUser.ID.Neq(userID),
			).Count()
			if err != nil {
				return dbError(err)
			}
			if others == 0 {
				return ipc.NewError(ipc.CodeBadParam, "不能删除最后一个可用的超级管理员")
			}
		}
		if _, err := appUser.WithContext(ctx).Where(appUser.ID.Eq(userID)).
			UpdateColumns(map[string]any{"state": string(StateDisabled), "updated_at": a.now()}); err != nil {
			return dbError(err)
		}
		userSession := query.Use(tx).UserSession
		if _, err := userSession.WithContext(ctx).
			Where(userSession.UserID.Eq(userID), userSession.RevokedAt.IsNull()).
			UpdateColumns(map[string]any{"revoked_at": a.now()}); err != nil {
			return dbError(err)
		}
		target = userFromModel(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.epochs.bump(userID)
	a.epochs.clearPending(userID)
	a.notifyUserBlocked(userID)
	return target, nil
}

// PurgeUser irreversibly removes the account row and every row owned by, shared with, or keyed by the
// user in one transaction. It is idempotent so a caller can sweep again after late writers settle; audit
// records the user owns go with the account, while records other accounts own (an administrator's
// record of this deletion) stay. User IDs are random ULIDs, so a recreated username never matches them.
func (a *Accounts) PurgeUser(ctx context.Context, userID string) error {
	// Every account ID is a ULID; anything else could match unrelated setting key segments (".user.").
	if !ids.Valid(userID) {
		return ipc.NewError(ipc.CodeBadParam, "参数错误: 用户 ID 无效")
	}
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return purgeUserRows(ctx, tx, userID)
	})
	if err != nil {
		return dbError(err)
	}
	a.epochs.bump(userID)
	a.epochs.clearPending(userID)
	return nil
}

func purgeUserRows(ctx context.Context, tx *gorm.DB, userID string) error {
	q := query.Use(tx)

	// Rows keyed only by a device ID: resolve the user's devices before their rows go.
	var deviceIDs []string
	if err := q.UserDevice.WithContext(ctx).Where(q.UserDevice.UserID.Eq(userID)).Pluck(q.UserDevice.ID, &deviceIDs); err != nil {
		return err
	}
	if len(deviceIDs) > 0 {
		steps := []func() error{
			func() error {
				_, err := q.DeviceAgent.WithContext(ctx).Where(q.DeviceAgent.DeviceID.In(deviceIDs...)).Delete()
				return err
			},
			func() error {
				_, err := q.DeviceMetrics.WithContext(ctx).Where(q.DeviceMetrics.DeviceID.In(deviceIDs...)).Delete()
				return err
			},
			func() error {
				_, err := q.DeviceMetricsHourly.WithContext(ctx).Where(q.DeviceMetricsHourly.DeviceID.In(deviceIDs...)).Delete()
				return err
			},
			func() error {
				_, err := q.HostShare.WithContext(ctx).Where(q.HostShare.DeviceID.In(deviceIDs...)).Delete()
				return err
			},
			func() error {
				_, err := q.DeviceAlertSettings.WithContext(ctx).Where(q.DeviceAlertSettings.DeviceID.In(deviceIDs...)).Delete()
				return err
			},
			func() error {
				_, err := q.DeviceAlertState.WithContext(ctx).Where(q.DeviceAlertState.DeviceID.In(deviceIDs...)).Delete()
				return err
			},
			func() error {
				_, err := q.DeviceAlertEvent.WithContext(ctx).Where(q.DeviceAlertEvent.DeviceID.In(deviceIDs...)).Delete()
				return err
			},
		}
		for _, step := range steps {
			if err := step(); err != nil {
				return err
			}
		}
	}

	// Per-user settings live in the global key/value table under keys that embed the user ID.
	var keys []string
	if err := q.Setting.WithContext(ctx).Where(q.Setting.Key.Like("%"+userID+"%")).Pluck(q.Setting.Key, &keys); err != nil {
		return err
	}
	owned := keys[:0]
	for _, key := range keys {
		if settingKeyNamesUser(key, userID) {
			owned = append(owned, key)
		}
	}
	if len(owned) > 0 {
		if _, err := q.Setting.WithContext(ctx).Where(q.Setting.Key.In(owned...)).Delete(); err != nil {
			return err
		}
	}

	steps := []func() error{
		// Account, credentials and sync state.
		func() error {
			_, err := q.UserSession.WithContext(ctx).Where(q.UserSession.UserID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.UserDevice.WithContext(ctx).Where(q.UserDevice.UserID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.SyncCredential.WithContext(ctx).Where(q.SyncCredential.UserID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.DeviceEnrollCode.WithContext(ctx).Where(q.DeviceEnrollCode.UserID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.UserDEK.WithContext(ctx).Where(q.UserDEK.UserID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.UserTOTP.WithContext(ctx).Where(q.UserTOTP.UserID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.UserTOTPRecoveryCode.WithContext(ctx).Where(q.UserTOTPRecoveryCode.UserID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.UserSetting.WithContext(ctx).Where(q.UserSetting.UserID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.UserSyncObject.WithContext(ctx).Where(q.UserSyncObject.UserID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.UserSyncHead.WithContext(ctx).Where(q.UserSyncHead.UserID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.SyncState.WithContext(ctx).Where(q.SyncState.UserID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.CredentialTombstone.WithContext(ctx).Where(q.CredentialTombstone.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.SyncTombstone.WithContext(ctx).Where(q.SyncTombstone.OwnerID.Eq(userID)).Delete()
			return err
		},
		// Assets and workspace data.
		func() error {
			_, err := q.AssetRow.WithContext(ctx).Where(q.AssetRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.AssetGroupRow.WithContext(ctx).Where(q.AssetGroupRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.CredentialRow.WithContext(ctx).Where(q.CredentialRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.SnippetRow.WithContext(ctx).Where(q.SnippetRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.KnownHostRow.WithContext(ctx).Where(q.KnownHostRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.ForwardRuleRow.WithContext(ctx).Where(q.ForwardRuleRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.StateRuleRow.WithContext(ctx).Where(q.StateRuleRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		// Terminal history: audit records, recordings, transcripts and the command log.
		func() error {
			_, err := q.AuditRow.WithContext(ctx).Where(q.AuditRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.RecordingRow.WithContext(ctx).Where(q.RecordingRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.TranscriptRow.WithContext(ctx).Where(q.TranscriptRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.TranscriptChunkRow.WithContext(ctx).Where(q.TranscriptChunkRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.TranscriptChunkTS.WithContext(ctx).Where(q.TranscriptChunkTS.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.TranscriptTSIndex.WithContext(ctx).Where(q.TranscriptTSIndex.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.TranscriptKeyframe.WithContext(ctx).Where(q.TranscriptKeyframe.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.CommandLog.WithContext(ctx).Where(q.CommandLog.UserID.Eq(userID)).Delete()
			return err
		},
		// Sharing, in both directions.
		func() error {
			_, err := q.HostShare.WithContext(ctx).Where(q.HostShare.OwnerID.Eq(userID)).Or(q.HostShare.RecipientID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.SharePreviewLink.WithContext(ctx).Where(q.SharePreviewLink.OwnerID.Eq(userID)).Delete()
			return err
		},
		// Batch execution and runbooks.
		func() error {
			_, err := q.ExecBatchRow.WithContext(ctx).Where(q.ExecBatchRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.ExecBatchResultRow.WithContext(ctx).Where(q.ExecBatchResultRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.ExecTargetSetRow.WithContext(ctx).Where(q.ExecTargetSetRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.ExecTargetSetItemRow.WithContext(ctx).Where(q.ExecTargetSetItemRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.RunbookTemplateRow.WithContext(ctx).Where(q.RunbookTemplateRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		// AI assistant: conversations, runs, scheduled jobs, memory, watches and agent state.
		func() error {
			_, err := q.ConversationRow.WithContext(ctx).Where(q.ConversationRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.MessageRow.WithContext(ctx).Where(q.MessageRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.RunRow.WithContext(ctx).Where(q.RunRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.RunEventRow.WithContext(ctx).Where(q.RunEventRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.HitlRunRow.WithContext(ctx).Where(q.HitlRunRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.CheckpointRow.WithContext(ctx).Where(q.CheckpointRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.OutcomeRecord.WithContext(ctx).Where(q.OutcomeRecord.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.CronJob.WithContext(ctx).Where(q.CronJob.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.MemoryEntry.WithContext(ctx).Where(q.MemoryEntry.Tenant.Eq(memoryUserTenant), q.MemoryEntry.Subject.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.MemorySettings.WithContext(ctx).Where(q.MemorySettings.Tenant.Eq(memoryUserTenant), q.MemorySettings.Subject.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.AgentWatchRow.WithContext(ctx).Where(q.AgentWatchRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.AgentWatchOutboxRow.WithContext(ctx).Where(q.AgentWatchOutboxRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.AgentWatchTargetRow.WithContext(ctx).Where(q.AgentWatchTargetRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.AgentRosterRow.WithContext(ctx).Where(q.AgentRosterRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.AgentOutcomeRow.WithContext(ctx).Where(q.AgentOutcomeRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.AgentEvidenceKeyRow.WithContext(ctx).Where(q.AgentEvidenceKeyRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.AgentEvidenceAnchorRow.WithContext(ctx).Where(q.AgentEvidenceAnchorRow.OwnerID.Eq(userID)).Delete()
			return err
		},
		// Device alerting owned by the user.
		func() error {
			_, err := q.DeviceAlertSettings.WithContext(ctx).Where(q.DeviceAlertSettings.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.DeviceAlertState.WithContext(ctx).Where(q.DeviceAlertState.OwnerID.Eq(userID)).Delete()
			return err
		},
		func() error {
			_, err := q.DeviceAlertEvent.WithContext(ctx).Where(q.DeviceAlertEvent.OwnerID.Eq(userID)).Delete()
			return err
		},
		// The account itself goes last.
		func() error { _, err := q.AppUser.WithContext(ctx).Where(q.AppUser.ID.Eq(userID)).Delete(); return err },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// settingKeyNamesUser reports whether a setting key embeds userID as a whole key segment (".user.<id>",
// "user.layout.<id>", "sync.cursor.<id>", "sync.kh_tombstone.<id>.<host>"), not as part of a longer token.
func settingKeyNamesUser(key, userID string) bool {
	if userID == "" {
		return false
	}
	for offset := 0; ; {
		index := strings.Index(key[offset:], userID)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(userID)
		if (start == 0 || !isIDChar(key[start-1])) && (end == len(key) || !isIDChar(key[end])) {
			return true
		}
		offset = start + 1
	}
}

func isIDChar(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}
