package session

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/localhelper"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

func AdaptEmitter(emitter ipc.Emitter) Emitter {
	return EmitterFunc(func(ctx context.Context, event Event) error {
		if emitter == nil {
			return ipc.ErrEventsUnavailable
		}
		return emitter.Emit(ctx, ipc.Event{Event: ipc.Topic(event.Topic), Payload: event.Payload})
	})
}

func IPCError(err error) *ipc.Error {
	if err == nil {
		return nil
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return appErr
	}
	var hostKeyErr *ssh.HostKeyError
	if errors.As(err, &hostKeyErr) {
		return hostKeyIPCError(hostKeyErr)
	}
	var localUnavailable *localhelper.UnavailableError
	if errors.As(err, &localUnavailable) {
		return ipc.WrapError(ipc.Code("local_helper_unavailable"), localUnavailable.Error(), err)
	}
	switch {
	// A failed replacement wraps whatever stopped it; its stage decides
	// what the user is told, so it comes first.
	case errors.Is(err, supervisor.ErrHelperReplaceFailed):
		detail := HelperReplaceFailureFor(err)
		if detail == nil {
			detail = &HelperReplaceFailure{Cause: err.Error()}
		}
		return ipc.WrapError(ipc.Code("helper_replace_failed"), helperReplaceMessage(detail), err).WithDetail(detail)
	case errors.Is(err, ErrForbidden):
		return ipc.WrapError(ipc.CodeForbidden, "无权访问该会话", err)
	case errors.Is(err, durable.ErrLimit):
		return ipc.WrapError(ipc.Code("durable_limit"), userMessage(err), err)
	case errors.Is(err, ErrSessionNotFound), errors.Is(err, ErrTabNotFound), errors.Is(err, durable.ErrNotFound):
		return ipc.WrapError(ipc.CodeNotFound, userMessage(err), err)
	case errors.Is(err, ErrDisconnected), errors.Is(err, base.ErrDisconnected), errors.Is(err, ErrSessionClosed), errors.Is(err, ErrTabClosed):
		return ipc.WrapError(ipc.CodeDisconnected, userMessage(err), err)
	case errors.Is(err, ErrDurableDisabled):
		return ipc.WrapError(ipc.CodeUnsupported, userMessage(err), err)
	case errors.Is(err, supervisor.ErrHelperUpgradeRequired):
		wrapped := ipc.WrapError(ipc.Code("helper_upgrade_required"), userMessage(err), err)
		if detail := LegacyHelperDetailFor(err); detail != nil {
			return wrapped.WithDetail(detail)
		}
		return wrapped
	case errors.Is(err, supervisor.ErrClientUpgradeRequired):
		return ipc.WrapError(ipc.Code("client_upgrade_required"), userMessage(err), err)
	case errors.Is(err, ErrAmbiguousDurable):
		return ipc.WrapError(ipc.CodeBadParam, userMessage(err), err)
	case errors.Is(err, ErrUnsupported), errors.Is(err, base.ErrUnsupported), errors.Is(err, durable.ErrUnavailable):
		return ipc.WrapError(ipc.CodeUnsupported, userMessage(err), err)
	case errors.Is(err, ErrInvalidSize), errors.Is(err, ErrHidden), errors.Is(err, ErrInvalidOptions), errors.Is(err, ErrInvalidTitle), errors.Is(err, ErrTabExists), errors.Is(err, durable.ErrInvalidInput), errors.Is(err, durable.ErrAlreadyExists), errors.Is(err, hub.ErrInvalidChannel):
		return ipc.WrapError(ipc.CodeBadParam, "参数错误: "+userMessage(err), err)
	case errors.Is(err, context.DeadlineExceeded):
		return ipc.WrapError(ipc.CodeTimeout, userMessage(err), err)
	case errors.Is(err, ErrAssetSessionConflict), errors.Is(err, ErrStaleGeneration):
		return ipc.WrapError(ipc.CodeInternal, userMessage(err), err)
	case errors.Is(err, ErrRecoveryOverrun):
		return ipc.WrapError(ipc.Code("recovery_overrun"), userMessage(err), err)
	case errors.Is(err, ErrDurableUnrecoverable):
		return ipc.WrapError(ipc.Code("durable_unrecoverable"), userMessage(err), err)
	case errors.Is(err, durable.ErrGenerationMismatch):
		return ipc.WrapError(ipc.Code("durable_helper_restarted"), userMessage(err), err)
	default:
		return ipc.NormalizeError(err)
	}
}

