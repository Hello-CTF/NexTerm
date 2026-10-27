package main

import (
	"context"
	"errors"
	"testing"

	production "github.com/Hello-CTF/NexTerm/internal/app/production"
)

type fakeNotificationBackend struct {
	startErr   error
	starts     int
	authorized bool
	asked      bool
	sent       []production.SystemNotification
	click      func(notificationClick)
}

func (f *fakeNotificationBackend) Start(context.Context) error {
	f.starts++
	return f.startErr
}

func (f *fakeNotificationBackend) Authorized(_ context.Context, ask bool) (bool, error) {
	f.asked = f.asked || ask
	return f.authorized, nil
}

func (f *fakeNotificationBackend) Send(notification production.SystemNotification) error {
	f.sent = append(f.sent, notification)
	return nil
}

func (f *fakeNotificationBackend) Remove(string) error { return nil }

func (f *fakeNotificationBackend) OnClick(click func(notificationClick)) { f.click = click }

func TestDesktopNotifierStartsOnceAndHandsBackClicks(t *testing.T) {
	backend := &fakeNotificationBackend{}
	var clicks []notificationClick
	notifier := newDesktopNotifier(backend, "", func(click notificationClick) { clicks = append(clicks, click) })
	ctx := context.Background()

	if status := notifier.Status(ctx); !status.Available || status.Permission != production.SystemNotificationPrompt {
		t.Fatalf("status before permission = %+v", status)
	}
	if status := notifier.RequestPermission(ctx); status.Permission != production.SystemNotificationDenied || status.Reason == "" || !backend.asked {
		t.Fatalf("refused request = %+v", status)
	}
	backend.authorized = true
	if status := notifier.Status(ctx); status.Permission != production.SystemNotificationGranted {
		t.Fatalf("granted status = %+v", status)
	}
	if err := notifier.Notify(ctx, production.SystemNotification{ID: "a", Title: "t", TabID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if backend.starts != 1 || len(backend.sent) != 1 {
		t.Fatalf("starts %d, sent %+v", backend.starts, backend.sent)
	}
	backend.click(clickFromResponse("a", map[string]any{"tabId": "T1"}))
	if len(clicks) != 1 || clicks[0] != (notificationClick{ID: "a", TabID: "T1"}) {
		t.Fatalf("clicks = %+v", clicks)
	}
}

func TestDesktopNotifierReportsWhyItIsUnavailable(t *testing.T) {
	failing := newDesktopNotifier(&fakeNotificationBackend{startErr: errors.New("no session bus")}, "", nil)
	if status := failing.Status(context.Background()); status.Available || status.Reason == "" {
		t.Fatalf("status with a failed service = %+v", status)
	}
	if err := failing.Notify(context.Background(), production.SystemNotification{ID: "a", Title: "t"}); err == nil {
		t.Fatal("notify through a failed service succeeded")
	}

	mobile := newDesktopNotifier(nil, "移动端暂不支持系统通知", nil)
	if status := mobile.Status(context.Background()); status.Available || status.Reason != "移动端暂不支持系统通知" {
		t.Fatalf("status without a backend = %+v", status)
	}
}
