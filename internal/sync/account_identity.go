package sync

import (
	"context"
	"encoding/json"
)

// settingLinkAccount records the account id of the last session established for the saved link, so account-scoped
// local data (preferences) stays readable and writable before the next session is restored after a restart.
const settingLinkAccount = "sync.link.account"

type linkAccount struct {
	Key    string `json:"key"`
	UserID string `json:"userId"`
}

func (e *Engine) rememberAccount(ctx context.Context, key, userID string) {
	if e.store == nil || key == "" || userID == "" {
		return
	}
	encoded, err := json.Marshal(linkAccount{Key: key, UserID: userID})
	if err != nil {
		return
	}
	if current, found, err := e.store.SettingGet(ctx, settingLinkAccount); err == nil && found && current == string(encoded) {
		return
	}
	if err := e.store.SettingSet(ctx, settingLinkAccount, string(encoded)); err != nil {
		e.logger.Warn("sync account id persist failed", "error", err)
	}
}

// rememberedAccount returns the account id recorded for the saved link, or "" when the link changed since, is not
// configured or cannot be read (a locked vault).
func (s *Service) rememberedAccount(ctx context.Context) string {
	raw, found, err := s.store.SettingGet(ctx, settingLinkAccount)
	if err != nil || !found {
		return ""
	}
	var account linkAccount
	if json.Unmarshal([]byte(raw), &account) != nil || account.UserID == "" {
		return ""
	}
	link, err := s.LinkGet(ctx)
	if err != nil || link.URL == "" || link.Username == "" || account.Key != link.URL+"\x00"+link.Username {
		return ""
	}
	return account.UserID
}
