//go:build !ios

package main

import (
	"time"

	production "github.com/Hello-CTF/NexTerm/internal/app/production"
)

const notificationMinInterval time.Duration = 0

// platformNotification sends the text as is: the user notification center
// renders plain text.
func platformNotification(notification production.SystemNotification) production.SystemNotification {
	return notification
}
