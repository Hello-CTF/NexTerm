package sync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func multiStepFixture() []store.SnippetStep {
	return []store.SnippetStep{
		{ID: "a", Name: "one", Command: "echo 1", Confirm: true},
		{ID: "b", Name: "two", Description: "second", Command: "echo 2", Interactive: true},
	}
}

// oldClientSnippet is what a client without multi-step support pushes for a snippet row.
func oldClientSnippet(row store.SnippetRow) snippetObject {
	return snippetObject{ID: row.ID, GroupID: row.GroupID, Name: row.Name, Body: row.Body, Sort: row.Sort, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func requireSettled(t *testing.T, server *testSyncServer, devices ...*testDevice) {
	t.Helper()
	for round := 0; round < 4; round++ {
		for _, device := range devices {
			syncDevice(t, device, server, "alice", "alice-pw-123")
		}
	}
	for _, device := range devices {
		if report := syncDevice(t, device, server, "alice", "alice-pw-123"); report.Pushed != 0 {
			t.Fatalf("sync did not settle: pushed %d objects", report.Pushed)
		}
	}
}

// An older client that only decoded the original snippet fields pushes the downgraded payload back with the
// same updatedAt and no user edit. The steps must survive and both sides must stop pushing.
func TestOldClientEchoOfMultiStepSnippetKeepsSteps(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	created, err := deviceA.db.SnippetCreateContent(ctx, "multi", store.SnippetContent{Description: "d", Steps: multiStepFixture()}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	requireSettled(t, server, deviceA, deviceB)

	pushLegacyObject(t, deviceB, server, KindSnippet, created.ID, oldClientSnippet(created))
	requireSettled(t, server, deviceA, deviceB)

	for name, device := range map[string]*testDevice{"A": deviceA, "B": deviceB} {
		got, err := device.db.SnippetGet(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		steps, err := store.DecodeSnippetSteps(got.Steps)
		if err != nil || len(steps) != 2 || steps[0] != multiStepFixture()[0] || steps[1] != multiStepFixture()[1] || got.Description != "d" {
			t.Fatalf("device %s lost steps: %+v %+v %v", name, got, steps, err)
		}
	}
}

func nextMillisecond() {
	for start := ids.NowMS(); ids.NowMS() == start; {
	}
}

func requireSnippetSteps(t *testing.T, device *testDevice, id, wantBody, wantDescription string, want []store.SnippetStep) {
	t.Helper()
	got, err := device.db.SnippetGet(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := store.DecodeSnippetSteps(got.Steps)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != wantBody || got.Description != wantDescription || len(steps) != len(want) {
		t.Fatalf("snippet = %+v steps = %+v, want body %q description %q steps %+v", got, steps, wantBody, wantDescription, want)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("step %d = %+v, want %+v", i, steps[i], want[i])
		}
	}
}

// A command edit by an older client is a real change: every new device ends single-step with the older
// client's body, the stale companion on the server is ignored, and nothing keeps being pushed.
func TestOldClientCommandEditDowngradesMultiStepSnippetEverywhere(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA, deviceB, fresh := newTestDevice(t), newTestDevice(t), newTestDevice(t)
	created, err := deviceA.db.SnippetCreateContent(ctx, "multi", store.SnippetContent{Description: "d", Steps: multiStepFixture()}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	requireSettled(t, server, deviceA, deviceB)
	requireSnippetSteps(t, deviceB, created.ID, "echo 1\necho 2", "d", multiStepFixture())

	edited := oldClientSnippet(created)
	edited.Name, edited.Body, edited.UpdatedAt = "renamed", "echo 1\necho 2\necho 3", created.UpdatedAt+1000
	pushLegacyObject(t, deviceB, server, KindSnippet, created.ID, edited)
	requireSettled(t, server, deviceA, deviceB, fresh)

	for _, device := range []*testDevice{deviceA, deviceB, fresh} {
		requireSnippetSteps(t, device, created.ID, "echo 1\necho 2\necho 3", "", nil)
		got, err := device.db.SnippetGet(ctx, created.ID)
		if err != nil || got.Name != "renamed" {
			t.Fatalf("snippet = %+v %v", got, err)
		}
	}
}

// Two new devices exchange multi-step snippets, edits of the steps, the change back to a single step and the deletion.
func TestMultiStepSnippetLifecycleConvergesBetweenNewDevices(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA, deviceB := newTestDevice(t), newTestDevice(t)
	created, err := deviceA.db.SnippetCreateContent(ctx, "multi", store.SnippetContent{Description: "d", Steps: multiStepFixture()}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	requireSettled(t, server, deviceA, deviceB)
	requireSnippetSteps(t, deviceB, created.ID, "echo 1\necho 2", "d", multiStepFixture())

	// Same commands, different flags: the body does not change, the steps still follow.
	reflagged := multiStepFixture()
	reflagged[0].Confirm, reflagged[1].Interactive = false, false
	nextMillisecond()
	if err := deviceA.db.SnippetUpdateFields(ctx, created.ID, store.SnippetPatch{Name: "multi", Description: "d2", Steps: reflagged}); err != nil {
		t.Fatal(err)
	}
	requireSettled(t, server, deviceA, deviceB)
	requireSnippetSteps(t, deviceB, created.ID, "echo 1\necho 2", "d2", reflagged)
	requireSnippetSteps(t, deviceA, created.ID, "echo 1\necho 2", "d2", reflagged)

	// Different commands and a different step order and count.
	reordered := []store.SnippetStep{{ID: "c", Command: "echo 3"}, reflagged[1], reflagged[0]}
	nextMillisecond()
	if err := deviceB.db.SnippetUpdateFields(ctx, created.ID, store.SnippetPatch{Name: "multi", Description: "d2", Steps: reordered}); err != nil {
		t.Fatal(err)
	}
	requireSettled(t, server, deviceA, deviceB)
	requireSnippetSteps(t, deviceA, created.ID, "echo 3\necho 2\necho 1", "d2", reordered)

	// Back to a single step with an identical body: the change must reach the other device although the body did not move.
	body := "echo 3\necho 2\necho 1"
	nextMillisecond()
	if err := deviceA.db.SnippetUpdateFields(ctx, created.ID, store.SnippetPatch{Name: "multi", Body: body}); err != nil {
		t.Fatal(err)
	}
	requireSettled(t, server, deviceA, deviceB)
	requireSnippetSteps(t, deviceA, created.ID, body, "", nil)
	requireSnippetSteps(t, deviceB, created.ID, body, "", nil)

	// A fresh device does not resurrect steps from anything left on the server.
	fresh := newTestDevice(t)
	requireSettled(t, server, deviceA, deviceB, fresh)
	requireSnippetSteps(t, fresh, created.ID, body, "", nil)

	// Multi-step again, then deleted.
	nextMillisecond()
	if err := deviceB.db.SnippetUpdateFields(ctx, created.ID, store.SnippetPatch{Name: "multi", Description: "again", Steps: multiStepFixture()}); err != nil {
		t.Fatal(err)
	}
	requireSettled(t, server, deviceA, deviceB, fresh)
	requireSnippetSteps(t, fresh, created.ID, "echo 1\necho 2", "again", multiStepFixture())
	nextMillisecond()
	if err := deviceA.db.SnippetDelete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	requireSettled(t, server, deviceA, deviceB, fresh)
	for _, device := range []*testDevice{deviceA, deviceB, fresh} {
		if _, err := device.db.SnippetGet(ctx, created.ID); !isNotFound(err) {
			t.Fatalf("deleted snippet still present: %v", err)
		}
	}
}

func companionFor(t *testing.T, row store.SnippetRow, steps []store.SnippetStep, updatedAt int64, description string) snippetStepsObject {
	t.Helper()
	object := snippetStepsObject{
		ID: store.SnippetStepsObjectID(row.ID), SnippetID: row.ID, BodyHash: snippetBodyHash(store.SnippetStepsBody(steps)),
		Description: description, UpdatedAt: updatedAt,
	}
	for _, step := range steps {
		object.Steps = append(object.Steps, snippetStepObject(step))
	}
	return object
}

// The companion can reach a device in an earlier round than its snippet; it is retried, not dropped.
func TestSnippetStepsArrivingBeforeTheirSnippetAreAssembledLater(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	pusher, device := newTestDevice(t), newTestDevice(t)
	snippetID := ids.New()
	row := store.SnippetRow{ID: snippetID, Name: "late", Body: store.SnippetStepsBody(multiStepFixture()), CreatedAt: 1000, UpdatedAt: 2000}

	pushLegacyObject(t, pusher, server, KindSnippetSteps, store.SnippetStepsObjectID(snippetID), companionFor(t, row, multiStepFixture(), 2000, "d"))
	syncDevice(t, device, server, "alice", "alice-pw-123")
	if _, err := device.db.SnippetGet(ctx, snippetID); !isNotFound(err) {
		t.Fatalf("companion alone created a snippet: %v", err)
	}

	pushLegacyObject(t, pusher, server, KindSnippet, snippetID, oldClientSnippet(row))
	requireSettled(t, server, device)
	requireSnippetSteps(t, device, snippetID, row.Body, "d", multiStepFixture())

	// The same with an older local version of the snippet: its companion belongs to the newer body.
	other := newTestDevice(t)
	older := row
	older.Body, older.UpdatedAt = "echo old", 1500
	if err := other.db.GORM().Create(&older).Error; err != nil {
		t.Fatal(err)
	}
	requireSettled(t, server, other)
	requireSnippetSteps(t, other, snippetID, row.Body, "d", multiStepFixture())
}

// A companion for a body the snippet no longer has (and that is not newer) never writes steps.
func TestSnippetStepsForAnotherBodyAreIgnored(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	created, err := instance.db.SnippetCreate(ctx, "plain", "echo changed", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	stale := companionFor(t, created, multiStepFixture(), created.UpdatedAt-1, "d")
	result := mustApplyObjects(t, instance, ApplyObject{ID: stale.ID, Kind: KindSnippetSteps, Payload: applyPayload(t, stale)})
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	if result.Objects[0].Warning != "" {
		t.Fatalf("an ignorable companion must not warn: %q", result.Objects[0].Warning)
	}
	got, err := instance.db.SnippetGet(ctx, created.ID)
	if err != nil || got.Steps != "" || got.Body != "echo changed" {
		t.Fatalf("snippet = %+v %v", got, err)
	}

	// For a snippet that has not arrived the browser is asked to retry.
	missing := store.SnippetRow{ID: ids.New()}
	pending := companionFor(t, missing, multiStepFixture(), 5, "")
	result = mustApplyObjects(t, instance, ApplyObject{ID: pending.ID, Kind: KindSnippetSteps, Payload: applyPayload(t, pending)})
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	if result.Objects[0].Warning == "" {
		t.Fatal("a companion without its snippet must ask for a retry")
	}
}

// Multi-step snippets leave this build as the original snippet fields plus a companion; the bytes are fixed.
func TestMultiStepSnippetCollectsLegacyPayloadAndCompanion(t *testing.T) {
	ctx := context.Background()
	device := newTestDevice(t)
	multi, err := device.db.SnippetCreateContent(ctx, "multi", store.SnippetContent{Description: "d", Steps: multiStepFixture()}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := device.db.SnippetCreate(ctx, "plain", "uptime", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := device.engine.collectLocalObjects(ctx, &SyncReport{}, allKindOptIn(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := marshalObject(oldClientSnippet(multi))
	if err != nil {
		t.Fatal(err)
	}
	if got := objects[multi.ID]; got.kind != KindSnippet || string(got.plaintext) != string(legacy) {
		t.Fatalf("multi-step snippet payload = %s (%s), want %s", got.plaintext, got.kind, legacy)
	}
	companionID := store.SnippetStepsObjectID(multi.ID)
	if !ids.Valid(companionID) || companionID == multi.ID {
		t.Fatalf("companion id %q", companionID)
	}
	if got := objects[companionID]; got.kind != KindSnippetSteps {
		t.Fatalf("companion missing: %+v", got)
	}
	if _, found := objects[store.SnippetStepsObjectID(plain.ID)]; found {
		t.Fatal("a single-step snippet produced a companion")
	}
}

// An older build cannot read the new kind at all (the kind is bound into the encryption), so it quarantines the
// blob instead of applying or re-pushing anything; a build that does know the kind skips unregistered kinds.
func TestOlderBuildsCannotOpenSnippetStepsObjects(t *testing.T) {
	dek := make([]byte, 32)
	id := store.SnippetStepsObjectID(ids.New())
	blob, err := sealObject(dek, []byte(`{}`), id, KindSnippetSteps)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range objectKinds {
		if kind == KindSnippetSteps {
			continue
		}
		if _, err := openObject(dek, blob, id, kind); err == nil {
			t.Fatalf("a build that only knows %s opened a snippet_steps object", kind)
		}
	}
	if applied, identical := (&Engine{}).applyDecryptedObject(context.Background(), id, "kind_from_the_future", []byte(`{}`), &SyncReport{}); applied || identical {
		t.Fatal("an unregistered kind was applied")
	}
}

func TestSnippetStepsGoldenPayloads(t *testing.T) {
	row := store.SnippetRow{ID: "01M6G627NENBMSX21C095HZQ6E", Name: "deploy <all> & more", CreatedAt: 1000, UpdatedAt: 2000}
	steps := []store.SnippetStep{
		{ID: "a", Name: "one", Command: "echo '<a>' && echo \"&\"", Confirm: true},
		{ID: "b", Name: "two", Description: "line sep", Command: "echo 2", Interactive: true},
	}
	row.Body = store.SnippetStepsBody(steps)
	snippetPayload, err := json.Marshal(snippetObjectFromRow(row))
	if err != nil {
		t.Fatal(err)
	}
	companionPayload, err := json.Marshal(companionFor(t, row, steps, row.UpdatedAt, "desc <&>  "))
	if err != nil {
		t.Fatal(err)
	}
	encode := func(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }
	want, err := json.MarshalIndent(map[string]string{"snippet": encode(snippetPayload), "snippetSteps": encode(companionPayload)}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join("testdata", "snippet_multistep_payloads.json")
	if *updateGolden {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update-sync-golden to create it)", err)
	}
	if string(got) != string(want) {
		t.Fatalf("golden payloads changed:\n got %s\nwant %s", got, want)
	}
}
