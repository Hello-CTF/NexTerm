package mount

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type Service struct {
	goos    string
	auditor Auditor
	runner  commandRunner
	newID   func() string
	now     func() time.Time
	onError func(error)
}

func NewService(config Config) *Service {
	if config.CommandTimeout <= 0 {
		config.CommandTimeout = 30 * time.Second
	}
	if config.NewID == nil {
		config.NewID = ids.New
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Service{
		goos:    runtime.GOOS,
		auditor: config.Auditor,
		runner:  execRunner{timeout: config.CommandTimeout},
		newID:   config.NewID,
		now:     config.Now,
		onError: config.OnError,
	}
}

func (s *Service) Start(context.Context) error {
	return nil
}

func (s *Service) Shutdown(context.Context) error {
	return nil
}

func (s *Service) Capability() *string {
	if s.goos == "darwin" {
		reason := MacOSUnavailableReason
		return &reason
	}
	if s.goos != "windows" && s.goos != "linux" {
		reason := "磁盘挂载仅支持 Windows 和 Linux；macOS 保持显式不可用"
		return &reason
	}
	return nil
}

func (s *Service) Create(ctx context.Context, args CreateArgs) (Entry, error) {
	if err := validateCreate(s.goos, args); err != nil {
		return Entry{}, ipc.BadParam(err)
	}
	start := time.Now()
	entry, err := s.create(ctx, args)
	s.audit(ctx, args.SessionID, "create", args.RemotePath, args.LocalPoint, start, err)
	return entry, err
}

func (s *Service) create(ctx context.Context, args CreateArgs) (Entry, error) {
	if reason := s.Capability(); reason != nil {
		return Entry{}, ipc.NewError(ipc.CodeUnsupported, *reason)
	}
	entry := Entry{
		ID:         s.newID(),
		LocalPoint: args.LocalPoint,
		Remote:     args.RemotePath,
		SessionID:  stringPointer(args.SessionID),
	}
	createdAt := s.now().UnixMilli()
	entry.CreatedAt = &createdAt

	var input command
	var err error
	switch s.goos {
	case "windows":
		var executable string
		executable, err = s.lookPath("net", "未找到 Windows net 命令")
		if err != nil {
			return Entry{}, err
		}
		input = command{name: executable, args: []string{"use", args.LocalPoint, args.RemotePath}}
		if args.Username != "" {
			input.args = append(input.args, "*", "/user:"+args.Username)
			input.stdin = []byte(args.Password + "\r\n")
			input.interactive = true
		}
		input.args = append(input.args, "/persistent:yes")
	case "linux":
		var executable string
		executable, err = s.lookPath("sshfs", "未找到 sshfs：请先安装（Debian/Ubuntu: apt install sshfs）")
		if err != nil {
			return Entry{}, err
		}
		remote := args.RemotePath
		if args.Username != "" {
			remote = args.Username + "@" + remote
		}
		entry.Remote = remote
		input = command{
			name: executable,
			args: []string{remote, args.LocalPoint, "-o", "reconnect", "-o", "ServerAliveInterval=30"},
		}
		if args.Password != "" {
			input.args = append(input.args, "-o", "password_stdin")
			input.stdin = []byte(args.Password + "\n")
		}
	}
	if _, err := s.run(ctx, input, "挂载磁盘", args.Password); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func (s *Service) Remove(ctx context.Context, args RemoveArgs) error {
	if err := validateRemove(s.goos, args); err != nil {
		return ipc.BadParam(err)
	}
	start := time.Now()
	err := s.remove(ctx, args.LocalPoint)
	s.audit(ctx, args.SessionID, "remove", "", args.LocalPoint, start, err)
	return err
}

func (s *Service) remove(ctx context.Context, localPoint string) error {
	if reason := s.Capability(); reason != nil {
		return ipc.NewError(ipc.CodeUnsupported, *reason)
	}
	var input command
	switch s.goos {
	case "windows":
		executable, err := s.lookPath("net", "未找到 Windows net 命令")
		if err != nil {
			return err
		}
		input = command{name: executable, args: []string{"use", localPoint, "/delete", "/y"}}
	case "linux":
		executable, err := s.runner.LookPath("fusermount3")
		if err != nil {
			executable, err = s.runner.LookPath("fusermount")
		}
		if err != nil {
			return ipc.WrapError(ipc.CodeUnsupported, "未找到 fusermount3 或 fusermount：请先安装 FUSE 工具", err)
		}
		input = command{name: executable, args: []string{"-u", localPoint}}
	}
	_, err := s.run(ctx, input, "卸载磁盘", "")
	return err
}

func (s *Service) List(ctx context.Context, _ bool) ([]Entry, error) {
	var input command
	switch s.goos {
	case "windows":
		executable, err := s.lookPath("net", "未找到 Windows net 命令")
		if err != nil {
			return nil, err
		}
		input = command{name: executable, args: []string{"use"}}
	case "darwin", "linux":
		executable, err := s.lookPath("mount", "未找到 mount 命令，无法读取系统挂载表")
		if err != nil {
			return nil, err
		}
		input = command{name: executable}
	default:
		return nil, ipc.NewError(ipc.CodeUnsupported, "当前操作系统不支持挂载表解析")
	}
	result, err := s.run(ctx, input, "读取系统挂载表", "")
	if err != nil {
		return nil, err
	}
	entries := ParseTable(s.goos, result.stdout)
	if entries == nil {
		entries = []Entry{}
	}
	return entries, nil
}

func (s *Service) lookPath(tool, message string) (string, error) {
	executable, err := s.runner.LookPath(tool)
	if err != nil {
		return "", ipc.WrapError(ipc.CodeUnsupported, message, err)
	}
	return executable, nil
}

func (s *Service) run(ctx context.Context, input command, action, secret string) (commandResult, error) {
	result, err := s.runner.Run(ctx, input)
	result.stdout = redact(result.stdout, secret)
	result.stderr = redact(result.stderr, secret)
	if err == nil {
		return result, nil
	}
	code := ipc.CodeInternal
	var exitErr *exec.ExitError
	var commandExitErr *commandExitError
	if !errors.As(err, &exitErr) && !errors.As(err, &commandExitErr) {
		code = ipc.CodeIO
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		code = ipc.CodeTimeout
	} else if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		code = ipc.CodeIO
	}
	detail := strings.TrimSpace(result.stderr)
	if stdout := strings.TrimSpace(result.stdout); stdout != "" {
		if detail != "" {
			detail += "\n"
		}
		detail += stdout
	}
	if detail == "" {
		detail = redact(err.Error(), secret)
	}
	return result, ipc.WrapError(code, action+"失败: "+detail, err)
}

type auditPayload struct {
	Remote string `json:"remote,omitempty"`
	Point  string `json:"point"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

func (s *Service) audit(ctx context.Context, sessionID, operation, remote, point string, start time.Time, operationErr error) {
	if s.auditor == nil {
		return
	}
	exitCode := int32(0)
	payload := auditPayload{Remote: remote, Point: point, OK: operationErr == nil}
	if operationErr != nil {
		exitCode = 1
		payload.Error = operationErr.Error()
	}
	duration := time.Since(start).Milliseconds()
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := s.auditor.AuditInsert(auditCtx, store.AuditInput{
		SessionID:  stringPointer(sessionID),
		Source:     "user",
		Kind:       "mount",
		Payload:    payload,
		ExitCode:   &exitCode,
		DurationMS: &duration,
	})
	if err != nil && s.onError != nil {
		s.onError(fmt.Errorf("写入挂载审计失败: %w", err))
	}
}

func validateCreate(platform string, args CreateArgs) error {
	if strings.TrimSpace(args.RemotePath) == "" {
		return errors.New("remotePath 不能为空")
	}
	if strings.TrimSpace(args.LocalPoint) == "" {
		return errors.New("localPoint 不能为空")
	}
	if strings.ContainsRune(args.RemotePath, 0) || strings.ContainsRune(args.LocalPoint, 0) {
		return errors.New("挂载路径不能包含 NUL 字符")
	}
	if strings.ContainsAny(args.Password, "\r\n\x00") {
		return errors.New("密码不能包含换行或 NUL 字符")
	}
	if strings.ContainsAny(args.Username, "\r\n\x00") {
		return errors.New("用户名不能包含换行或 NUL 字符")
	}
	switch platform {
	case "windows":
		if args.Password != "" && args.Username == "" {
			return errors.New("Windows 挂载使用密码时必须同时提供用户名")
		}
		if windowsOption(args.RemotePath) || windowsOption(args.LocalPoint) {
			return errors.New("Windows 挂载路径不能以选项前缀开头")
		}
		if !validWindowsDrive(args.LocalPoint) {
			return errors.New("Windows 挂载点必须是明确的驱动器号（如 Z:），不能使用通配符")
		}
	case "linux":
		if strings.HasPrefix(args.RemotePath, "-") || strings.HasPrefix(args.LocalPoint, "-") {
			return errors.New("SSHFS 路径不能以选项前缀开头")
		}
		colon := sshRemoteColon(args.RemotePath)
		if colon <= 0 {
			return errors.New("SSHFS 远端必须为 [user@]host:/path")
		}
		if strings.Contains(args.RemotePath[:colon], "@") {
			if args.Username != "" {
				return errors.New("remotePath 已包含用户名，请勿重复提供 username")
			}
		} else if args.Username != "" {
			if err := validateSSHUsername(args.Username); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateRemove(platform string, args RemoveArgs) error {
	if strings.TrimSpace(args.LocalPoint) == "" {
		return errors.New("localPoint 不能为空")
	}
	if strings.ContainsRune(args.LocalPoint, 0) {
		return errors.New("挂载路径不能包含 NUL 字符")
	}
	if platform == "windows" && windowsOption(args.LocalPoint) || platform == "linux" && strings.HasPrefix(args.LocalPoint, "-") {
		return errors.New("卸载路径不能以选项前缀开头")
	}
	if platform == "windows" && !validWindowsDrive(args.LocalPoint) {
		return errors.New("Windows 卸载点必须是明确的驱动器号（如 Z:），不能使用通配符")
	}
	return nil
}

func windowsOption(value string) bool {
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, "-")
}

func validateSSHUsername(username string) error {
	if username == "" || strings.ContainsAny(username, "@/") || strings.IndexFunc(username, unicode.IsSpace) >= 0 {
		return errors.New("SSHFS 用户名无效")
	}
	return nil
}

func sshRemoteColon(remote string) int {
	start := 0
	if strings.HasPrefix(remote, "[") {
		closing := strings.Index(remote, "]")
		if closing < 0 {
			return -1
		}
		start = closing + 1
	}
	colon := strings.Index(remote[start:], ":")
	if colon < 0 {
		return -1
	}
	return start + colon
}

func redact(value, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[REDACTED]")
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}
