package update

import (
	"context"
	"errors"
	"testing"
)

type memoryChannelStore struct {
	values map[string]string
	getErr error
}

func (s *memoryChannelStore) SettingGet(_ context.Context, key string) (string, bool, error) {
	if s.getErr != nil {
		return "", false, s.getErr
	}
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *memoryChannelStore) SettingSet(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func TestStoredChannelFallsBackToStable(t *testing.T) {
	ctx := context.Background()
	store := &memoryChannelStore{values: map[string]string{}}
	if StoredChannel(ctx, store) != ChannelStable || StoredChannel(ctx, nil) != ChannelStable {
		t.Fatal("missing value must mean stable")
	}
	store.values[ReleaseChannelSettingKey] = "rc"
	if StoredChannel(ctx, store) != ChannelRC {
		t.Fatal("rc must be read back")
	}
	store.values[ReleaseChannelSettingKey] = "beta"
	if StoredChannel(ctx, store) != ChannelStable {
		t.Fatal("invalid value must mean stable")
	}
	store.values[ReleaseChannelSettingKey] = "rc"
	store.getErr = errors.New("db down")
	if StoredChannel(ctx, store) != ChannelStable {
		t.Fatal("read error must mean stable")
	}
}

func TestStoreChannelRejectsInvalid(t *testing.T) {
	store := &memoryChannelStore{values: map[string]string{}}
	if err := StoreChannel(context.Background(), store, "beta"); err == nil {
		t.Fatal("invalid channel must be rejected")
	}
	if len(store.values) != 0 {
		t.Fatalf("invalid channel was stored: %v", store.values)
	}
}

func TestManagerFollowsStoredChannelImmediately(t *testing.T) {
	ctx := context.Background()
	store := &memoryChannelStore{values: map[string]string{}}
	fixture := rcFixture(t, "1.4.0-rc.1")
	server := fixture.server(t)
	manager := &Manager{
		CurrentVersion: "1.3.6", GOOS: "linux", GOARCH: "amd64", APIURL: server.URL + "/releases",
		ChannelProvider: ChannelProviderFor(store),
	}
	if manager.Check(ctx).Available {
		t.Fatal("default stable must hide the rc")
	}
	if err := StoreChannel(ctx, store, ChannelRC); err != nil {
		t.Fatal(err)
	}
	if !manager.Check(ctx).Available {
		t.Fatal("saving rc must apply to the next check")
	}
}
