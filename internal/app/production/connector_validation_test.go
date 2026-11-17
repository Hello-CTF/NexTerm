package production

import (
	"errors"
	"net"
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

	_, err = connector.connectWinRM(ctx, productionAssetOptions{Host: "example.com", Auth: "digest"})
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeWinRM {
		t.Fatalf("invalid auth = %+v, want winrm", err)
	}
	if !strings.Contains(appErr.Message, `unsupported WinRM authentication "digest"`) {
		t.Fatalf("invalid auth lost underlying detail: %q", appErr.Message)
	}
}

func TestConnectWinRMPassesKerberosOptions(t *testing.T) {
	ctx := t.Context()
	connector := &productionConnector{}
	base := productionAssetOptions{Host: "win01.corp.example.com", Username: "alice", Auth: "kerberos", KerberosRealm: "corp.example.com"}
	tests := []struct {
		name   string
		mutate func(*productionAssetOptions)
		want   string
	}{
		// The explicit realm is consumed, so validation reaches the missing password.
		{"realm", func(*productionAssetOptions) {}, "Kerberos 认证需要密码凭据"},
		{"no realm", func(o *productionAssetOptions) { o.KerberosRealm = "" }, "Kerberos 需要域（realm）"},
		{"KDC list", func(o *productionAssetOptions) { o.KerberosKDC = "dc1.corp.example.com, dc2:bad" }, `KDC 地址 "dc2:bad"`},
		{"SPN", func(o *productionAssetOptions) { o.KerberosSPN = "HTTP" }, `Kerberos SPN "HTTP" 无效`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := base
			test.mutate(&options)
			_, err := connector.connectWinRM(ctx, options)
			var appErr *ipc.Error
			if !errors.As(err, &appErr) || appErr.Code != ipc.CodeWinRM || !strings.Contains(appErr.Message, test.want) {
				t.Fatalf("error = %+v, want winrm error containing %q", err, test.want)
			}
		})
	}
	if got := winRMKerberosKDCs(" dc1.corp.example.com, dc2:750;\n10.0.0.5 "); strings.Join(got, "|") != "dc1.corp.example.com|dc2:750|10.0.0.5" {
		t.Fatalf("KDC list = %q", got)
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

func TestConnectTelnetAsset(t *testing.T) {
	connector := &productionConnector{}
	_, err := connector.Connect(t.Context(), session.Asset{ID: "t1", Kind: session.KindTelnet, Options: map[string]any{}}, 1)
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("empty host = %+v, want bad_param", err)
	}
	_, err = connector.Connect(t.Context(), session.Asset{ID: "t1", Kind: session.KindTelnet, Options: map[string]any{"host": "h", "localEcho": "maybe"}}, 1)
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("invalid local echo = %+v, want bad_param", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().(*net.TCPAddr)
	_ = listener.Close()
	_, err = connector.Connect(t.Context(), session.Asset{ID: "t1", Kind: session.KindTelnet, Options: map[string]any{"host": "127.0.0.1", "port": address.Port}}, 1)
	if !errors.As(err, &appErr) || !strings.Contains(appErr.Message, "Telnet") {
		t.Fatalf("closed port = %+v, want a Telnet connect error", err)
	}

	listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	address = listener.Addr().(*net.TCPAddr)
	transport, err := connector.Connect(t.Context(), session.Asset{ID: "t1", Kind: session.KindTelnet, Options: map[string]any{"host": "127.0.0.1", "port": address.Port, "encoding": "gbk"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if transport.Kind() != session.KindTelnet {
		t.Fatalf("transport kind = %s", transport.Kind())
	}
}
