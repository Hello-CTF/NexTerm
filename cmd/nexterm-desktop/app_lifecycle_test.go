package main

import (
	"slices"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
	wailsevents "github.com/wailsapp/wails/v3/pkg/events"
)

type recordedBackground struct{ states []bool }

func (r *recordedBackground) SetBackground(background bool) { r.states = append(r.states, background) }

// Fake lifecycle events reach the path manager as background and foreground transitions.
func TestMobileLifecycleForwardsBackground(t *testing.T) {
	handlers := map[wailsevents.ApplicationEventType]func(*application.ApplicationEvent){}
	on := func(event wailsevents.ApplicationEventType, handler func(*application.ApplicationEvent)) func() {
		handlers[event] = handler
		return func() {}
	}
	target := &recordedBackground{}
	registerMobileLifecycle(on, target)
	for _, event := range []wailsevents.ApplicationEventType{
		wailsevents.IOS.ApplicationDidEnterBackground, wailsevents.IOS.ApplicationWillEnterForeground,
		wailsevents.Android.ActivityPaused, wailsevents.Android.ActivityResumed, wailsevents.Android.ActivityStopped,
	} {
		handler := handlers[event]
		if handler == nil {
			t.Fatalf("event %d is not subscribed", event)
		}
		handler(nil)
	}
	if want := []bool{true, false, true, false, true}; !slices.Equal(target.states, want) {
		t.Fatalf("background states = %v, want %v", target.states, want)
	}
	registerMobileLifecycle(on, nil)
}
