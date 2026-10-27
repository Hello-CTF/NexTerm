//go:build !android

package main

import (
	"slices"
	"sync"
	"time"

	production "github.com/Hello-CTF/NexTerm/internal/app/production"
	"github.com/godbus/dbus/v5"
)

const notificationMinInterval time.Duration = 0

var bodyMarkup = sync.OnceValue(func() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return true
	}
	var capabilities []string
	call := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications").
		Call("org.freedesktop.Notifications.GetCapabilities", 0)
	if call.Err != nil || call.Store(&capabilities) != nil {
		// Unknown server: escape, as a body shown with entities beats markup
		// a remote program controls.
		return true
	}
	return slices.Contains(capabilities, "body-markup")
})

// platformNotification escapes the body for a notification server that
// renders body markup, so remote text cannot add links or images.
func platformNotification(notification production.SystemNotification) production.SystemNotification {
	if bodyMarkup() {
		notification.Body = escapeNotificationMarkup(notification.Body)
	}
	return notification
}
