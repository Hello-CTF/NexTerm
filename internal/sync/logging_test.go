package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

func newLogTestHandler(t *testing.T, buf *bytes.Buffer) (*ObjectHandler, context.Context, string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user, err := account.New(db.GORM()).CreateUser(ctx, "log-test-"+ids.New(), "test", "handler-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	return NewObjectHandler(db.GORM(), db.Backend(), captureLogger(buf)), WithUserID(ctx, user.ID), user.ID
}

func postSync(t *testing.T, handler *ObjectHandler, ctx context.Context, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestObjectHandlerSyncDataPlaneLogging(t *testing.T) {
	var buf bytes.Buffer
	handler, ctx, userID := newLogTestHandler(t, &buf)

	// Empty pull: an empty round stays silent.
	response := postSync(t, handler, ctx, "/sync/v2/pull", PullRequest{Protocol: ProtocolVersion, SinceSeq: 0})
	if response.Code != http.StatusOK {
		t.Fatalf("empty pull status=%d", response.Code)
	}
	if buf.Len() != 0 {
		t.Fatalf("empty pull must stay silent, got %q", buf.String())
	}

	// An unauthenticated 401 is not logged.
	request := httptest.NewRequest(http.MethodPost, "/sync/v2/pull", bytes.NewReader([]byte(`{"protocol":2}`)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || buf.Len() != 0 {
		t.Fatalf("unauthenticated pull: status=%d logs=%q", recorder.Code, buf.String())
	}

	response = postSync(t, handler, ctx, "/sync/v2/push", PushRequest{
		Protocol: ProtocolVersion, KnownHead: genesisHead(userID), Objects: []WireObject{{ID: "obj-1", Blob: []byte("x")}},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("push status=%d body=%s", response.Code, response.Body.String())
	}
	var pushed PushResponse
	if err := json.Unmarshal(response.Body.Bytes(), &pushed); err != nil {
		t.Fatal(err)
	}
	line := buf.String()
	for _, want := range []string{`level=INFO msg="sync push"`, "user=" + userID, "received=1", "applied=1", "skipped=0", "max_seq=1"} {
		if !strings.Contains(line, want) {
			t.Fatalf("push log missing %q: %q", want, line)
		}
	}

	buf.Reset()
	response = postSync(t, handler, ctx, "/sync/v2/pull", PullRequest{Protocol: ProtocolVersion, SinceSeq: 0})
	if response.Code != http.StatusOK {
		t.Fatalf("pull status=%d", response.Code)
	}
	line = buf.String()
	for _, want := range []string{`level=INFO msg="sync pull"`, "user=" + userID, "since_seq=0", "returned=1", "next_seq=1", "cursor_done=true"} {
		if !strings.Contains(line, want) {
			t.Fatalf("pull log missing %q: %q", want, line)
		}
	}

	// Empty pages are not logged.
	buf.Reset()
	response = postSync(t, handler, ctx, "/sync/v2/pull", PullRequest{Protocol: ProtocolVersion, SinceSeq: pushed.MaxSeq})
	if response.Code != http.StatusOK {
		t.Fatalf("empty page pull status=%d", response.Code)
	}
	if strings.Contains(buf.String(), "sync pull") {
		t.Fatalf("empty pull page must stay silent, got %q", buf.String())
	}

	// ids unchanged is not logged; only changes are logged.
	buf.Reset()
	response = postSync(t, handler, ctx, "/sync/v2/ids", IDsRequest{Protocol: ProtocolVersion, KnownHead: pushed.Head})
	if response.Code != http.StatusOK {
		t.Fatalf("ids unchanged status=%d", response.Code)
	}
	if strings.Contains(buf.String(), "sync ids") {
		t.Fatalf("unchanged ids must stay silent, got %q", buf.String())
	}
	buf.Reset()
	response = postSync(t, handler, ctx, "/sync/v2/ids", IDsRequest{Protocol: ProtocolVersion})
	if response.Code != http.StatusOK {
		t.Fatalf("ids status=%d", response.Code)
	}
	line = buf.String()
	for _, want := range []string{`level=INFO msg="sync ids"`, "user=" + userID, "entries=1", "max_seq=1"} {
		if !strings.Contains(line, want) {
			t.Fatalf("ids log missing %q: %q", want, line)
		}
	}

	// Head conflict logs Warn with known_head keeping only the first 8 characters.
	buf.Reset()
	staleHead := "stale-head-1234567890abcdef"
	response = postSync(t, handler, ctx, "/sync/v2/push", PushRequest{
		Protocol: ProtocolVersion, KnownHead: staleHead, Objects: []WireObject{{ID: "obj-2", Blob: []byte("y")}},
	})
	if response.Code != http.StatusConflict {
		t.Fatalf("stale head push status=%d", response.Code)
	}
	line = buf.String()
	if !strings.Contains(line, `level=WARN msg="sync push head conflict"`) || !strings.Contains(line, "known_head=stale-he") {
		t.Fatalf("head conflict log: %q", line)
	}
	if strings.Contains(line, staleHead) {
		t.Fatalf("full known_head must not be logged: %q", line)
	}
}

func TestApplyObjectsLogging(t *testing.T) {
	var buf bytes.Buffer
	instance := newTestInstance(t, false, WithLogger(captureLogger(&buf)))
	ctx := context.Background()

	result, err := instance.service.ApplyObjects(ctx, ApplyObjectsRequest{Objects: []ApplyObject{
		applyGroupObject(t, ids.New(), "日志分组", 100),
	}})
	if err != nil || result.Applied != 1 {
		t.Fatalf("apply: %+v %v", result, err)
	}
	line := buf.String()
	for _, want := range []string{`level=INFO msg="sync apply"`, "received=1", "applied=1", "identical=0", "skipped=0"} {
		if !strings.Contains(line, want) {
			t.Fatalf("apply log missing %q: %q", want, line)
		}
	}

	buf.Reset()
	result, err = instance.service.ApplyObjects(ctx, ApplyObjectsRequest{Objects: []ApplyObject{
		{ID: ids.New(), Kind: "bogus", Payload: []byte(`{"id":"x"}`)},
	}})
	if err != nil || result.Skipped != 1 {
		t.Fatalf("apply skipped: %+v %v", result, err)
	}
	line = buf.String()
	if !strings.Contains(line, `level=WARN msg="sync apply"`) || !strings.Contains(line, "skipped=1") || !strings.Contains(line, "kinds=map[bogus:1]") {
		t.Fatalf("apply skipped log: %q", line)
	}

	buf.Reset()
	if _, err := instance.service.ApplyObjects(ctx, ApplyObjectsRequest{}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("empty apply must stay silent, got %q", buf.String())
	}
}

func TestEngineSyncRoundSummaryLogging(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	var buf bytes.Buffer
	device := newTestDevice(t)
	device.engine = NewEngine(device.db, device.vault, captureLogger(&buf))

	putDeviceGroup(t, device, ids.New(), nil, "日志分组", 100)
	syncDevice(t, device, server, "alice", "alice-pw-123")
	line := buf.String()
	for _, want := range []string{`level=INFO msg="sync round complete"`, "user=", "pulled=1", "applied=0", "skipped=1", "pushed=1", "conflicts=0", "warnings=0"} {
		if !strings.Contains(line, want) {
			t.Fatalf("round summary missing %q: %q", want, line)
		}
	}

	// Empty rounds stay silent.
	buf.Reset()
	syncDevice(t, device, server, "alice", "alice-pw-123")
	if strings.Contains(buf.String(), "sync round") {
		t.Fatalf("idle round must stay silent, got %q", buf.String())
	}

	// Failures log Warn with error.
	buf.Reset()
	server.Server.Close()
	if _, err := device.engine.Sync(context.Background(), device.config(server, "alice", "alice-pw-123")); err == nil {
		t.Fatal("expected sync error after server close")
	}
	line = buf.String()
	if !strings.Contains(line, `level=WARN msg="sync round failed"`) || !strings.Contains(line, "error=") {
		t.Fatalf("round failure log: %q", line)
	}
}

func TestEngineSyncRoundWarningsLogging(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	assetID := ids.New()
	putDeviceAsset(t, deviceA, store.AssetRow{ID: assetID, Kind: "ssh", Name: "旧密钥资产", CreatedAt: 100, UpdatedAt: 100})
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	resetDEK(t, server, "alice", "alice-pw-123")

	var buf bytes.Buffer
	deviceB := newTestDevice(t)
	deviceB.engine = NewEngine(deviceB.db, deviceB.vault, captureLogger(&buf))
	report, err := deviceB.engine.Sync(context.Background(), deviceB.config(server, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatal(err)
	}
	if report.DecryptFailed == 0 || len(report.Warnings) == 0 {
		t.Fatalf("expected quarantine round, report=%+v", report)
	}
	line := buf.String()
	if !strings.Contains(line, `level=WARN msg="sync round warnings"`) || !strings.Contains(line, "warnings=1") {
		t.Fatalf("round warnings log: %q", line)
	}
	if strings.Contains(line, assetID) {
		t.Fatalf("object ID must not be logged: %q", line)
	}
}

func TestServiceWithLoggerInjection(t *testing.T) {
	var buf bytes.Buffer
	logger := captureLogger(&buf)
	instance := newTestInstance(t, false, WithLogger(logger))
	if instance.service.engine.logger != logger {
		t.Fatal("WithLogger did not reach the engine")
	}
	if instance.service.ObjectHandler().logger != logger {
		t.Fatal("WithLogger did not reach the object handler")
	}
	fallback := newTestInstance(t, false)
	if fallback.service.engine.logger != slog.Default() {
		t.Fatal("missing WithLogger must fall back to slog.Default")
	}
}

func TestServiceSyncRoundStatusTriple(t *testing.T) {
	instance := newTestInstance(t, true)
	server := newTestSyncServer(t)
	ctx := context.Background()
	server.createUser(t, "alice", "alice-password")
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr("alice-password"),
	}); err != nil {
		t.Fatal(err)
	}
	before, err := instance.service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !before.LastAttemptAt.IsZero() || !before.LastSuccessAt.IsZero() {
		t.Fatalf("triple must be zero before any sync: %+v", before)
	}
	if _, err := instance.service.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	succeeded, err := instance.service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if succeeded.LastAttemptAt.IsZero() || succeeded.LastSuccessAt.IsZero() || succeeded.LastError != "" {
		t.Fatalf("triple after success: %+v", succeeded)
	}

	server.Server.Close()
	if _, err := instance.service.Sync(ctx); err == nil {
		t.Fatal("expected sync error after server close")
	}
	failed, err := instance.service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if failed.LastError == "" {
		t.Fatalf("triple must carry the sync error: %+v", failed)
	}
	if !failed.LastSuccessAt.Equal(succeeded.LastSuccessAt) {
		t.Fatalf("LastSuccessAt moved on failure: %+v -> %+v", succeeded, failed)
	}
	if failed.LastAttemptAt.Before(succeeded.LastAttemptAt) {
		t.Fatalf("LastAttemptAt went backwards: %+v -> %+v", succeeded, failed)
	}
}
