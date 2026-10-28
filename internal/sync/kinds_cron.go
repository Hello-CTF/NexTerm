package sync

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	CommandCronForeignList   = "sync_cron_foreign_list"
	CommandCronForeignRemove = "sync_cron_foreign_remove"
	CommandCronForeignAdopt  = "sync_cron_foreign_adopt"
)

// cronObject is a scheduled AI job's configuration. DeviceID binds it to
// the device that runs it: every other device keeps a copy that never runs.
// Run bookkeeping stays on each device. UpdatedAt is the configuration
// revision, which run bookkeeping does not move.
type cronObject struct {
	ID             string `json:"id"`
	SessionID      string `json:"sessionId"`
	Name           string `json:"name,omitempty"`
	Prompt         string `json:"prompt"`
	Schedule       string `json:"schedule"`
	Timezone       string `json:"timezone"`
	Enabled        bool   `json:"enabled"`
	TimeoutMS      int64  `json:"timeoutMs"`
	ModelProfileID string `json:"modelProfileId,omitempty"`
	DeviceID       string `json:"deviceId"`
	DeviceName     string `json:"deviceName,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
}

func (p *cronObject) payloadID() string { return p.ID }

// cronJobs returns the cron store over the application database, bound to
// this database's device.
func (e *Engine) cronJobs(ctx context.Context) (*cron.SQLStore, error) {
	e.cronMu.Lock()
	defer e.cronMu.Unlock()
	if e.cron != nil {
		return e.cron, nil
	}
	jobs, err := cron.NewSQLStore(ctx, e.store.GORM(), cron.WithShared(e.aiShared), cron.WithInstall(e.deviceInstall))
	if err != nil {
		return nil, err
	}
	e.cron = jobs
	return jobs, nil
}

// cronPayload is the synced form of a job; a job created before binding
// existed belongs to the device that holds it.
func cronPayload(job cron.Job, localDevice string, localName string) cronObject {
	deviceID, deviceName := job.DeviceID, job.DeviceName
	if deviceID == "" {
		deviceID, deviceName = localDevice, localName
	}
	return cronObject{
		ID: job.ID, SessionID: job.SessionID, Name: job.Name, Prompt: job.Prompt, Schedule: job.Schedule,
		Timezone: job.Timezone, Enabled: job.Enabled, TimeoutMS: job.Timeout.Milliseconds(),
		ModelProfileID: job.ModelProfileID, DeviceID: deviceID, DeviceName: deviceName,
		CreatedAt: job.CreatedAt.UnixMilli(), UpdatedAt: job.ConfigUpdatedAt.UnixMilli(),
	}
}

// syncableCronJobs lists the caller's jobs that carry an owner; jobs written
// before owners existed never run and do not sync.
func (e *Engine) syncableCronJobs(ctx context.Context) (*cron.SQLStore, []cron.Job, error) {
	jobs, err := e.cronJobs(ctx)
	if err != nil {
		return nil, nil, err
	}
	list, err := jobs.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	result := list[:0]
	for _, job := range list {
		if job.OwnerKind != "" {
			result = append(result, job)
		}
	}
	return jobs, result, nil
}

func (e *Engine) collectCronObjects(ctx context.Context, c *localCollection) error {
	if !e.aiSharedAccess(ctx) {
		return nil
	}
	jobs, list, err := e.syncableCronJobs(ctx)
	if err != nil {
		return err
	}
	device := jobs.Device()
	for _, job := range list {
		payload, err := marshalObject(cronPayload(job, device.ID, device.Name))
		if err != nil {
			return err
		}
		c.objects[job.ID] = localObject{KindCron, payload}
	}
	return nil
}

func (e *Engine) applyCronObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload cronObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("定时任务对象数据损坏：%v", err)
		return false, false
	}
	jobs, list, err := e.syncableCronJobs(ctx)
	if err != nil {
		report.warnf("定时任务 %s 无法应用：%v", payload.ID, err)
		return false, false
	}
	if deleted, ok := e.tombstoneWins(ctx, payload.ID, payload.UpdatedAt, report, "定时任务"); !ok || deleted {
		return false, false
	}
	device := jobs.Device()
	var expected *cron.Job
	for index := range list {
		if list[index].ID != payload.ID {
			continue
		}
		local := list[index]
		localPayload, err := marshalObject(cronPayload(local, device.ID, device.Name))
		if err != nil {
			report.warnf("定时任务 %s 本机数据编码失败：%v", payload.ID, err)
			return false, false
		}
		if bytes.Equal(plaintext, localPayload) {
			return false, true
		}
		if !remoteWins(payload.UpdatedAt, local.ConfigUpdatedAt.UnixMilli(), plaintext, localPayload) {
			return false, false
		}
		expected = &local
	}
	held, err := jobs.PutSynced(ctx, cron.SyncedJob{
		ID: payload.ID, SessionID: payload.SessionID, Name: payload.Name, Prompt: payload.Prompt,
		Schedule: payload.Schedule, Timezone: payload.Timezone, Enabled: payload.Enabled,
		Timeout: time.Duration(payload.TimeoutMS) * time.Millisecond, ModelProfileID: payload.ModelProfileID,
		DeviceID: payload.DeviceID, DeviceName: payload.DeviceName,
		CreatedAt: time.UnixMilli(payload.CreatedAt), ConfigUpdatedAt: time.UnixMilli(payload.UpdatedAt),
	}, expected, time.Now())
	switch {
	case err == nil:
		if err := e.clearTombstone(ctx, payload.ID); err != nil {
			report.warnf("定时任务 %s 的删除标记清除失败：%v", payload.ID, err)
		}
		if held {
			report.warnf("定时任务 %q 来自其他设备并绑定到本机，已默认停用；请在定时任务面板确认后再启用", payload.Name)
		}
		return true, false
	case errors.Is(err, cron.ErrJobRunning):
		report.markUnresolvedRef(payload.ID)
		report.warnf("定时任务 %s 正在运行，结束后再应用同步的修改", payload.ID)
	case errors.Is(err, cron.ErrSyncConflict):
		report.markUnresolvedRef(payload.ID)
	default:
		report.warnf("定时任务 %s 应用失败: %v", payload.ID, err)
	}
	return false, false
}

func (e *Engine) applyCronTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	jobs, err := e.cronJobs(ctx)
	if err != nil {
		report.warnf("应用定时任务 %s 的删除标记失败：%v", objectID, err)
		return false, false
	}
	removed, err := jobs.DeleteSynced(ctx, objectID, time.UnixMilli(deletedAt))
	switch {
	case errors.Is(err, cron.ErrJobRunning), errors.Is(err, cron.ErrSyncConflict):
		report.markUnresolvedRef(objectID)
		return false, false
	case err != nil:
		report.warnf("应用定时任务 %s 的删除标记失败：%v", objectID, err)
		return false, false
	case !removed:
		return false, false
	}
	if err := e.syncTombstonePut(ctx, objectID, KindCron, deletedAt); err != nil {
		report.warnf("定时任务 %s 的删除标记记录失败：%v", objectID, err)
		return false, false
	}
	return true, false
}

// ForeignCronJob is a synced job bound to another device: shown here, run there.
type ForeignCronJob struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prompt     string `json:"prompt"`
	Schedule   string `json:"schedule"`
	Timezone   string `json:"timezone"`
	Enabled    bool   `json:"enabled"`
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
}

type ForeignCronRemoveRequest struct {
	ID string `json:"id"`
}

type ForeignCronAdoptRequest struct {
	ID string `json:"id"`
}

// ForeignCronJobs lists the caller's jobs that run on other devices.
func (s *Service) ForeignCronJobs(ctx context.Context) ([]ForeignCronJob, error) {
	_, list, err := s.engine.syncableCronJobs(ctx)
	if err != nil {
		return nil, err
	}
	result := []ForeignCronJob{}
	for _, job := range list {
		if !job.ForeignDevice {
			continue
		}
		result = append(result, ForeignCronJob{
			ID: job.ID, Name: job.Name, Prompt: job.Prompt, Schedule: job.Schedule, Timezone: job.Timezone,
			Enabled: job.Enabled, DeviceID: job.DeviceID, DeviceName: job.DeviceName,
		})
	}
	return result, nil
}

// ForeignCronRemove deletes a job bound to another device, for example one
// whose device is gone; the deletion syncs to every device of the owner.
func (s *Service) ForeignCronRemove(ctx context.Context, request ForeignCronRemoveRequest) error {
	jobs, list, err := s.engine.syncableCronJobs(ctx)
	if err != nil {
		return err
	}
	for _, job := range list {
		if job.ID != request.ID {
			continue
		}
		if !job.ForeignDevice {
			return ipc.NewError(ipc.CodeBadParam, "该定时任务在本机运行，请在所属会话中删除")
		}
		if err := jobs.Delete(ctx, job.SessionID, job.ID, job.Revision); err != nil {
			if errors.Is(err, cron.ErrConflict) || errors.Is(err, cron.ErrNotFound) {
				return ipc.WrapError(ipc.CodeBadParam, cron.ErrConflict.Error(), err)
			}
			return err
		}
		s.wake()
		return nil
	}
	return ipc.NewError(ipc.CodeNotFound, "定时任务不存在")
}

// ForeignCronAdopt binds a job of another device to this device, for example
// when this installation replaces that device or runs a copied database. The
// change syncs, so the job then runs here only.
func (s *Service) ForeignCronAdopt(ctx context.Context, request ForeignCronAdoptRequest) error {
	jobs, list, err := s.engine.syncableCronJobs(ctx)
	if err != nil {
		return err
	}
	for _, job := range list {
		if job.ID != request.ID {
			continue
		}
		if !job.ForeignDevice {
			return ipc.NewError(ipc.CodeBadParam, "该定时任务已在本机运行")
		}
		if _, err := jobs.Adopt(ctx, job.ID, time.Now()); err != nil {
			switch {
			case errors.Is(err, cron.ErrJobRunning):
				return ipc.WrapError(ipc.CodeBadParam, cron.ErrJobRunning.Error(), err)
			case errors.Is(err, cron.ErrConflict), errors.Is(err, cron.ErrNotFound):
				return ipc.WrapError(ipc.CodeBadParam, cron.ErrConflict.Error(), err)
			}
			return err
		}
		s.wake()
		return nil
	}
	return ipc.NewError(ipc.CodeNotFound, "定时任务不存在")
}
