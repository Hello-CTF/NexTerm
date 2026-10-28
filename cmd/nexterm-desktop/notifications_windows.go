package main

import (
	"time"

	production "github.com/Hello-CTF/NexTerm/internal/app/production"
)

// Windows toasts cannot be taken back reliably once shown, so each tab
// notifies at most once in this interval.
const notificationMinInterval = 30 * time.Second

// platformNotification keeps remote text inert in the toast: the go-toast
// fallback runs its XML through a PowerShell expandable here-string.
func platformNotification(notification production.SystemNotification) production.SystemNotification {
	notification.Title = escapeToastScript(notification.Title)
	notification.Body = escapeToastScript(notification.Body)
	return notification
}
