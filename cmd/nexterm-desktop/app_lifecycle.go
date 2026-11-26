package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	wailsevents "github.com/wailsapp/wails/v3/pkg/events"
)

// backgroundTarget is what reacts to the app moving between foreground and background; the device path manager
// closes direct legs in the background (their streams resume on the relay) and probes again in the foreground.
type backgroundTarget interface {
	SetBackground(background bool)
}

// mobileLifecycleEvents maps the iOS and Android lifecycle events to "in the background". Desktop platforms
// never emit them.
func mobileLifecycleEvents() map[wailsevents.ApplicationEventType]bool {
	return map[wailsevents.ApplicationEventType]bool{
		wailsevents.IOS.ApplicationDidEnterBackground:  true,
		wailsevents.IOS.ApplicationWillEnterForeground: false,
		wailsevents.Android.ActivityPaused:             true,
		wailsevents.Android.ActivityStopped:            true,
		wailsevents.Android.ActivityResumed:            false,
	}
}

// registerMobileLifecycle forwards the lifecycle events to target through on, the Wails event subscription.
func registerMobileLifecycle(on func(wailsevents.ApplicationEventType, func(*application.ApplicationEvent)) func(), target backgroundTarget) {
	if target == nil {
		return
	}
	for event, background := range mobileLifecycleEvents() {
		on(event, func(*application.ApplicationEvent) { target.SetBackground(background) })
	}
}
