package production

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type fakeSystemNotifier struct {
	status  SystemNotificationStatus
	asked   bool
	sent    []SystemNotification
	removed []string
}

func (f *fakeSystemNotifier) Status(context.Context) SystemNotificationStatus { return f.status }

func (f *fakeSystemNotifier) RequestPermission(context.Context) SystemNotificationStatus {
	f.asked = true
	f.status.Permission = SystemNotificationGranted
	return f.status
}

func (f *fakeSystemNotifier) Notify(_ context.Context, notification SystemNotification) error {
	f.sent = append(f.sent, notification)
	return nil
}

func (f *fakeSystemNotifier) Remove(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	return nil
}

func newSystemNotificationDispatcher(t *testing.T, notifier SystemNotifier) *ipc.Dispatcher {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := registerSystemNotificationCommands(dispatcher, notifier); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func TestSystemNotificationsWithoutANotifierAreUnavailable(t *testing.T) {
	dispatcher := newSystemNotificationDispatcher(t, nil)
	var status SystemNotificationStatus
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "system_notification_status", `null`), &status)
	if status.Available || status.Reason == "" {
		t.Fatalf("status = %+v, want unavailable with a reason", status)
	}
	response := dispatchStoreTest(dispatcher, "system_notify", `{"id":"n1","title":"t"}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("notify without a notifier = %+v", response)
	}
}

func TestSystemNotifyPassesOneLineOfBoundedText(t *testing.T) {
	notifier := &fakeSystemNotifier{status: SystemNotificationStatus{Available: true, Permission: SystemNotificationPrompt}}
	dispatcher := newSystemNotificationDispatcher(t, notifier)

	var status SystemNotificationStatus
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "system_notification_request", `null`), &status)
	if !notifier.asked || status.Permission != SystemNotificationGranted {
		t.Fatalf("request = %+v (asked %v)", status, notifier.asked)
	}

	long := strings.Repeat("x", 2*maxSystemNotificationBody)
	var ignored any
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "system_notify",
		`{"id":"attention:T1","title":"build\n  host","body":"`+long+`","tabId":"T1"}`), &ignored)
	if len(notifier.sent) != 1 {
		t.Fatalf("sent = %+v", notifier.sent)
	}
	sent := notifier.sent[0]
	if sent.ID != "attention:T1" || sent.Title != "build host" || sent.TabID != "T1" || len([]rune(sent.Body)) != maxSystemNotificationBody {
		t.Fatalf("sent = %+v (body %d runes)", sent, len([]rune(sent.Body)))
	}

	response := dispatchStoreTest(dispatcher, "system_notify", `{"id":"n2","title":"   "}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("notify without a title = %+v", response)
	}

	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "system_notification_remove", `{"id":"attention:T1"}`), &ignored)
	if len(notifier.removed) != 1 || notifier.removed[0] != "attention:T1" {
		t.Fatalf("removed = %v", notifier.removed)
	}
}

func TestSystemNotifyDropsControlsAndBoundsIDs(t *testing.T) {
	notifier := &fakeSystemNotifier{status: SystemNotificationStatus{Available: true, Permission: SystemNotificationGranted}}
	dispatcher := newSystemNotificationDispatcher(t, notifier)
	var ignored any
	// ESC, C1 NEXT LINE, RIGHT-TO-LEFT OVERRIDE and ZERO WIDTH SPACE go; ZWJ stays.
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "system_notify",
		`{"id":"a","title":"ok\u001b[31m\u0085\u202egnp.exe\u200b","body":"family \ud83d\udc68\u200d\ud83d\udc69"}`), &ignored)
	if len(notifier.sent) != 1 {
		t.Fatalf("sent = %+v", notifier.sent)
	}
	if got := notifier.sent[0]; got.Title != "ok[31mgnp.exe" || got.Body != "family \U0001F468\u200d\U0001F469" {
		t.Fatalf("sent = %q / %q", got.Title, got.Body)
	}

	long := strings.Repeat("i", maxSystemNotificationID+1)
	for _, args := range []string{
		`{"id":"` + long + `","title":"t"}`,
		`{"id":"a","title":"t","tabId":"` + long + `"}`,
		`{"id":"a\u202e","title":"t"}`,
	} {
		response := dispatchStoreTest(dispatcher, "system_notify", args)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
			t.Fatalf("notify %s = %+v", args, response)
		}
	}
	if response := dispatchStoreTest(dispatcher, "system_notification_remove", `{"id":"`+long+`"}`); response.OK {
		t.Fatalf("remove with a long id = %+v", response)
	}
}
