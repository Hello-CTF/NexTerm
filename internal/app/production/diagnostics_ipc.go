package production

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"time"
	"unicode"

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

// diagnosticsFrontendReport is one performance window (or the start line) sent by the web view while a capture
// runs. Everything is numbers or short names; the server re-encodes it field by field instead of storing the raw
// request, so nothing free-form can reach the capture folder.
type diagnosticsFrontendReport struct {
	Kind          string                  `json:"kind"`
	Agent         string                  `json:"agent,omitempty"`
	ViewWidth     float64                 `json:"viewWidth,omitempty"`
	ViewHeight    float64                 `json:"viewHeight,omitempty"`
	WindowMs      float64                 `json:"windowMs,omitempty"`
	LagMaxMs      float64                 `json:"lagMaxMs,omitempty"`
	LagP95Ms      float64                 `json:"lagP95Ms,omitempty"`
	Fps           float64                 `json:"fps,omitempty"`
	MaxFrameGapMs float64                 `json:"maxFrameGapMs,omitempty"`
	Mutations     float64                 `json:"mutations,omitempty"`
	DomChurn      []diagnosticsNamedValue `json:"domChurn,omitempty"`
	Animations    float64                 `json:"animations,omitempty"`
	IPC           []diagnosticsNamedValue `json:"ipc,omitempty"`
	Events        []diagnosticsNamedValue `json:"events,omitempty"`
	StoreSets     float64                 `json:"storeSets,omitempty"`
	StoreKeys     []diagnosticsNamedValue `json:"storeKeys,omitempty"`
	DomNodes      float64                 `json:"domNodes,omitempty"`
	Xterms        float64                 `json:"xterms,omitempty"`
	Hidden        bool                    `json:"hidden,omitempty"`
	LongTasks     float64                 `json:"longTasks,omitempty"`
	LongTaskMsMax float64                 `json:"longTaskMsMax,omitempty"`
	HeapUsedMB    float64                 `json:"heapUsedMB,omitempty"`
}

type diagnosticsNamedValue struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

const (
	diagnosticsMaxName  = 64
	diagnosticsMaxItems = 24
)

// sanitizeName keeps a short printable label; anything else about the string is discarded.
func sanitizeName(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	if runes := []rune(value); len(runes) > diagnosticsMaxName {
		value = string(runes[:diagnosticsMaxName])
	}
	return value
}

func finiteNumbers(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func sanitizeNamed(items []diagnosticsNamedValue) ([]diagnosticsNamedValue, bool) {
	if len(items) > diagnosticsMaxItems {
		items = items[:diagnosticsMaxItems]
	}
	out := make([]diagnosticsNamedValue, 0, len(items))
	for _, item := range items {
		if !finiteNumbers(item.Value) {
			return nil, false
		}
		out = append(out, diagnosticsNamedValue{Name: sanitizeName(item.Name), Value: item.Value})
	}
	return out, true
}

// encode validates the report and returns the JSON line to store.
func (r diagnosticsFrontendReport) encode() ([]byte, error) {
	if r.Kind != "start" && r.Kind != "window" {
		return nil, ipc.NewError(ipc.CodeBadParam, "无效的前端性能数据类型")
	}
	if !finiteNumbers(r.ViewWidth, r.ViewHeight, r.WindowMs, r.LagMaxMs, r.LagP95Ms, r.Fps, r.MaxFrameGapMs, r.Mutations,
		r.Animations, r.StoreSets, r.DomNodes, r.Xterms, r.LongTasks, r.LongTaskMsMax, r.HeapUsedMB) {
		return nil, ipc.NewError(ipc.CodeBadParam, "前端性能数据包含无效数字")
	}
	var ok bool
	if r.DomChurn, ok = sanitizeNamed(r.DomChurn); !ok {
		return nil, ipc.NewError(ipc.CodeBadParam, "前端性能数据包含无效数字")
	}
	if r.Events, ok = sanitizeNamed(r.Events); !ok {
		return nil, ipc.NewError(ipc.CodeBadParam, "前端性能数据包含无效数字")
	}
	if r.IPC, ok = sanitizeNamed(r.IPC); !ok {
		return nil, ipc.NewError(ipc.CodeBadParam, "前端性能数据包含无效数字")
	}
	if r.StoreKeys, ok = sanitizeNamed(r.StoreKeys); !ok {
		return nil, ipc.NewError(ipc.CodeBadParam, "前端性能数据包含无效数字")
	}
	r.Agent = sanitizeName(r.Agent)
	line, err := json.Marshal(struct {
		TimeMs int64 `json:"t"`
		diagnosticsFrontendReport
	}{TimeMs: time.Now().UnixMilli(), diagnosticsFrontendReport: r})
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeInternal, "无法编码前端性能数据", err)
	}
	return line, nil
}

