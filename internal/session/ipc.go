package session

import (
	"context"
	"errors"

	"github.com/ProbiusOfficial/NexTerm/internal/durable"
	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/ssh"
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
	switch {
	case errors.Is(err, ErrNotController):
		return ipc.WrapError(ipc.CodeNotController, userMessage(err), err)
	case errors.Is(err, ErrSessionNotFound), errors.Is(err, ErrTabNotFound), errors.Is(err, durable.ErrNotFound):
		return ipc.WrapError(ipc.CodeNotFound, userMessage(err), err)
	case errors.Is(err, ErrDisconnected), errors.Is(err, base.ErrDisconnected), errors.Is(err, ErrSessionClosed), errors.Is(err, ErrTabClosed):
		return ipc.WrapError(ipc.CodeDisconnected, userMessage(err), err)
	case errors.Is(err, ErrUnsupported), errors.Is(err, base.ErrUnsupported), errors.Is(err, durable.ErrUnavailable):
		return ipc.WrapError(ipc.CodeUnsupported, userMessage(err), err)
	case errors.Is(err, ErrInvalidSize), errors.Is(err, ErrHidden), errors.Is(err, ErrInvalidOptions), errors.Is(err, ErrTabExists), errors.Is(err, durable.ErrInvalidInput), errors.Is(err, durable.ErrAlreadyExists), errors.Is(err, hub.ErrInvalidChannel):
		return ipc.WrapError(ipc.CodeBadParam, "参数错误: "+userMessage(err), err)
	case errors.Is(err, context.DeadlineExceeded):
		return ipc.WrapError(ipc.CodeTimeout, userMessage(err), err)
	case errors.Is(err, ErrAssetSessionConflict), errors.Is(err, ErrStaleGeneration):
		return ipc.WrapError(ipc.CodeInternal, userMessage(err), err)
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
	case errors.Is(err, ErrNotController):
		return "当前没有该终端的控制权，请先获取控制权"
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
	Changed     bool                 `json:"changed"`
	Known       []hostKeyKnownDetail `json:"known,omitempty"`
}

func hostKeyIPCError(err *ssh.HostKeyError) *ipc.Error {
	detail := hostKeyDetail{
		Host:        err.Presented.Host,
		Port:        err.Presented.Port,
		KeyType:     err.Presented.KeyType,
		Fingerprint: err.Presented.Fingerprint,
		Changed:     !err.Pending,
	}
	for _, known := range err.Known {
		detail.Known = append(detail.Known, hostKeyKnownDetail{KeyType: known.KeyType, Fingerprint: known.Fingerprint})
	}
	return ipc.WrapError(ipc.CodeHostKeyPending, err.UserMessage(), err).WithDetail(detail)
}
