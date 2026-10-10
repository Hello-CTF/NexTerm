package subagent_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/subagent"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type fakeSettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *fakeSettings) SettingGet(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.values[key]
	return value, found, nil
}

func (s *fakeSettings) SettingSet(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *fakeSettings) SecretProtector() store.SecretProtector {
	return stubProtector{}
}

type stubProtector struct{}

func (stubProtector) EncryptSecret(_ context.Context, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	return store.SecretEnvelopePrefix + base64.StdEncoding.EncodeToString([]byte(plaintext)), nil
}

func (stubProtector) DecryptSecret(_ context.Context, envelope string) (string, error) {
	if !strings.HasPrefix(envelope, store.SecretEnvelopePrefix) {
		return "", errors.New("not a secret envelope")
	}
	raw, err := base64.StdEncoding.DecodeString(envelope[len(store.SecretEnvelopePrefix):])
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func newFakeSettings() *fakeSettings {
	return &fakeSettings{values: map[string]string{}}
}

func TestProfileModelFactory(t *testing.T) {
	ctx := context.Background()
	factory := subagent.NewProfileModelFactory(nil)
	if _, err := factory(ctx); err == nil {
		t.Fatal("nil profile manager must fail")
	}
	manager, err := profiles.NewManager(ctx, newFakeSettings())
	if err != nil {
		t.Fatal(err)
	}
	factory = subagent.NewProfileModelFactory(manager)
	if _, err := factory(ctx); err == nil || !strings.Contains(err.Error(), "未配置活动 AI 模型") {
		t.Fatalf("missing active profile error = %v", err)
	}
	if _, err := manager.Save(ctx, profiles.Profile{BaseURL: "http://127.0.0.1:1/v1", APIKey: "key", Model: "model", Stream: false}); err != nil {
		t.Fatal(err)
	}
	chatModel, err := factory(ctx)
	if err != nil {
		t.Fatalf("active profile did not resolve a chat model: %v", err)
	}
	if chatModel == nil {
		t.Fatal("profile factory returned a nil chat model")
	}
}