type diagnosticsService struct {
	paths      platform.Paths
	desktop    bool
	openPath   func(string) error
	revealPath func(string) error
	activeDir  func() string
}

func newDiagnosticsService(dataDir string, desktop bool) *diagnosticsService {
	return &diagnosticsService{
		paths:      platform.NewPaths(dataDir),
		desktop:    desktop,
		openPath:   diagcapture.OpenFolder,
		revealPath: diagcapture.RevealFile,
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
			return ipc.Register(dispatcher, "diagnostics_reveal_archive", func(ctx context.Context, _ *ipc.Call, input diagnosticsPathRequest) (any, error) {
				if err := s.authorize(ctx); err != nil {
					return nil, err
				}
				if !s.desktop {
					return nil, ipc.NewError(ipc.CodeUnsupported, "网页模式无法在服务器上打开文件夹")
				}
				archive, err := diagcapture.ResolveArchive(s.paths, input.Path)
				if err != nil {
					return nil, diagnosticsIPCError(err)
				}
				if err := s.revealPath(archive); err != nil {
					return nil, ipc.WrapError(ipc.CodeIO, "无法在文件夹中显示压缩包："+err.Error(), err)
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
	registrations = append(registrations,
		func() error {
			return ipc.Register(dispatcher, "diagnostics_delete", func(ctx context.Context, _ *ipc.Call, input diagnosticsPathRequest) (diagnosticsStatusDTO, error) {
				if err := s.authorize(ctx); err != nil {
					return diagnosticsStatusDTO{}, err
				}
				if err := diagcapture.Delete(s.paths, input.Path, s.activeDir()); err != nil {
					if errors.Is(err, diagcapture.ErrCaptureActive) {
						return diagnosticsStatusDTO{}, ipc.WrapError(ipc.CodeBadParam, "这次启动正在记录，无法删除", err)
					}
					return diagnosticsStatusDTO{}, diagnosticsIPCError(err)
				}
				return s.status()
			})
		},
		func() error {
			return ipc.Register(dispatcher, "diagnostics_clear", func(ctx context.Context, _ *ipc.Call, _ struct{}) (diagnosticsStatusDTO, error) {
				if err := s.authorize(ctx); err != nil {
					return diagnosticsStatusDTO{}, err
				}
				if _, err := diagcapture.Clear(s.paths, s.activeDir()); err != nil {
					return diagnosticsStatusDTO{}, ipc.WrapError(ipc.CodeIO, "部分调试记录无法删除："+err.Error(), err)
				}
				return s.status()
			})
		},
		func() error {
			return ipc.Register(dispatcher, "diagnostics_report_frontend", func(ctx context.Context, _ *ipc.Call, input diagnosticsFrontendReport) (any, error) {
				if err := s.authorize(ctx); err != nil {
					return nil, err
				}
				line, err := input.encode()
				if err != nil {
					return nil, err
				}
				if err := diagcapture.AppendFrontend(line); err != nil {
					switch {
					case errors.Is(err, diagcapture.ErrNoActiveCapture):
						return nil, ipc.WrapError(ipc.CodeBadParam, "当前没有正在进行的调试记录", err)
					case errors.Is(err, diagcapture.ErrFrontendFull):
						return nil, ipc.WrapError(ipc.CodeBadParam, "前端性能数据已达上限", err)
					}
					return nil, ipc.WrapError(ipc.CodeIO, "无法写入前端性能数据", err)
				}
				return nil, nil
			})
		},
	)
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
