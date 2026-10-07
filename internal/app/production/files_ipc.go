package production

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

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

// filesImageSaveMaxBytes 与 internal/server 的 DefaultImageMaxBytes 对齐:
// 本地回退保存走 JSON IPC, 同样按单图上限拒绝, 避免 oversized 载荷拖垮桌面端。
const filesImageSaveMaxBytes = 20 << 20

type filesSaveImageInput struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"contentBase64"`
}

type filesSaveImageView struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
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

// registerFilesImageCommands 注册图片本地回退保存 IPC。
// 只在终端图片粘贴没有可用 HTTP 图像服务时由前端显式调用(用户经系统保存对话框选定的路径),
// 路径来自对话框而不是网络输入; 前端不会把它用作静默上传通道。
// desktop 为 false(nexterm-server 装配)时一律拒绝, 任意路径写不能经服务端 /rpc 到达。
func registerFilesImageCommands(dispatcher *ipc.Dispatcher, desktop bool) error {
	return ipc.Register(dispatcher, "files_save_image", func(_ context.Context, _ *ipc.Call, input filesSaveImageInput) (filesSaveImageView, error) {
		if !desktop {
			return filesSaveImageView{}, ipc.NewError(ipc.CodeUnsupported, "图片本地保存只在桌面端可用")
		}
		if input.Path == "" {
			return filesSaveImageView{}, ipc.NewError(ipc.CodeBadParam, "参数错误: 保存路径不能为空")
		}
		data, err := base64.StdEncoding.DecodeString(input.ContentBase64)
		if err != nil {
			return filesSaveImageView{}, ipc.NewError(ipc.CodeBadParam, "参数错误: 图片内容不是合法 base64")
		}
		if len(data) == 0 || int64(len(data)) > filesImageSaveMaxBytes {
			return filesSaveImageView{}, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("参数错误: 图片大小需在 1 至 %d 字节之间", filesImageSaveMaxBytes))
		}
		if err := os.WriteFile(input.Path, data, 0o644); err != nil {
			return filesSaveImageView{}, fmt.Errorf("保存图片失败: %w", err)
		}
		return filesSaveImageView{Path: input.Path, Bytes: int64(len(data))}, nil
	})
}
