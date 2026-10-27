package main

import (
	"context"
	"sync"

	production "github.com/Hello-CTF/NexTerm/internal/app/production"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// notificationClickEvent carries the clicked notification to the frontend.
const notificationClickEvent = "app://notification-click"

// notificationClick names the notification the user clicked and the tab it
// was about.
type notificationClick struct {
	ID    string `json:"id"`
	TabID string `json:"tabId,omitempty"`
}

// notificationBackend is the platform notification service.
type notificationBackend interface {
	Start(ctx context.Context) error
	// Authorized reports whether the system lets the app notify; asked
	// requests the permission from the user first.
	Authorized(ctx context.Context, ask bool) (bool, error)
	Send(notification production.SystemNotification) error
	Remove(id string) error
	OnClick(func(notificationClick))
}

// desktopNotifier starts the platform service on first use, so a system
// without a notification service (no session bus, an unbundled macOS build)
// only loses notifications and reports why instead of failing to start.
type desktopNotifier struct {
	backend notificationBackend
	// unsupported is the reason this build has no backend.
	unsupported string
	click       func(notificationClick)

	once     sync.Once
	startErr error
}

func newDesktopNotifier(backend notificationBackend, unsupported string, click func(notificationClick)) *desktopNotifier {
	return &desktopNotifier{backend: backend, unsupported: unsupported, click: click}
}

func (n *desktopNotifier) start(ctx context.Context) error {
	n.once.Do(func() {
		n.startErr = n.backend.Start(ctx)
		if n.startErr == nil {
			n.backend.OnClick(n.click)
		}
	})
	return n.startErr
}

func (n *desktopNotifier) status(ctx context.Context, ask bool) production.SystemNotificationStatus {
	if n.backend == nil {
		return production.SystemNotificationStatus{Reason: n.unsupported}
	}
	if err := n.start(ctx); err != nil {
		return production.SystemNotificationStatus{Reason: "系统通知服务不可用：" + err.Error()}
	}
	granted, err := n.backend.Authorized(ctx, ask)
	switch {
	case err != nil:
		return production.SystemNotificationStatus{Reason: "无法读取系统通知权限：" + err.Error()}
	case granted:
		return production.SystemNotificationStatus{Available: true, Permission: production.SystemNotificationGranted}
	case ask:
		return production.SystemNotificationStatus{Available: true, Permission: production.SystemNotificationDenied, Reason: "系统设置不允许 NexTerm 发送通知"}
	default:
		return production.SystemNotificationStatus{Available: true, Permission: production.SystemNotificationPrompt}
	}
}

func (n *desktopNotifier) Status(ctx context.Context) production.SystemNotificationStatus {
	return n.status(ctx, false)
}

func (n *desktopNotifier) RequestPermission(ctx context.Context) production.SystemNotificationStatus {
	return n.status(ctx, true)
}

func (n *desktopNotifier) Notify(ctx context.Context, notification production.SystemNotification) error {
	if n.backend == nil {
		return errNotificationsUnsupported(n.unsupported)
	}
	if err := n.start(ctx); err != nil {
		return err
	}
	return n.backend.Send(notification)
}

func (n *desktopNotifier) Remove(ctx context.Context, id string) error {
	if n.backend == nil {
		return errNotificationsUnsupported(n.unsupported)
	}
	if err := n.start(ctx); err != nil {
		return err
	}
	return n.backend.Remove(id)
}

func errNotificationsUnsupported(reason string) error {
	return ipc.NewError(ipc.CodeUnsupported, reason)
}

// clickFromResponse reads the tab a clicked notification was about from the
// data it was sent with.
func clickFromResponse(id string, data map[string]any) notificationClick {
	tabID, _ := data["tabId"].(string)
	return notificationClick{ID: id, TabID: tabID}
}
