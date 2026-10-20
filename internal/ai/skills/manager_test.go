package skills_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type memorySettings struct {
	mu     sync.Mutex
	values map[string]string
}

func newMemorySettings() *memorySettings {
	return &memorySettings{values: map[string]string{}}
}

func (m *memorySettings) SettingGet(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.values[key]
	return value, ok, nil
}

func (m *memorySettings) SettingSet(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value
	return nil
}

func (m *memorySettings) seed(key, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value
}

func (m *memorySettings) value(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.values[key]
}

func testIndex(t *testing.T) *skills.Index {
	t.Helper()
	index, err := skills.BuildIndex(syntheticFS())
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func stringPointer(value string) *string { return &value }

func TestManagerDispatchPinsDigest(t *testing.T) {
	manager, err := skills.NewManager(t.Context(), newMemorySettings(), skills.WithIndex(testIndex(t)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	first, err := manager.Dispatch(ctx, "alpha-skill")
	if err != nil {
		t.Fatal(err)
	}
	if first.Body != "alpha body\n" || first.Description != "Alpha synthetic skill" || first.Source != skills.SourceEmbedded {
		t.Fatalf("dispatch = %+v", first)
	}
	if !strings.HasPrefix(first.Digest, "sha256:") {
		t.Fatalf("digest = %q", first.Digest)
	}
	second, err := manager.Dispatch(ctx, "alpha-skill")
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatal("unchanged skill produced different digests")
	}
	if _, err := manager.Dispatch(ctx, "missing-skill"); !errors.Is(err, skills.ErrSkillNotFound) {
		t.Fatalf("err = %v, want ErrSkillNotFound", err)
	}
}

func TestManagerOverrideCRUD(t *testing.T) {
	settings := newMemorySettings()
	manager, err := skills.NewManager(t.Context(), settings, skills.WithIndex(testIndex(t)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	baseline, err := manager.Dispatch(ctx, "alpha-skill")
	if err != nil {
		t.Fatal(err)
	}

	entry, err := manager.SaveOverride(ctx, skills.OverrideInput{Name: "alpha-skill", Description: stringPointer("Alpha overridden")})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Overridden || entry.Source != skills.SourceEmbedded || entry.UpdatedAt == 0 {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Digest == baseline.Digest {
		t.Fatal("override did not change the effective digest")
	}
	dispatch, err := manager.Dispatch(ctx, "alpha-skill")
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Description != "Alpha overridden" || dispatch.Body != "alpha body\n" {
		t.Fatalf("partial override must inherit the embedded body: %+v", dispatch)
	}
	if dispatch.Digest != entry.Digest {
		t.Fatalf("dispatch digest %q != entry digest %q", dispatch.Digest, entry.Digest)
	}

	if _, err := manager.SaveOverride(ctx, skills.OverrideInput{Name: "alpha-skill", Body: stringPointer("replaced body\n")}); err != nil {
		t.Fatal(err)
	}
	dispatch, err = manager.Dispatch(ctx, "alpha-skill")
	if err != nil {
		t.Fatal(err)
	}
	// The second save replaces the first override wholesale.
	if dispatch.Description != "Alpha synthetic skill" || dispatch.Body != "replaced body\n" {
		t.Fatalf("dispatch = %+v", dispatch)
	}

	if _, err := manager.SaveOverride(ctx, skills.OverrideInput{Name: "alpha-skill", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Dispatch(ctx, "alpha-skill"); !errors.Is(err, skills.ErrSkillDisabled) {
		t.Fatalf("err = %v, want ErrSkillDisabled", err)
	}
	entries, err := manager.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "alpha-skill" || !entries[0].Disabled {
		t.Fatalf("disabled skill must stay visible in List: %+v", entries)
	}

	if err := manager.DeleteOverride(ctx, "alpha-skill"); err != nil {
		t.Fatal(err)
	}
	restored, err := manager.Dispatch(ctx, "alpha-skill")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Digest != baseline.Digest {
		t.Fatal("delete did not revert to the embedded content")
	}
	if err := manager.DeleteOverride(ctx, "alpha-skill"); !errors.Is(err, skills.ErrSkillNotFound) {
		t.Fatalf("err = %v, want ErrSkillNotFound", err)
	}
}

func TestManagerOverrideValidation(t *testing.T) {
	manager, err := skills.NewManager(t.Context(), newMemorySettings(), skills.WithIndex(testIndex(t)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	cases := map[string]skills.OverrideInput{
		"invalid name":        {Name: "Bad_Name"},
		"empty override":      {Name: "alpha-skill"},
		"blank description":   {Name: "alpha-skill", Description: stringPointer("   ")},
		"blank body":          {Name: "alpha-skill", Body: stringPointer("\n")},
		"custom missing body": {Name: "custom-skill", Description: stringPointer("desc")},
		"custom missing desc": {Name: "custom-skill", Body: stringPointer("body")},
	}
	for label, input := range cases {
		if _, err := manager.SaveOverride(ctx, input); !errors.Is(err, skills.ErrInvalidSkill) {
			t.Errorf("%s: err = %v, want ErrInvalidSkill", label, err)
		}
	}
}

func TestManagerCustomSkillLifecycle(t *testing.T) {
	settings := newMemorySettings()
	manager, err := skills.NewManager(t.Context(), settings, skills.WithIndex(testIndex(t)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	entry, err := manager.SaveOverride(ctx, skills.OverrideInput{
		Name: "custom-skill", Description: stringPointer("Custom skill"), Body: stringPointer("custom body\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Source != skills.SourceCustom || entry.Overridden {
		t.Fatalf("entry = %+v", entry)
	}
	dispatch, err := manager.Dispatch(ctx, "custom-skill")
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Source != skills.SourceCustom || dispatch.Body != "custom body\n" {
		t.Fatalf("dispatch = %+v", dispatch)
	}

	reopened, err := skills.NewManager(t.Context(), settings, skills.WithIndex(testIndex(t)))
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err = reopened.Dispatch(ctx, "custom-skill")
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Body != "custom body\n" {
		t.Fatalf("custom skill did not survive reopen: %+v", dispatch)
	}

	if err := reopened.DeleteOverride(ctx, "custom-skill"); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Dispatch(ctx, "custom-skill"); !errors.Is(err, skills.ErrSkillNotFound) {
		t.Fatalf("err = %v, want ErrSkillNotFound", err)
	}
}

func TestManagerOwnerIsolation(t *testing.T) {
	settings := newMemorySettings()
	manager, err := skills.NewManager(t.Context(), settings, skills.WithIndex(testIndex(t)), skills.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctxA := ipc.WithUserID(t.Context(), "owner-a")
	ctxB := ipc.WithUserID(t.Context(), "owner-b")
	if _, err := manager.SaveOverride(ctxA, skills.OverrideInput{Name: "alpha-skill", Body: stringPointer("body from A\n")}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveOverride(ctxB, skills.OverrideInput{Name: "alpha-skill", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	dispatchA, err := manager.Dispatch(ctxA, "alpha-skill")
	if err != nil {
		t.Fatal(err)
	}
	if dispatchA.Body != "body from A\n" {
		t.Fatalf("owner A dispatch = %+v", dispatchA)
	}
	if _, err := manager.Dispatch(ctxB, "alpha-skill"); !errors.Is(err, skills.ErrSkillDisabled) {
		t.Fatalf("owner B dispatch err = %v, want ErrSkillDisabled", err)
	}
	entriesB, err := manager.List(ctxB)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesB) != 2 || !entriesB[0].Disabled {
		t.Fatalf("owner B entries = %+v", entriesB)
	}
	if _, err := manager.Dispatch(t.Context(), "alpha-skill"); err == nil {
		t.Fatal("ownerless server request dispatched shared skills")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
}

func requireIPCCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("err = %v, want ipc code %s", err, code)
	}
}

func TestManagerSharedStateIsSeparateFromOwnerState(t *testing.T) {
	settings := newMemorySettings()
	shared, err := skills.NewManager(t.Context(), settings, skills.WithIndex(testIndex(t)), skills.WithShared(true))
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := skills.NewManager(t.Context(), settings, skills.WithIndex(testIndex(t)), skills.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := shared.SaveOverride(ctx, skills.OverrideInput{Name: "alpha-skill", Body: stringPointer("shared body\n")}); err != nil {
		t.Fatal(err)
	}
	dispatch, err := shared.Dispatch(ctx, "alpha-skill")
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Body != "shared body\n" {
		t.Fatalf("shared dispatch = %+v", dispatch)
	}
	ownerDispatch, err := scoped.Dispatch(ipc.WithUserID(ctx, "owner-a"), "alpha-skill")
	if err != nil {
		t.Fatal(err)
	}
	if ownerDispatch.Body != "alpha body\n" {
		t.Fatalf("shared override leaked into owner state: %+v", ownerDispatch)
	}
}

func TestManagerCorruptSettingsAreQuarantined(t *testing.T) {
	settings := newMemorySettings()
	settings.seed(skills.SettingKey, "{not json")
	manager, err := skills.NewManager(t.Context(), settings, skills.WithIndex(testIndex(t)))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := manager.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	var persisted struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal([]byte(settings.value(skills.SettingKey)), &persisted); err != nil || persisted.Version != 1 {
		t.Fatalf("quarantined state was not rewritten: %q err=%v", settings.value(skills.SettingKey), err)
	}
}

func TestManagerRejectsUnsupportedStoreVersion(t *testing.T) {
	settings := newMemorySettings()
	settings.seed(skills.SettingKey, `{"version":99,"overrides":[]}`)
	if _, err := skills.NewManager(t.Context(), settings, skills.WithIndex(testIndex(t))); err == nil || !strings.Contains(err.Error(), "unsupported AI skill store version") {
		t.Fatalf("err = %v", err)
	}
}

func TestManagerDropsPoisonedOverrideEntries(t *testing.T) {
	settings := newMemorySettings()
	settings.seed(skills.SettingKeyForUser("owner-a"), `{"version":1,"overrides":[
		{"name":"BAD NAME","body":"x"},
		{"name":"alpha-skill","body":"first body\n"},
		{"name":"alpha-skill","body":"second body\n"}
	]}`)
	manager, err := skills.NewManager(t.Context(), settings, skills.WithIndex(testIndex(t)), skills.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	dispatch, err := manager.Dispatch(ctx, "alpha-skill")
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Body != "first body\n" {
		t.Fatalf("poisoned store must keep the first entry per name: %+v", dispatch)
	}
	var persisted struct {
		Overrides []struct {
			Name string `json:"name"`
		} `json:"overrides"`
	}
	if err := json.Unmarshal([]byte(settings.value(skills.SettingKeyForUser("owner-a"))), &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Overrides) != 1 || persisted.Overrides[0].Name != "alpha-skill" {
		t.Fatalf("normalized store = %+v", persisted)
	}
}

func TestManagerConcurrentOwnerIsolation(t *testing.T) {
	settings := newMemorySettings()
	manager, err := skills.NewManager(t.Context(), settings, skills.WithIndex(testIndex(t)), skills.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctxA := ipc.WithUserID(t.Context(), "owner-a")
	ctxB := ipc.WithUserID(t.Context(), "owner-b")
	bodies := map[context.Context]string{ctxA: "body-a\n", ctxB: "body-b\n"}
	var wait sync.WaitGroup
	for ctx, body := range bodies {
		ctx, body := ctx, body
		for worker := 0; worker < 4; worker++ {
			wait.Add(1)
			go func() {
				defer wait.Done()
				for round := 0; round < 12; round++ {
					if _, err := manager.SaveOverride(ctx, skills.OverrideInput{Name: "alpha-skill", Body: stringPointer(body)}); err != nil {
						t.Errorf("save: %v", err)
						return
					}
					dispatch, err := manager.Dispatch(ctx, "alpha-skill")
					if err != nil || dispatch.Body != body {
						t.Errorf("dispatch = %+v, %v", dispatch, err)
						return
					}
					entries, err := manager.List(ctx)
					if err != nil || len(entries) != 2 {
						t.Errorf("list = %+v, %v", entries, err)
						return
					}
				}
			}()
		}
	}
	wait.Wait()
}
