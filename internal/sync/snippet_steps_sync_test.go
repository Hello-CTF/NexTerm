package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// pushLegacyObject writes one object to the sync server the way an older
// client would, bypassing this build's collection.
func pushLegacyObject(t *testing.T, device *testDevice, server *testSyncServer, kind, id string, payload any) {
	t.Helper()
	ctx := context.Background()
	session, err := device.engine.ensureSession(ctx, device.config(server, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := marshalObject(payload)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := sealObject(mustDEK(t, server, "alice", "alice-pw-123"), plaintext, id, kind)
	if err != nil {
		t.Fatal(err)
	}
	head, err := session.client.ids(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.client.push(ctx, head.Head, []WireObject{{ID: id, Blob: blob}}); err != nil {
		t.Fatal(err)
	}
}

func legacyTemplate(id string, updatedAt int64) runbookTemplateObject {
	return runbookTemplateObject{
		ID: id, Name: "restart api", Description: "restart flow", Version: 3, CreatedAt: 1000, UpdatedAt: updatedAt,
		Steps: []runbookTemplateStepObject{
			{ID: "s1", SnippetID: ids.New(), Name: "stop", Command: "systemctl stop api"},
			{ID: "s2", Name: "start", Description: "bring back", Command: "systemctl start api", Interactive: true},
		},
	}
}

func requireNoRunbookTemplateTraffic(t *testing.T, device *testDevice) {
	t.Helper()
	objects, err := device.engine.collectLocalObjects(context.Background(), &SyncReport{}, allKindOptIn(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	for id, object := range objects {
		if object.kind == KindRunbookTemplate {
			t.Fatalf("object %s of kind runbook_template is collected for push", id)
		}
		if object.kind == KindTombstone && strings.Contains(string(object.plaintext), `"targetKind":"runbook_template"`) {
			t.Fatalf("tombstone %s targets runbook_template", id)
		}
	}
}

func TestLegacyRunbookTemplateObjectBecomesMultiStepSnippet(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	device := newTestDevice(t)
	templateID := ids.New()
	pushLegacyObject(t, device, server, KindRunbookTemplate, templateID, legacyTemplate(templateID, 2000))

	syncDevice(t, device, server, "alice", "alice-pw-123")

	snippetID := store.LegacyRunbookSnippetID(templateID)
	snippet, err := device.db.SnippetGet(ctx, snippetID)
	if err != nil {
		t.Fatalf("converted snippet missing: %v", err)
	}
	steps, err := store.DecodeSnippetSteps(snippet.Steps)
	if err != nil || len(steps) != 2 {
		t.Fatalf("steps = %+v, %v", steps, err)
	}
	if snippet.Name != "restart api" || snippet.Description != "restart flow" || snippet.CreatedAt != 1000 || snippet.UpdatedAt != 2000 ||
		snippet.Body != "systemctl stop api\nsystemctl start api" || snippet.GroupID != nil ||
		steps[1] != (store.SnippetStep{ID: "s2", Name: "start", Description: "bring back", Command: "systemctl start api", Interactive: true}) || steps[0].Confirm {
		t.Fatalf("snippet = %+v steps = %+v", snippet, steps)
	}

	// (b) A second round neither duplicates nor touches the snippet.
	report := syncDevice(t, device, server, "alice", "alice-pw-123")
	again, err := device.db.SnippetList(ctx)
	if err != nil || len(again) != 1 || again[0].UpdatedAt != 2000 || report.Applied != 0 {
		t.Fatalf("second round: %d snippets (%+v), applied=%d, %v", len(again), again, report.Applied, err)
	}
	// (c) Nothing about the legacy kind leaves this device.
	requireNoRunbookTemplateTraffic(t, device)
	tombstones, err := device.engine.syncTombstoneList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tombstone := range tombstones {
		if tombstone.Kind == KindRunbookTemplate {
			t.Fatalf("legacy tombstone recorded: %+v", tombstone)
		}
	}
}

func TestLegacyRunbookTemplateConversionKeepsLocalPlacementAndIsRebuiltByNewerTemplate(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	device := newTestDevice(t)
	templateID := ids.New()
	snippetID := store.LegacyRunbookSnippetID(templateID)
	group, err := device.db.GroupCreate(ctx, store.GroupInput{Name: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	pushLegacyObject(t, device, server, KindRunbookTemplate, templateID, legacyTemplate(templateID, 2000))
	syncDevice(t, device, server, "alice", "alice-pw-123")
	groupID := group.ID
	if err := device.db.GORM().Model(&store.SnippetRow{}).Where("id = ?", snippetID).UpdateColumns(map[string]any{"group_id": groupID, "sort": 7}).Error; err != nil {
		t.Fatal(err)
	}

	newer := legacyTemplate(templateID, 3000)
	newer.Name = "restart api v2"
	pushLegacyObject(t, device, server, KindRunbookTemplate, templateID, newer)
	syncDevice(t, device, server, "alice", "alice-pw-123")

	snippet, err := device.db.SnippetGet(ctx, snippetID)
	if err != nil {
		t.Fatal(err)
	}
	if snippet.Name != "restart api v2" || snippet.UpdatedAt != 3000 || snippet.GroupID == nil || *snippet.GroupID != groupID || snippet.Sort != 7 {
		t.Fatalf("snippet = %+v", snippet)
	}
}

func TestSnippetTombstoneBlocksLegacyRunbookTemplateConversion(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	device := newTestDevice(t)
	templateID := ids.New()
	snippetID := store.LegacyRunbookSnippetID(templateID)
	pushLegacyObject(t, device, server, KindRunbookTemplate, templateID, legacyTemplate(templateID, 2000))
	syncDevice(t, device, server, "alice", "alice-pw-123")
	if err := device.db.SnippetDelete(ctx, snippetID); err != nil {
		t.Fatal(err)
	}

	// The same template arrives again as a fresh blob; the newer tombstone wins.
	pushLegacyObject(t, device, server, KindRunbookTemplate, templateID, legacyTemplate(templateID, 2000))
	syncDevice(t, device, server, "alice", "alice-pw-123")
	if _, err := device.db.SnippetGet(ctx, snippetID); !isNotFound(err) {
		t.Fatalf("deleted snippet came back: %v", err)
	}
}

func TestOlderLegacyRunbookTemplateDoesNotOverwriteNewerSnippet(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	device := newTestDevice(t)
	templateID := ids.New()
	snippetID := store.LegacyRunbookSnippetID(templateID)
	farFuture := ids.NowMS() - 1
	if err := device.db.GORM().Create(&store.SnippetRow{
		ID: snippetID, Name: "edited locally", Body: "echo local", CreatedAt: 1, UpdatedAt: farFuture,
	}).Error; err != nil {
		t.Fatal(err)
	}
	pushLegacyObject(t, device, server, KindRunbookTemplate, templateID, legacyTemplate(templateID, 2000))
	syncDevice(t, device, server, "alice", "alice-pw-123")
	snippet, err := device.db.SnippetGet(ctx, snippetID)
	if err != nil || snippet.Name != "edited locally" || snippet.Steps != "" || snippet.UpdatedAt != farFuture {
		t.Fatalf("snippet = %+v, %v", snippet, err)
	}
}

func TestLegacyRunbookTemplateTombstoneDeletesConvertedSnippet(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	templateID := ids.New()
	snippetID := store.LegacyRunbookSnippetID(templateID)
	result := mustApplyObjects(t, instance, ApplyObject{ID: templateID, Kind: KindRunbookTemplate, Payload: applyPayload(t, legacyTemplate(templateID, 2000))})
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)

	older := mustApplyObjects(t, instance, ApplyObject{ID: templateID, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{TargetKind: KindRunbookTemplate, DeletedAt: 1500})})
	requireApplyResult(t, older.Objects[0], ApplyResultSkipped)
	if _, err := instance.db.SnippetGet(ctx, snippetID); err != nil {
		t.Fatalf("an older tombstone removed the snippet: %v", err)
	}
	newer := mustApplyObjects(t, instance, ApplyObject{ID: templateID, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{TargetKind: KindRunbookTemplate, DeletedAt: 2500})})
	requireApplyResult(t, newer.Objects[0], ApplyResultApplied)
	if _, err := instance.db.SnippetGet(ctx, snippetID); !isNotFound(err) {
		t.Fatalf("snippet survived a newer tombstone: %v", err)
	}
	tombstone, found, err := instance.service.engine.syncTombstoneGet(ctx, snippetID)
	if err != nil || !found || tombstone.Kind != KindSnippet || tombstone.DeletedAt != 2500 {
		t.Fatalf("snippet tombstone = %+v found=%v err=%v", tombstone, found, err)
	}
}

func TestMultiStepSnippetSyncsBetweenDevices(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	steps := []store.SnippetStep{
		{ID: "a", Name: "one", Command: "echo 1", Confirm: true},
		{ID: "b", Name: "two", Description: "second", Command: "echo 2", Interactive: true, WaitFor: `[$#] $`, WaitForRegex: true, TimeoutMs: 30000},
	}
	created, err := deviceA.db.SnippetCreateContent(ctx, "multi", store.SnippetContent{Description: "d", Steps: steps}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	got, err := deviceB.db.SnippetGet(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := store.DecodeSnippetSteps(got.Steps)
	if err != nil || len(decoded) != 2 || decoded[0] != steps[0] || decoded[1] != steps[1] || got.Description != "d" || got.Body != "echo 1\necho 2" {
		t.Fatalf("received = %+v steps = %+v, %v", got, decoded, err)
	}
	if report := syncDevice(t, deviceB, server, "alice", "alice-pw-123"); report.Pushed != 0 {
		t.Fatalf("identical copy pushed %d objects", report.Pushed)
	}
}

func TestOldClientSnippetPayloadWithoutStepsDegradesMultiStepSnippetToSingleStep(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	created, err := instance.db.SnippetCreateContent(ctx, "multi", store.SnippetContent{Description: "d", Steps: []store.SnippetStep{
		{ID: "a", Command: "echo 1", Confirm: true}, {ID: "b", Command: "echo 2"},
	}}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// An older client edits the snippet and pushes it back without steps.
	payload := snippetObject{ID: created.ID, Name: "renamed", Body: "echo 1\necho 2\necho 3", Sort: 0, CreatedAt: created.CreatedAt, UpdatedAt: created.UpdatedAt + 1000}
	result := mustApplyObjects(t, instance, ApplyObject{ID: created.ID, Kind: KindSnippet, Payload: applyPayload(t, payload)})
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	got, err := instance.db.SnippetGet(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps != "" || got.Body != "echo 1\necho 2\necho 3" || got.Name != "renamed" || got.Description != "" {
		t.Fatalf("degraded snippet = %+v", got)
	}
}

func TestSnippetStepsPayloadWithInvalidStepsIsSkippedWithWarning(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	created, err := instance.db.SnippetCreate(ctx, "plain", "echo", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	payload := snippetStepsObject{
		ID: store.SnippetStepsObjectID(created.ID), SnippetID: created.ID, BodyHash: snippetBodyHash("echo\ndup"), UpdatedAt: created.UpdatedAt,
		Steps: []snippetStepObject{{ID: "a", Command: "echo"}, {ID: "a", Command: "dup"}},
	}
	result := mustApplyObjects(t, instance, ApplyObject{ID: payload.ID, Kind: KindSnippetSteps, Payload: applyPayload(t, payload)})
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	if result.Objects[0].Warning == "" {
		t.Fatal("skipped companion must carry a warning")
	}
	got, err := instance.db.SnippetGet(ctx, created.ID)
	if err != nil || got.Steps != "" {
		t.Fatalf("invalid steps were written: %+v %v", got, err)
	}
}

func TestOlderClientCompanionWithoutWaitConditionsKeepsThem(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	steps := []store.SnippetStep{
		{ID: "a", Command: "ssh db"},
		{ID: "b", Name: "password", Command: "hunter2", WaitFor: "Password:", TimeoutMs: 30000},
	}
	created, err := instance.db.SnippetCreateContent(ctx, "login", store.SnippetContent{Steps: steps}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	stripped := func(updatedAt int64) snippetStepsObject {
		return snippetStepsObject{
			ID: store.SnippetStepsObjectID(created.ID), SnippetID: created.ID, BodyHash: snippetBodyHash(created.Body), UpdatedAt: updatedAt,
			Steps: []snippetStepObject{{ID: "a", Command: "ssh db"}, {ID: "b", Name: "password", Command: "hunter2"}},
		}
	}
	localSteps := func() []store.SnippetStep {
		t.Helper()
		got, err := instance.db.SnippetGet(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := store.DecodeSnippetSteps(got.Steps)
		if err != nil {
			t.Fatal(err)
		}
		return decoded
	}

	// The same version re-encoded by a client that drops the wait fields.
	same := stripped(created.UpdatedAt)
	result := mustApplyObjects(t, instance, ApplyObject{ID: same.ID, Kind: KindSnippetSteps, Payload: applyPayload(t, same)})
	requireApplyResult(t, result.Objects[0], ApplyResultIdentical)
	if got := localSteps(); !sameSnippetSteps(got, steps) {
		t.Fatalf("wait conditions were erased: %+v", got)
	}

	// A newer version is a real edit and still wins.
	newer := stripped(created.UpdatedAt + 1000)
	result = mustApplyObjects(t, instance, ApplyObject{ID: newer.ID, Kind: KindSnippetSteps, Payload: applyPayload(t, newer)})
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	if got := localSteps(); len(got) != 2 || got[1].WaitFor != "" {
		t.Fatalf("newer companion was not applied: %+v", got)
	}
}

// Plain snippets keep the exact payload bytes older clients and the browser hash, so no push storm follows an upgrade.
func TestSingleStepSnippetPayloadBytesAreUnchanged(t *testing.T) {
	group := "G1"
	encoded, err := marshalObject(snippetObject{ID: "S1", GroupID: &group, Name: "n", Body: "b", Sort: 4, CreatedAt: 5, UpdatedAt: 6})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":"S1","groupId":"G1","name":"n","body":"b","sort":4,"createdAt":5,"updatedAt":6}`
	if string(encoded) != want {
		t.Fatalf("payload = %s, want %s", encoded, want)
	}
}

func TestBundleCarriesSnippetDescriptionAndSteps(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, true)
	steps := []store.SnippetStep{
		{ID: "a", Name: "one", Command: "echo 1", Confirm: true},
		{ID: "b", Name: "two", Command: "echo 2"},
	}
	multi, err := source.db.SnippetCreateContent(ctx, "multi", store.SnippetContent{Description: "d", Steps: steps}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := source.db.SnippetCreate(ctx, "plain", "uptime", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := source.service.ExportBundle(ctx, ExportBundleRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var exportedPlain SyncBundleSnippet
	for _, snippet := range bundle.Snippets {
		if snippet.ID == plain.ID {
			exportedPlain = snippet
		}
	}
	if exportedPlain.Description != "" || len(exportedPlain.Steps) != 0 {
		t.Fatalf("plain snippet exported content: %+v", exportedPlain)
	}

	target := newTestInstance(t, true)
	if _, err := target.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle}); err != nil {
		t.Fatal(err)
	}
	got, err := target.db.SnippetGet(ctx, multi.ID)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := store.DecodeSnippetSteps(got.Steps)
	if err != nil || got.Description != "d" || len(decoded) != 2 || decoded[0] != steps[0] || decoded[1] != steps[1] {
		t.Fatalf("imported = %+v steps = %+v, %v", got, decoded, err)
	}

	// A bundle step list that breaks the bounds is refused visibly.
	badID := ids.New()
	badBundle := SyncBundle{Protocol: BundleProtocolVersion, Snippets: []SyncBundleSnippet{{
		ID: badID, Name: "bad", Steps: []snippetStepObject{{ID: "a", Command: ""}}, UpdatedAt: 5,
	}}}
	report, err := target.service.ImportBundle(ctx, ImportBundleRequest{Bundle: badBundle})
	if err != nil {
		t.Fatal(err)
	}
	if report.Refused != 1 {
		t.Fatalf("refused = %d, warnings = %v", report.Refused, report.Warnings)
	}
	if _, err := target.db.SnippetGet(ctx, badID); !isNotFound(err) {
		t.Fatalf("invalid snippet imported: %v", err)
	}
}

// The retired runbookTemplates switch no longer decides whether an older client's template becomes a snippet.
func TestLegacyRunbookTemplateConvertsWithOptInOff(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	device := newTestDevice(t)
	user, err := server.accounts.GetUserByUsername(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := device.db.SetRunbookTemplateSyncOptIn(ctx, user.ID, false); err != nil {
		t.Fatal(err)
	}
	templateID := ids.New()
	pushLegacyObject(t, device, server, KindRunbookTemplate, templateID, legacyTemplate(templateID, 2000))
	syncDevice(t, device, server, "alice", "alice-pw-123")
	if _, err := device.db.SnippetGet(ctx, store.LegacyRunbookSnippetID(templateID)); err != nil {
		t.Fatalf("template was not converted with the switch off: %v", err)
	}
}

// Owner B's template derives to an ID owner A already holds. Conversions from sync land on the fresh snippet ID
// the store resolves for B (the one the startup migration uses too), once, and never touch A's snippet.
func TestLegacyRunbookTemplateSyncConversionAvoidsCrossOwnerIDDatabase(t *testing.T) {
	instance := newDatabaseTestInstance(t, false)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	templateID := ids.New()
	derived := store.LegacyRunbookSnippetID(templateID)
	if err := instance.db.GORM().Create(&store.SnippetRow{ID: derived, Name: "foreign", Body: "echo a", OwnerID: "owner-a", CreatedAt: 1, UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	resolved, err := instance.db.ResolveRunbookTemplateSnippetID(ctxB, templateID)
	if err != nil || resolved == derived {
		t.Fatalf("resolved = %q, %v", resolved, err)
	}

	report := &SyncReport{}
	converted := legacyTemplate(templateID, 2000)
	if applied, _ := instance.service.engine.applyRunbookTemplateObject(ctxB, applyPayload(t, converted), report); !applied {
		t.Fatalf("conversion not applied: %v", report.Warnings)
	}
	newer := legacyTemplate(templateID, 3000)
	newer.Name = "restart api v2"
	if applied, _ := instance.service.engine.applyRunbookTemplateObject(ctxB, applyPayload(t, newer), report); !applied {
		t.Fatalf("newer conversion not applied: %v", report.Warnings)
	}
	after, err := instance.db.SnippetList(ctxB)
	if err != nil || len(after) != 1 || after[0].ID != resolved || after[0].Name != "restart api v2" || after[0].UpdatedAt != 3000 {
		t.Fatalf("owner-b snippets after sync = %+v, %v", after, err)
	}
	foreign, err := instance.db.SnippetList(ctxA)
	if err != nil || len(foreign) != 1 || foreign[0].Name != "foreign" || foreign[0].ID != derived {
		t.Fatalf("owner-a snippets = %+v, %v", foreign, err)
	}

	// The deletion of the template removes B's snippet and leaves A's alone.
	if applied, _ := instance.service.engine.applyRunbookTemplateTombstone(ctxB, templateID, 4000, report); !applied {
		t.Fatalf("tombstone not applied: %v", report.Warnings)
	}
	if left, err := instance.db.SnippetList(ctxB); err != nil || len(left) != 0 {
		t.Fatalf("owner-b snippets after tombstone = %+v, %v", left, err)
	}
	if foreign, err := instance.db.SnippetList(ctxA); err != nil || len(foreign) != 1 {
		t.Fatalf("owner-a snippets after tombstone = %+v, %v", foreign, err)
	}
}
