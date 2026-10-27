package production

import (
	"context"
	"errors"
	"os"

	"github.com/Hello-CTF/NexTerm/internal/ai/privacy"
	"github.com/Hello-CTF/NexTerm/internal/ai/privacy/inference"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type privacyEngine struct{ *inference.Engine }

func (e privacyEngine) Detect(ctx context.Context, text string) ([]privacy.Span, error) {
	spans, err := e.Engine.Detect(ctx, text)
	if err != nil {
		return nil, err
	}
	result := make([]privacy.Span, len(spans))
	for i, span := range spans {
		result[i] = privacy.Span{Start: span.Start, End: span.End, Label: span.Label, Score: span.Score}
	}
	return result, nil
}
func loadPrivacyEngine(ctx context.Context, directory string, installation privacy.Installation) (privacy.Engine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtimeDir := os.Getenv("NEXTERM_AI_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = inference.DefaultRuntimeDirectory()
	}
	engine, err := inference.Open(directory, runtimeDir, string(installation.Precision))
	if err != nil {
		return nil, err
	}
	return privacyEngine{engine}, nil
}

type privacyConfigureRequest struct {
	Enabled   bool              `json:"enabled"`
	Precision privacy.Precision `json:"precision"`
	Revision  string            `json:"revision"`
}
type privacyDownloadRequest struct {
	Precision privacy.Precision `json:"precision"`
}

func registerPrivacyCommands(dispatcher *ipc.Dispatcher, manager *privacy.Manager) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "ai_privacy_status", func(ctx context.Context, _ *ipc.Call, _ struct{}) (privacy.Status, error) {
				return privacyCommandResult(manager.Status(ctx))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_privacy_configure", func(ctx context.Context, _ *ipc.Call, input privacyConfigureRequest) (privacy.Status, error) {
				return privacyCommandResult(manager.Configure(ctx, input.Enabled, input.Precision, input.Revision))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_privacy_check_updates", func(ctx context.Context, _ *ipc.Call, _ struct{}) (privacy.ModelRelease, error) {
				return privacyCommandResult(manager.CheckUpdates(ctx))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_privacy_download", func(ctx context.Context, _ *ipc.Call, input privacyDownloadRequest) (privacy.Status, error) {
				return privacyCommandResult(manager.StartDownload(ctx, input.Precision))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_privacy_cancel_download", func(ctx context.Context, _ *ipc.Call, _ struct{}) (any, error) {
				return privacyCommandResult[any](nil, manager.CancelDownload(ctx))
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func privacyCommandResult[T any](value T, err error) (T, error) {
	var userError *privacy.UserError
	if errors.As(err, &userError) {
		return value, ipc.WrapError(ipc.CodeBadParam, userError.Message, err)
	}
	var platformError *inference.PlatformError
	if errors.As(err, &platformError) {
		return value, ipc.WrapError(ipc.CodeUnsupported, platformError.Message, err)
	}
	if errors.Is(err, inference.ErrUnavailable) {
		return value, ipc.WrapError(ipc.CodeUnsupported, "本地隐私推理运行时未安装或不兼容，请安装支持本设备的完整 NexTerm 版本", err)
	}
	return value, err
}
