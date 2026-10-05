package production

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestCronRegisterValidatesModelProfile(t *testing.T) {
	ctx := context.Background()
	services, database, _, _ := composeOutcomeRuntime(t, "echo hi")
	dispatcher := ipc.NewDispatcher()
	if err := services.cron.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	conversation, err := database.ConvCreate(ctx, "cron-profile", nil)
	if err != nil {
		t.Fatal(err)
	}
	requireCronError(t, dispatchCronTest(dispatcher, "cron_register",
		`{"sessionId":"`+conversation.ID+`","prompt":"p","schedule":"0 0 1 1 *","modelProfileId":"missing"}`), "profile")

	profile, err := services.Profiles.Save(ctx, profiles.Profile{BaseURL: "https://example.test/v1", APIKey: "k", Model: "m"})
	var saved profiles.Profile
	for _, candidate := range profile.Profiles {
		saved = candidate
	}
	body, err := json.Marshal(map[string]any{
		"sessionId": conversation.ID, "prompt": "p", "schedule": "0 0 1 1 *", "modelProfileId": saved.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var job struct {
		ModelProfileID string `json:"modelProfileId"`
	}
	requireCronData(t, dispatcher.Dispatch(ctx, ipc.Request{Command: "cron_register", Args: body}, ipc.Environment{}), &job)
	if job.ModelProfileID != saved.ID {
		t.Fatalf("job profile = %q, want %q", job.ModelProfileID, saved.ID)
	}
}

func TestAIUsageSummaryCommand(t *testing.T) {
	ctx := context.Background()
	services, database, _, _ := composeOutcomeRuntime(t, "echo hi")
	dispatcher := ipc.NewDispatcher()
	if err := registerAICommands(dispatcher, services.Profiles, database); err != nil {
		t.Fatal(err)
	}
	conversation, err := database.ConvCreate(ctx, "usage-summary", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunInsert(ctx, store.RunRow{ID: "run-summary", ConversationID: conversation.ID, Status: store.RunStatusRunning, Source: "cron", ProfileID: "profile-1"}); err != nil {
		t.Fatal(err)
	}
	if err := database.RunFinishUsage(ctx, "run-summary", store.RunStatusCompleted, "", "", 1, 60, 20, 5, 900); err != nil {
		t.Fatal(err)
	}
	var rows []store.RunUsageSummaryRow
	requireCronData(t, dispatchCronTest(dispatcher, "ai_usage_summary", `{}`), &rows)
	if len(rows) != 1 || rows[0].Source != "cron" || rows[0].ProfileID != "profile-1" || rows[0].TokensIn != 60 || rows[0].CacheCreationTokens != 5 || rows[0].AverageLatencyMS != 900 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestAIRetentionPolicyDefaultsAndOverride(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	component := newAIRetentionComponent(database, time.Hour)
	policy, err := component.policy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if policy.AIRunMaxAge != defaultAIRetentionMaxAge || policy.AIRunMaxCount != defaultAIRetentionMaxCount {
		t.Fatalf("default policy = %+v", policy)
	}
	if err := database.SettingSet(ctx, aiRetentionSettingKey, `{"maxAgeMs":86400000,"maxCount":100}`); err != nil {
		t.Fatal(err)
	}
	policy, err = component.policy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if policy.AIRunMaxAge != 24*time.Hour || policy.AIRunMaxCount != 100 {
		t.Fatalf("override policy = %+v", policy)
	}
	if err := database.SettingSet(ctx, aiRetentionSettingKey, `{"maxAgeMs":0,"maxCount":0}`); err != nil {
		t.Fatal(err)
	}
	policy, err = component.policy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if policy.AIRunMaxAge != 0 || policy.AIRunMaxCount != 0 {
		t.Fatalf("disabled policy = %+v", policy)
	}
}

func TestAIRetentionEnforcesWithoutTouchingActiveRuns(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	conversation, err := database.ConvCreate(ctx, "ai-retention", nil)
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-40 * 24 * time.Hour).UnixMilli()
	if err := database.RunInsert(ctx, store.RunRow{ID: "run-old", ConversationID: conversation.ID, Status: store.RunStatusCompleted, CreatedAt: expired, UpdatedAt: expired, FinishedAt: &expired}); err != nil {
		t.Fatal(err)
	}
	if err := database.RunInsert(ctx, store.RunRow{ID: "run-live", ConversationID: conversation.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	component := newAIRetentionComponent(database, time.Hour)
	component.enforce(ctx)
	if _, err := database.RunGet(ctx, "run-old"); err == nil {
		t.Fatal("old finished run survived default retention")
	}
	if _, err := database.RunGet(ctx, "run-live"); err != nil {
		t.Fatal("active run deleted by retention")
	}
}

func TestAIRetentionPolicyRejectsInvalidSettings(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	component := newAIRetentionComponent(database, time.Hour)
	cases := map[string]string{
		"negative age":   `{"maxAgeMs":-1,"maxCount":10}`,
		"negative count": `{"maxAgeMs":1000,"maxCount":-1}`,
		"overflow age":   `{"maxAgeMs":9223372036854775807,"maxCount":10}`,
		"malformed":      `{`,
	}
	for name, raw := range cases {
		if err := database.SettingSet(ctx, aiRetentionSettingKey, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := component.policy(ctx); err == nil {
			t.Fatalf("%s: expected configuration error, got nil", name)
		}
	}
}
