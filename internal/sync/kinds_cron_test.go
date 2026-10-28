package sync

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
)

func deviceCron(t *testing.T, device *testDevice) *cron.SQLStore {
	t.Helper()
	jobs, err := device.engine.cronJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

func deviceCronJob(t *testing.T, device *testDevice, id string) (cron.Job, bool) {
	t.Helper()
	jobs, err := deviceCron(t, device).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.ID == id {
			return job, true
		}
	}
	return cron.Job{}, false
}

// A job reaches the owner's other device bound to its own device, configuration changes follow it,
// and removing the copy on the other device deletes the job everywhere.
func TestCronJobsSyncBoundToTheirDevice(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	userID, _ := seedTestSyncUser(t, server.db, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	for _, device := range []*testDevice{deviceA, deviceB} {
		if err := device.db.SetCronSyncOptIn(ctx, userID, true); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	created, err := deviceCron(t, deviceA).Create(ctx, cron.Job{
		ID: "job-1", SessionID: "session-a", Name: "disk report", Prompt: "report disk usage", Schedule: "0 9 * * *",
		Timezone: "UTC", Enabled: true, Timeout: time.Minute, CreatedAt: now, UpdatedAt: now,
		ConfigUpdatedAt: now.Add(-time.Minute), NextRunAt: now.Add(time.Hour),
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	received, found := deviceCronJob(t, deviceB, created.ID)
	if !found || !received.ForeignDevice || received.DeviceID != deviceCron(t, deviceA).Device().ID || received.Prompt != created.Prompt {
		t.Fatalf("device B job=%+v found=%v", received, found)
	}
	foreign, err := (&Service{engine: deviceB.engine}).ForeignCronJobs(ctx)
	if err != nil || len(foreign) != 1 || foreign[0].ID != created.ID {
		t.Fatalf("foreign jobs=%+v err=%v", foreign, err)
	}
	if local, _ := deviceCronJob(t, deviceA, created.ID); local.ForeignDevice {
		t.Fatal("the bound device must not treat its own job as foreign")
	}

	// A configuration change on the bound device reaches the copy.
	local, _ := deviceCronJob(t, deviceA, created.ID)
	local.Enabled, local.ConfigUpdatedAt = false, now.Add(-time.Second)
	if _, err := deviceCron(t, deviceA).CompareAndSwap(ctx, local, local.Revision); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if received, _ := deviceCronJob(t, deviceB, created.ID); received.Enabled {
		t.Fatal("the disabled job is still enabled on the other device")
	}

	// Removing the copy (for example, the bound device is gone) deletes the job everywhere.
	serviceB := &Service{engine: deviceB.engine, wakeCh: make(chan struct{}, 1)}
	if err := serviceB.ForeignCronRemove(ctx, ForeignCronRemoveRequest{ID: created.ID}); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if _, found := deviceCronJob(t, deviceA, created.ID); found {
		t.Fatal("the removed job remains on its bound device")
	}
}

// With the switch at its default (off), jobs stay on their device.
func TestCronSyncDefaultsOff(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	now := time.Now().UTC()
	if _, err := deviceCron(t, deviceA).Create(ctx, cron.Job{
		ID: "job-off", SessionID: "s", Prompt: "p", Schedule: "0 9 * * *", Timezone: "UTC", Enabled: true,
		Timeout: time.Minute, CreatedAt: now, UpdatedAt: now, NextRunAt: now.Add(time.Hour),
	}, 4); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceCronJob(t, deviceB, "job-off"); found {
		t.Fatal("a job synced while the switch is off")
	}
}
