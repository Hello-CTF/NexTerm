package ipc

import (
	"errors"
	"log/slog"
	"strings"
)

type Code string

const (
	CodeIO               Code = "io"
	CodeDB               Code = "db"
	CodeDBMigrate        Code = "db_migrate"
	CodeSSH              Code = "ssh"
	CodeHostKeyPending   Code = "host_key_pending"
	CodeSFTP             Code = "sftp"
	CodeWinRM            Code = "winrm"
	CodeDisconnected     Code = "disconnected"
	CodeUnsupported      Code = "unsupported"
	CodeNotFound         Code = "not_found"
	CodeBadParam         Code = "bad_param"
	CodeVaultLocked      Code = "vault_locked"
	CodeVaultNotInit     Code = "vault_not_init"
	CodeVaultAlreadyInit Code = "vault_already_init"
	CodeBadMasterPass    Code = "bad_master_password"
	CodeDecrypt          Code = "decrypt"
	CodeForbidden        Code = "forbidden"
	// CodeMFAEnrollmentRequired means that under the mfa_required policy, unenrolled sessions are restricted to enrollment-related routes.
	CodeMFAEnrollmentRequired Code = "mfa_enrollment_required"
	CodeNeedsConfirm          Code = "needs_confirm"
	CodeTimeout               Code = "timeout"
	CodeCrypto                Code = "crypto"
	CodeInternal              Code = "internal"
)

type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`

	cause error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	// A raw string detail stays in the Go error text (logs, wrapped errors) although the UI message is plain.
	if raw, ok := e.Detail.(string); ok && raw != "" {
		return e.Message + ": " + raw
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// rawPrefixes are legacy "<label>: <raw error>" messages. WrapError keeps the code, shows a plain sentence and moves the
// raw text into Detail so the UI can offer it under a details disclosure.
var rawPrefixes = []struct {
	code    Code
	prefix  string
	message string
}{
	{CodeDB, "数据库错误: ", "读写本地数据失败，请重试；仍然失败请重启应用并查看详情"},
	{CodeDBMigrate, "数据库迁移错误: ", "升级本地数据失败，请重启应用后重试；仍然失败请查看详情"},
}

func WrapError(code Code, message string, cause error) *Error {
	for _, rule := range rawPrefixes {
		if code == rule.code && strings.HasPrefix(message, rule.prefix) {
			raw := strings.TrimPrefix(message, rule.prefix)
			return &Error{Code: code, Message: rule.message, Detail: raw, cause: cause}
		}
	}
	return &Error{Code: code, Message: message, cause: cause}
}

func (e *Error) WithDetail(detail any) *Error {
	if e == nil {
		return nil
	}
	clone := *e
	clone.Detail = detail
	return &clone
}

func BadParam(cause error) *Error {
	return WrapError(CodeBadParam, "参数错误: "+cause.Error(), cause)
}

func badDecode(cause error) *Error {
	return WrapError(CodeBadParam, "请求参数格式不正确，请检查输入后重试", cause)
}

func NormalizeError(err error) *Error {
	if err == nil {
		return nil
	}
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr
	}
	slog.Error("internal IPC error", "error", err)
	return WrapError(CodeInternal, "内部错误，请稍后重试", err)
}
