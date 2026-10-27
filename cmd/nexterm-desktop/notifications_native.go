//go:build !android && !ios

package main

import (
	"context"

	production "github.com/Hello-CTF/NexTerm/internal/app/production"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// wailsNotifications drives the Wails notification service directly instead
// of registering it as an application service, whose startup failure would
// stop the app.
type wailsNotifications struct {
	service *notifications.NotificationService
}

func newNotificationBackend() (notificationBackend, string) {
	return &wailsNotifications{service: notifications.New()}, ""
}

func (w *wailsNotifications) Start(ctx context.Context) error {
	return w.service.ServiceStartup(ctx, application.ServiceOptions{})
}

func (w *wailsNotifications) Stop() error {
	return w.service.ServiceShutdown()
}

func (w *wailsNotifications) Authorized(_ context.Context, ask bool) (bool, error) {
	if ask {
		return w.service.RequestNotificationAuthorization()
	}
	return w.service.CheckNotificationAuthorization()
}

func (w *wailsNotifications) Send(notification production.SystemNotification) error {
	notification = platformNotification(notification)
	options := notifications.NotificationOptions{
		ID:    notification.ID,
		Title: notification.Title,
		Body:  notification.Body,
	}
	if notification.TabID != "" {
		options.Data = map[string]any{"tabId": notification.TabID}
	}
	return w.service.SendNotification(options)
}

func (w *wailsNotifications) Remove(id string) error {
	return w.service.RemoveNotification(id)
}

func (w *wailsNotifications) OnClick(click func(notificationClick)) {
	w.service.OnNotificationResponse(func(result notifications.NotificationResult) {
		if result.Error != nil || result.Response.ActionIdentifier != notifications.DefaultActionIdentifier {
			return
		}
		click(clickFromResponse(result.Response.ID, result.Response.UserInfo))
	})
}
