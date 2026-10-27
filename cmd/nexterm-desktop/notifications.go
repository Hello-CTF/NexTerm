package main

import (
	"context"
	"html"
	"strings"
	"sync"
	"time"

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
	Stop() error
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
	// minInterval is the shortest time between two notifications about one
	// tab; zero sends every one.
	minInterval time.Duration
	now         func() time.Time

	mu       sync.Mutex
	started  bool
	startErr error
	lastSent map[string]time.Time
}

func newDesktopNotifier(backend notificationBackend, unsupported string, minInterval time.Duration, click func(notificationClick)) *desktopNotifier {
	return &desktopNotifier{
		backend: backend, unsupported: unsupported, click: click,
		minInterval: minInterval, now: time.Now, lastSent: make(map[string]time.Time),
	}
}

func (n *desktopNotifier) start(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.started || n.startErr != nil {
		return n.startErr
	}
	n.startErr = n.backend.Start(ctx)
	if n.startErr == nil {
		n.started = true
		n.backend.OnClick(n.click)
	}
	return n.startErr
}

// Shutdown stops the platform service when it was started.
func (n *desktopNotifier) Shutdown() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.started {
		return nil
	}
	n.started = false
	return n.backend.Stop()
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

// allow reports whether a notification about key may be sent now and records it.
func (n *desktopNotifier) allow(key string) bool {
	if n.minInterval <= 0 || key == "" {
		return true
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	if last, ok := n.lastSent[key]; ok && now.Sub(last) < n.minInterval {
		return false
	}
	for other, at := range n.lastSent {
		if now.Sub(at) >= n.minInterval {
			delete(n.lastSent, other)
		}
	}
	n.lastSent[key] = now
	return true
}

func (n *desktopNotifier) Notify(ctx context.Context, notification production.SystemNotification) error {
	if n.backend == nil {
		return errNotificationsUnsupported(n.unsupported)
	}
	if err := n.start(ctx); err != nil {
		return err
	}
	key := notification.TabID
	if key == "" {
		key = notification.ID
	}
	// A notification the platform cannot take back is limited per tab
	// instead; a dropped one is still listed in the inbox.
	if !n.allow(key) {
		return nil
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

// toastScriptEscaper keeps text inert inside the PowerShell expandable
// here-string that the Windows toast fallback embeds its XML in: "$" would
// start a subexpression or variable and the backtick is the escape
// character, so both become their full-width forms.
var toastScriptEscaper = strings.NewReplacer("$", "＄", "`", "｀")

func escapeToastScript(text string) string {
	return toastScriptEscaper.Replace(text)
}

// escapeNotificationMarkup escapes a body for a notification server that
// renders the freedesktop body markup.
func escapeNotificationMarkup(text string) string {
	return html.EscapeString(text)
}

// stripToastActivationArgs removes what Windows passes when a click on a
// toast starts the app through its COM activator: -Embedding (or
// /Embedding) and, from the registered command line, a literal %1. It
// reports whether the app was started that way.
func stripToastActivationArgs(args []string) ([]string, bool) {
	cleaned := make([]string, 0, len(args))
	activated := false
	for _, arg := range args {
		switch arg {
		case "-Embedding", "/Embedding", "%1":
			activated = true
		default:
			cleaned = append(cleaned, arg)
		}
	}
	return cleaned, activated
}
