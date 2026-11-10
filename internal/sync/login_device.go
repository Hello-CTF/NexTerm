package sync

import (
	"context"
	"encoding/json"
	"strings"
)

// persistedDevice is the saved login device token (setting sync.link.device, vault-encrypted). It is kept apart from the
// session envelope so logout and session expiry, which clear the session, leave it in place: the server exempts a client
// holding it from the per-username login backoff.
type persistedDevice struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

// newLoginClient builds a client for a password login, carrying the saved login device token for this link.
func (e *Engine) newLoginClient(ctx context.Context, config RemoteConfig) (*remoteClient, error) {
	client, err := newRemoteClient(config)
	if err != nil {
		return nil, err
	}
	client.deviceToken = e.loadDeviceToken(ctx, config)
	return client, nil
}

func (e *Engine) loadDeviceToken(ctx context.Context, config RemoteConfig) string {
	e.sessionMu.Lock()
	device := e.device
	e.sessionMu.Unlock()
	if device == nil && e.vault != nil && e.store != nil {
		value, found, err := e.store.SettingGet(ctx, settingLinkDevice)
		if err == nil && found && strings.TrimSpace(value) != "" {
			if plaintext, err := e.vault.DecryptSecret(ctx, value); err == nil {
				var saved persistedDevice
				if json.Unmarshal([]byte(plaintext), &saved) == nil {
					device = &saved
				}
			}
		}
	}
	if device == nil || device.URL != config.URL || device.Username != config.Username {
		return ""
	}
	return device.Token
}

// rememberDevice keeps the login device token the server issued; persist failures only log a warning.
func (e *Engine) rememberDevice(ctx context.Context, config RemoteConfig, token string) {
	if token == "" {
		return
	}
	device := &persistedDevice{URL: config.URL, Username: config.Username, Token: token}
	e.sessionMu.Lock()
	e.device = device
	e.sessionMu.Unlock()
	if e.vault == nil || e.store == nil {
		return
	}
	encoded, err := json.Marshal(device)
	if err != nil {
		return
	}
	protected, err := e.vault.EncryptSecret(ctx, string(encoded))
	if err != nil {
		e.logger.Warn("sync login device persist failed", "error", err)
		return
	}
	if err := e.store.SettingSet(ctx, settingLinkDevice, protected); err != nil {
		e.logger.Warn("sync login device persist failed", "error", err)
	}
}
