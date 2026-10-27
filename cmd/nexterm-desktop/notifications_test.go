package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	production "github.com/Hello-CTF/NexTerm/internal/app/production"
)

type fakeNotificationBackend struct {
	startErr   error
	starts     int
	authorized bool
	asked      bool
	sent       []production.SystemNotification
	click      func(notificationClick)
	stops      int
}

func (f *fakeNotificationBackend) Stop() error {
	f.stops++
	return nil
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
	notifier := newDesktopNotifier(backend, "", 0, func(click notificationClick) { clicks = append(clicks, click) })
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
	failing := newDesktopNotifier(&fakeNotificationBackend{startErr: errors.New("no session bus")}, "", 0, nil)
	if status := failing.Status(context.Background()); status.Available || status.Reason == "" {
		t.Fatalf("status with a failed service = %+v", status)
	}
	if err := failing.Notify(context.Background(), production.SystemNotification{ID: "a", Title: "t"}); err == nil {
		t.Fatal("notify through a failed service succeeded")
	}

	mobile := newDesktopNotifier(nil, "移动端暂不支持系统通知", 0, nil)
	if status := mobile.Status(context.Background()); status.Available || status.Reason != "移动端暂不支持系统通知" {
		t.Fatalf("status without a backend = %+v", status)
	}
}

func TestDesktopNotifierLimitsEachTabAndStopsAStartedService(t *testing.T) {
	backend := &fakeNotificationBackend{authorized: true}
	notifier := newDesktopNotifier(backend, "", 30*time.Second, nil)
	clock := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	notifier.now = func() time.Time { return clock }
	ctx := context.Background()
	if err := notifier.Shutdown(); err != nil || backend.stops != 0 {
		t.Fatalf("shutdown before start: err %v, stops %d", err, backend.stops)
	}
	send := func(tab string) {
		t.Helper()
		if err := notifier.Notify(ctx, production.SystemNotification{ID: "attention:" + tab, Title: "t", TabID: tab}); err != nil {
			t.Fatal(err)
		}
	}
	send("T1")
	send("T1")
	send("T2")
	clock = clock.Add(30 * time.Second)
	send("T1")
	var tabs []string
	for _, sent := range backend.sent {
		tabs = append(tabs, sent.TabID)
	}
	if len(tabs) != 3 || tabs[0] != "T1" || tabs[1] != "T2" || tabs[2] != "T1" {
		t.Fatalf("sent tabs = %v", tabs)
	}
	if err := notifier.Shutdown(); err != nil || backend.stops != 1 {
		t.Fatalf("shutdown: err %v, stops %d", err, backend.stops)
	}
}

func TestToastTextCannotReachThePowerShellTemplate(t *testing.T) {
	for _, text := range []string{"$(x)", "`$x", "a $env:USERPROFILE b", "``"} {
		got := escapeToastScript(text)
		for _, special := range []string{"$", "`"} {
			if strings.Contains(got, special) {
				t.Fatalf("escapeToastScript(%q) = %q keeps %q", text, got, special)
			}
		}
	}
	if got := escapeToastScript("$(x) `$x"); got != "\uFF04(x) \uFF40\uFF04x" {
		t.Fatalf("escapeToastScript = %q", got)
	}
}

func TestNotificationMarkupIsEscaped(t *testing.T) {
	if got := escapeNotificationMarkup(`<a href="x">a & b</a>`); got != "&lt;a href=&#34;x&#34;&gt;a &amp; b&lt;/a&gt;" {
		t.Fatalf("escapeNotificationMarkup = %q", got)
	}
}

func TestRunIgnoresToastActivationArguments(t *testing.T) {
	for _, args := range [][]string{{"-Embedding", "--version"}, {"/Embedding", "%1", "--version"}} {
		if code := run(args); code != 0 {
			t.Fatalf("run(%q) = %d, want 0", args, code)
		}
	}
	if code := run([]string{"-Embeddingx", "--version"}); code != 2 {
		t.Fatalf("an unknown argument still has to fail, got %d", code)
	}
}
