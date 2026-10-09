package production

import (
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

func TestConnectWinRMValidationAndClassification(t *testing.T) {
	ctx := t.Context()
	connector := &productionConnector{}

	_, err := connector.connectWinRM(ctx, productionAssetOptions{})
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("empty host = %+v, want bad_param", err)
	}
	if appErr.Message != "参数错误: WinRM 资产缺少主机地址" {
		t.Fatalf("empty host message = %q", appErr.Message)
	}

	_, err = connector.connectWinRM(ctx, productionAssetOptions{Host: "example.com", Port: 70000})
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeWinRM {
		t.Fatalf("invalid port = %+v, want winrm", err)
	}
	if !strings.Contains(appErr.Message, "WinRM port 70000 is outside 1..65535") {
		t.Fatalf("invalid port lost underlying detail: %q", appErr.Message)
	}

	_, err = connector.connectWinRM(ctx, productionAssetOptions{Host: "example.com", Auth: "kerberos"})
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeWinRM {
		t.Fatalf("invalid auth = %+v, want winrm", err)
	}
	if !strings.Contains(appErr.Message, `unsupported WinRM authentication "kerberos"`) {
		t.Fatalf("invalid auth lost underlying detail: %q", appErr.Message)
	}
}

func TestConnectRejectsMismatchedAssetOptions(t *testing.T) {
	connector := &productionConnector{}
	_, err := connector.Connect(t.Context(), session.Asset{ID: "a1", Kind: session.KindWinRM, Options: map[string]any{"port": "not-a-number"}}, 1)
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("mismatched options = %+v, want bad_param", err)
	}
	if !strings.HasPrefix(appErr.Message, "参数错误: 会话资产选项无效: ") {
		t.Fatalf("mismatched options message = %q", appErr.Message)
	}
}
