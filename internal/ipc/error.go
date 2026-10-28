package ipc

import (
	"errors"
	"log/slog"
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

func WrapError(code Code, message string, cause error) *Error {
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
