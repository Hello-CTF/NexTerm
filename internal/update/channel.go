package update

import (
	"context"
)

// ChannelStore is the subset of the local Setting table the channel needs.
type ChannelStore interface {
	SettingGet(ctx context.Context, key string) (string, bool, error)
	SettingSet(ctx context.Context, key, value string) error
}

// StoredChannel reads the channel from the local settings. A missing row, an invalid value or a read error all
// mean stable, so a damaged setting can never opt a machine into prereleases.
func StoredChannel(ctx context.Context, store ChannelStore) ReleaseChannel {
	if store == nil {
		return ChannelStable
	}
	value, found, err := store.SettingGet(ctx, ReleaseChannelSettingKey)
	if err != nil || !found {
		return ChannelStable
	}
	return NormalizeReleaseChannel(value)
}

// StoreChannel persists a validated channel to the local settings only.
func StoreChannel(ctx context.Context, store ChannelStore, channel ReleaseChannel) error {
	if _, ok := ParseReleaseChannel(string(channel)); !ok {
		return userErrorf(UserErrorBadParam, "未知的更新通道 %q", string(channel))
	}
	return store.SettingSet(ctx, ReleaseChannelSettingKey, string(channel))
}

// ChannelProviderFor adapts a store into Manager.ChannelProvider.
func ChannelProviderFor(store ChannelStore) func(context.Context) ReleaseChannel {
	return func(ctx context.Context) ReleaseChannel { return StoredChannel(ctx, store) }
}
