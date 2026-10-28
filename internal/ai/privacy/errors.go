package privacy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
)

// UserError contains only fixed, safe remediation text suitable for a UI.
// Filesystem, database and native-library diagnostics must not use this type.
type UserError struct{ Message string }

func (e *UserError) Error() string   { return e.Message }
func userError(message string) error { return &UserError{Message: message} }

// describeDownloadError maps a download failure to a fixed Chinese summary.
// The raw error is returned separately as secondary detail for administrators.
func describeDownloadError(err error) (message, detail string) {
	detail = err.Error()
	var status *HTTPStatusError
	var netErr net.Error
	var safe *UserError
	switch {
	case errors.As(err, &safe):
		return safe.Message, ""
	case errors.Is(err, syscall.ENOSPC):
		return "磁盘空间不足，无法保存模型，请清理空间后重试", detail
	case errors.Is(err, ErrIntegrity):
		return "模型文件校验失败，可能在传输中损坏，请重新下载", detail
	case errors.Is(err, ErrStalled), errors.Is(err, context.DeadlineExceeded):
		return "下载长时间没有进度，请检查网络后重试", detail
	case errors.As(err, &status):
		return fmt.Sprintf("模型下载服务器返回错误（HTTP %d），请稍后重试", status.Code), detail
	case errors.As(err, &netErr):
		return "无法连接模型下载服务器，请检查网络或代理设置后重试", detail
	default:
		return "模型下载失败", detail
	}
}