func userMessage(err error) string {
	var connectErr *ssh.ConnectError
	if errors.As(err, &connectErr) {
		return connectErr.UserMessage()
	}
	switch {
	case errors.Is(err, ErrSessionNotFound):
		return "会话不存在或已关闭，请刷新后重试"
	case errors.Is(err, ErrTabNotFound):
		return "终端标签页不存在或已关闭，请刷新后重试"
	case errors.Is(err, ErrSessionClosed):
		return "会话已关闭，请重新连接"
	case errors.Is(err, ErrTabClosed):
		return "终端标签页已关闭，请重新打开"
	case errors.Is(err, ErrDisconnected), errors.Is(err, base.ErrDisconnected):
		return "会话已断开，请重新连接"
	case errors.Is(err, supervisor.ErrLegacyHelperRunning):
		return "主机上仍在运行旧版持久终端组件，当前版本无法接入其中的持久终端。它们会继续在旧版组件中运行，不会被自动结束或迁移；" +
			"可在持久终端组件窗口查看其中的会话数并确认升级，升级会结束这些会话"
	case errors.Is(err, supervisor.ErrHelperUpgradeRequired):
		return "主机上的 helper 或 nexterm-agent 版本过旧，需要升级后才能使用持久终端；其中已有的会话不会被结束"
	case errors.Is(err, supervisor.ErrClientUpgradeRequired):
		return "主机上的 helper 比当前客户端新，请先升级客户端"
	case errors.Is(err, ErrDurableDisabled):
		return "该 SSH 资产已设置为普通终端，不能新建持久终端；已有持久终端可从后台会话恢复"
	case errors.Is(err, durable.ErrLimit):
		return "持久终端组件已达到会话数量或创建频率上限，已拒绝新建终端；请结束不用的后台会话后重试（" + err.Error() + "）"
	case errors.Is(err, ErrAmbiguousDurable):
		return "多个已连接主机存在同一持久终端，请指定会话后重试"
	case errors.Is(err, ErrUnsupported), errors.Is(err, base.ErrUnsupported), errors.Is(err, durable.ErrUnavailable):
		return "当前会话不支持此操作"
	case errors.Is(err, ErrInvalidSize):
		return "终端尺寸无效"
	case errors.Is(err, ErrHidden):
		return "终端已隐藏"
	case errors.Is(err, ErrInvalidOptions):
		return "终端选项无效"
	case errors.Is(err, ErrTabExists):
		return "终端标签页已存在"
	case errors.Is(err, ErrStaleGeneration):
		return "会话已更新，请重试"
	case errors.Is(err, ErrAssetSessionConflict):
		return "该资产已有活动会话，请先断开现有会话"
	case errors.Is(err, ErrRecoveryOverrun):
		return "终端输出积压过长，早期输出已无法恢复"
	case errors.Is(err, ErrDurableUnrecoverable):
		return "该持久终端没有记录会话标识，无法恢复；请关闭后新建终端"
	case errors.Is(err, durable.ErrGenerationMismatch):
		return "主机上的 helper 已重启，该持久终端的进程已结束；请关闭后新建终端"
	case errors.Is(err, durable.ErrNotFound):
		return "资源不存在或已删除"
	case errors.Is(err, context.DeadlineExceeded):
		return "操作超时，请重试"
	}
	return err.Error()
}

type hostKeyKnownDetail struct {
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
}

type hostKeyDetail struct {
	Host        string               `json:"host"`
	Port        int                  `json:"port"`
	KeyType     string               `json:"keyType"`
	Fingerprint string               `json:"fingerprint"`
	Key         string               `json:"key,omitempty"`
	Changed     bool                 `json:"changed"`
	Known       []hostKeyKnownDetail `json:"known,omitempty"`
}

func hostKeyIPCError(err *ssh.HostKeyError) *ipc.Error {
	detail := hostKeyDetail{
		Host:        err.Presented.Host,
		Port:        err.Presented.Port,
		KeyType:     err.Presented.KeyType,
		Fingerprint: err.Presented.Fingerprint,
		Key:         err.Presented.Key,
		Changed:     !err.Pending,
	}
	for _, known := range err.Known {
		detail.Known = append(detail.Known, hostKeyKnownDetail{KeyType: known.KeyType, Fingerprint: known.Fingerprint})
	}
	return ipc.WrapError(ipc.CodeHostKeyPending, err.UserMessage(), err).WithDetail(detail)
}
