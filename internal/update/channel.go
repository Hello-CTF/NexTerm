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

// LookupChannel reads the channel like StoredChannel but reports a read error instead of assuming stable, for
// callers that would discard something on the stable channel.
func LookupChannel(ctx context.Context, store ChannelStore) (ReleaseChannel, error) {
	if store == nil {
		return ChannelStable, nil
	}
	value, found, err := store.SettingGet(ctx, ReleaseChannelSettingKey)
	if err != nil {
		return "", err
	}
	if !found {
		return ChannelStable, nil
	}
	return NormalizeReleaseChannel(value), nil
}

// ChannelLookupFor adapts a store into Manager.ChannelLookup.
func ChannelLookupFor(store ChannelStore) func(context.Context) (ReleaseChannel, error) {
	return func(ctx context.Context) (ReleaseChannel, error) { return LookupChannel(ctx, store) }
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
