package production

import (
	"context"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

// filesSettingStore 是 setting 表的最小读写子集, 由 *store.Store 满足。
type filesSettingStore interface {
	SettingGet(ctx context.Context, key string) (string, bool, error)
	SettingSet(ctx context.Context, key, value string) error
}

type filesSettingsView struct {
	PublicBaseURL string `json:"publicBaseURL"`
}

// registerFilesCommands 注册文件链接设置的桌面 IPC。
// 校验与服务端 admin settings API 共用 core.ParsePublicBaseURL;
// 空串表示清除持久化值(回退 CLI/env 默认或同源相对链接)。
func registerFilesCommands(dispatcher *ipc.Dispatcher, settings filesSettingStore) error {
	if err := ipc.Register(dispatcher, "files_settings_get", func(ctx context.Context, _ *ipc.Call, _ struct{}) (filesSettingsView, error) {
		view := filesSettingsView{}
		value, ok, err := settings.SettingGet(ctx, core.FilePublicBaseURLSettingKey)
		if err != nil {
			return filesSettingsView{}, err
		}
		if ok {
			view.PublicBaseURL = value
		}
		return view, nil
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "files_settings_set", func(ctx context.Context, _ *ipc.Call, input filesSettingsView) (filesSettingsView, error) {
		base, err := core.ParsePublicBaseURL(input.PublicBaseURL)
		if err != nil {
			return filesSettingsView{}, ipc.BadParam(err)
		}
		if err := settings.SettingSet(ctx, core.FilePublicBaseURLSettingKey, base); err != nil {
			return filesSettingsView{}, err
		}
		return filesSettingsView{PublicBaseURL: base}, nil
	})
}
