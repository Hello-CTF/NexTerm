package production

import (
	"context"
	"errors"
	"os"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/diagcapture"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/platform"
)

type diagnosticsCaptureDTO struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	TimeMs    int64  `json:"timeMs"`
	Archive   string `json:"archive,omitempty"`
}

type diagnosticsStatusDTO struct {
	Armed         bool                    `json:"armed"`
	Active        bool                    `json:"active"`
	ActiveDir     string                  `json:"activeDir,omitempty"`
	Root          string                  `json:"root"`
	CanOpenFolder bool                    `json:"canOpenFolder"`
	Captures      []diagnosticsCaptureDTO `json:"captures"`
}

type diagnosticsArmRequest struct {
	Armed bool `json:"armed"`
}

type diagnosticsPathRequest struct {
	Path string `json:"path"`
}

type diagnosticsPackDTO struct {
	Path string `json:"path"`
}

type diagnosticsService struct {
	paths     platform.Paths
	desktop   bool
	openPath  func(string) error
	activeDir func() string
}

func newDiagnosticsService(dataDir string, desktop bool) *diagnosticsService {
	return &diagnosticsService{
		paths:    platform.NewPaths(dataDir),
		desktop:  desktop,
		openPath: diagcapture.OpenFolder,
		activeDir: func() string {
			if session := diagcapture.Active(); session != nil {
				return session.Dir()
			}
			return ""
		},
	}
}

// authorize limits the commands to the desktop user, an explicitly open deployment, or a server superadmin:
// the capture settings and archives are process-wide, not per account.
func (s *diagnosticsService) authorize(ctx context.Context) error {
	if s.desktop || owner.OpenAccess(ctx) {
		return nil
	}
	if role, ok := ipc.RoleFromContext(ctx); ok && role == "superadmin" {
		return nil
	}
	return ipc.NewError(ipc.CodeForbidden, "仅管理员可使用诊断功能")
}

func (s *diagnosticsService) status() (diagnosticsStatusDTO, error) {
	captures, err := diagcapture.List(s.paths)
	if err != nil {
		return diagnosticsStatusDTO{}, err
	}
	result := diagnosticsStatusDTO{
		Armed:         diagcapture.IsArmed(s.paths),
		Root:          diagcapture.Root(s.paths),
		CanOpenFolder: s.desktop && diagcapture.CanOpenFolder(),
		Captures:      make([]diagnosticsCaptureDTO, 0, len(captures)),
	}
	if dir := s.activeDir(); dir != "" {
		result.Active, result.ActiveDir = true, dir
	}
	for _, c := range captures {
		result.Captures = append(result.Captures, diagnosticsCaptureDTO(c))
	}
	return result, nil
}

func diagnosticsIPCError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrNotExist):
		return ipc.WrapError(ipc.CodeNotFound, "诊断数据不存在", err)
	default:
		return ipc.WrapError(ipc.CodeBadParam, "诊断数据路径无效："+err.Error(), err)
	}
}

func registerDiagnosticsCommands(dispatcher *ipc.Dispatcher, s *diagnosticsService) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "diagnostics_status", func(ctx context.Context, _ *ipc.Call, _ struct{}) (diagnosticsStatusDTO, error) {
				if err := s.authorize(ctx); err != nil {
					return diagnosticsStatusDTO{}, err
				}
				return s.status()
			})
		},
		func() error {
			return ipc.Register(dispatcher, "diagnostics_arm", func(ctx context.Context, _ *ipc.Call, input diagnosticsArmRequest) (diagnosticsStatusDTO, error) {
				if err := s.authorize(ctx); err != nil {
					return diagnosticsStatusDTO{}, err
				}
				var err error
				if input.Armed {
					err = diagcapture.Arm(s.paths)
				} else {
					err = diagcapture.Disarm(s.paths)
				}
				if err != nil {
					return diagnosticsStatusDTO{}, ipc.WrapError(ipc.CodeIO, "无法更新调试模式开关："+err.Error(), err)
				}
				return s.status()
			})
		},
		func() error {
			return ipc.Register(dispatcher, "diagnostics_open_folder", func(ctx context.Context, _ *ipc.Call, input diagnosticsPathRequest) (any, error) {
				if err := s.authorize(ctx); err != nil {
					return nil, err
				}
				if !s.desktop {
					return nil, ipc.NewError(ipc.CodeUnsupported, "网页模式无法在服务器上打开文件夹，请使用“打包”并从服务器取回压缩包")
				}
				target := diagcapture.Root(s.paths)
				if input.Path != "" {
					resolved, err := diagcapture.ResolveCapture(s.paths, input.Path)
					if err != nil {
						return nil, diagnosticsIPCError(err)
					}
					target = resolved
				} else if err := os.MkdirAll(target, 0o700); err != nil {
					return nil, ipc.WrapError(ipc.CodeIO, "无法创建诊断文件夹："+err.Error(), err)
				}
				if err := s.openPath(target); err != nil {
					return nil, ipc.WrapError(ipc.CodeIO, "无法打开文件夹："+err.Error(), err)
				}
				return nil, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "diagnostics_pack", func(ctx context.Context, _ *ipc.Call, input diagnosticsPathRequest) (diagnosticsPackDTO, error) {
				if err := s.authorize(ctx); err != nil {
					return diagnosticsPackDTO{}, err
				}
				archive, err := diagcapture.Pack(s.paths, input.Path)
				if err != nil {
					return diagnosticsPackDTO{}, diagnosticsIPCError(err)
				}
				return diagnosticsPackDTO{Path: archive}, nil
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
