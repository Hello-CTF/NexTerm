package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestRecordProbeLeavesLegacyPlaintextUntouched(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	plaintext := `{"url":"https://sync.example.com","username":"u","password":"p"}`
	if err := instance.db.SettingSet(ctx, settingLink, plaintext); err != nil {
		t.Fatal(err)
	}

	instance.service.recordProbe(ctx, errors.New("probe exploded"))

	stored, found, err := instance.db.SettingGet(ctx, settingLink)
	if err != nil || !found || stored != plaintext {
		t.Fatalf("legacy link changed after probe: %q %v %v", stored, found, err)
	}
}

func TestServiceBackgroundSync(t *testing.T) {
	instance := newTestInstance(t, true)
	server := newTestSyncServer(t)
	ctx := context.Background()
	const password = "alice-password"
	server.createUser(t, "alice", password)
	groupID := ids.New()
	putTestGroup(t, instance, groupID, nil, "background")
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr(password),
	}); err != nil {
		t.Fatal(err)
	}
	instance.service.syncPeriod = 5 * time.Millisecond
	if err := instance.service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := instance.service.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		if err := server.db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM user_sync_object WHERE id = ?", groupID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background sync did not push the local group")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServiceLinkSetWakesBackgroundSync(t *testing.T) {
	instance := newTestInstance(t, true)
	server := newTestSyncServer(t)
	ctx := context.Background()
	const password = "alice-password"
	server.createUser(t, "alice", password)
	groupID := ids.New()
	putTestGroup(t, instance, groupID, nil, "wake")
	instance.service.syncPeriod = time.Hour
	if err := instance.service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := instance.service.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr(password),
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		if err := server.db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM user_sync_object WHERE id = ?", groupID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("LinkSet did not wake background sync")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServiceSyncSerializesProbeAndLinkSet(t *testing.T) {
	instance := newTestInstance(t, true)
	serverA := newTestSyncServer(t)
	serverB := newTestSyncServer(t)
	ctx := context.Background()
	serverA.createUser(t, "alice", "alice-password")
	serverB.createUser(t, "bob", "bob-password")
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(serverA.URL), Username: testPtr("alice"), Password: testPtr("alice-password"),
	}); err != nil {
		t.Fatal(err)
	}
	idsReached := make(chan struct{})
	releaseSync := make(chan struct{})
	serverA.afterIDs = func() {
		close(idsReached)
		<-releaseSync
	}
	defer func() {
		select {
		case <-releaseSync:
		default:
			close(releaseSync)
		}
	}()

	syncDone := make(chan error, 1)
	go func() {
		_, err := instance.service.Sync(ctx)
		syncDone <- err
	}()
	<-idsReached
	linkStarted := make(chan struct{})
	linkDone := make(chan error, 1)
	go func() {
		close(linkStarted)
		_, err := instance.service.LinkSet(ctx, LinkPatch{
			URL: testPtr(serverB.URL), Username: testPtr("bob"), Password: testPtr("bob-password"),
		})
		linkDone <- err
	}()
	<-linkStarted
	select {
	case err := <-linkDone:
		t.Fatalf("LinkSet completed before probe finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseSync)
	if err := <-syncDone; err != nil {
		t.Fatal(err)
	}
	if err := <-linkDone; err != nil {
		t.Fatal(err)
	}
	link, err := instance.service.LinkGet(ctx)
	if err != nil || link.URL != serverB.URL {
		t.Fatalf("LinkSet was overwritten by an old probe: %+v, %v", link, err)
	}
}

func TestServiceShutdownHonorsContext(t *testing.T) {
	instance := newTestInstance(t, true)
	instance.service.cancel = func() {}
	instance.service.done = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := instance.service.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want DeadlineExceeded", err)
	}
	if err := instance.service.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded while shutdown was still in progress")
	}
}

func TestServiceSyncEmitsChangedStatus(t *testing.T) {
	instance := newTestInstance(t, true)
	server := newTestSyncServer(t)
	ctx := context.Background()
	const password = "alice-password"
	server.createUser(t, "alice", password)
	var events []ipc.Event
	instance.service.events = ipc.EmitterFunc(func(_ context.Context, event ipc.Event) error {
		events = append(events, event)
		return nil
	})
	groupID := ids.New()
	putTestGroup(t, instance, groupID, nil, "sync-event")
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr(password),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Event != ipc.TopicSyncStatus {
		t.Fatalf("events = %+v, want one sync status event", events)
	}
}

func TestSyncReportChangedIgnoresPulledOnly(t *testing.T) {
	for _, test := range []struct {
		name   string
		report SyncReport
		want   bool
	}{
		{name: "pulled only", report: SyncReport{Pulled: 1, PullSkipped: 1}, want: false},
		{name: "decrypt failure", report: SyncReport{Pulled: 1, DecryptFailed: 1}, want: false},
		{name: "applied", report: SyncReport{Applied: 1}, want: true},
		{name: "pushed", report: SyncReport{Pushed: 1}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := syncReportChanged(test.report); got != test.want {
				t.Fatalf("syncReportChanged(%+v) = %v, want %v", test.report, got, test.want)
			}
		})
	}
}

func TestApplyObjectsEmitsChangedStatus(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	var events []ipc.Event
	instance.service.events = ipc.EmitterFunc(func(_ context.Context, event ipc.Event) error {
		events = append(events, event)
		return nil
	})
	reloads := 0
	instance.service.profilesReload = func(context.Context) error {
		reloads++
		return nil
	}
	result, err := instance.service.ApplyObjects(ctx, ApplyObjectsRequest{Objects: []ApplyObject{
		applyKnownHost(t, ids.New(), "sync.example.com", 22, "ssh-ed25519", "SHA256:sync", 100),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied != 1 {
		t.Fatalf("apply result = %+v, want one applied object", result)
	}
	if reloads != 1 {
		t.Fatalf("profile reloads = %d, want 1", reloads)
	}
	if len(events) != 1 || events[0].Event != ipc.TopicSyncStatus {
		t.Fatalf("events = %+v, want one sync status event", events)
	}
}
