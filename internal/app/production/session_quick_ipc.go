package production

import (
	"context"
	"fmt"
	"os/user"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
)

type sessionQuickConnectRequest struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	AuthKind      string `json:"authKind"`
	Password      string `json:"password"`
	AcceptHostKey bool   `json:"acceptHostKey"`
}

func (s *terminalCommandService) connectQuick(ctx context.Context, input sessionQuickConnectRequest) (sessionInfoDTO, error) {
	asset, err := quickConnectSessionAsset(input)
	if err != nil {
		return sessionInfoDTO{}, err
	}
	connected, err := s.sessions.Connect(ctx, asset)
	if err != nil {
		return sessionInfoDTO{}, terminalIPCError(err)
	}
	return productionSessionInfo(connected.Info()), nil
}

func quickConnectSessionAsset(input sessionQuickConnectRequest) (session.Asset, error) {
	host := strings.TrimSpace(input.Host)
	if host == "" {
		return session.Asset{}, ipc.BadParam(fmt.Errorf("快速连接缺少主机地址"))
	}
	port := input.Port
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return session.Asset{}, ipc.BadParam(fmt.Errorf("快速连接端口 %d 无效，应在 1-65535 之间", port))
	}
	username := strings.TrimSpace(input.Username)
	if username == "" {
		username = quickConnectDefaultUsername()
	}
	options := map[string]any{
		"host":     host,
		"port":     port,
		"username": username,
	}
	switch input.AuthKind {
	case "agent":
		options["authKind"] = "agent"
	case "password":
		if input.Password == "" {
			return session.Asset{}, ipc.BadParam(fmt.Errorf("密码认证需要输入密码"))
		}
		options["authKind"] = "password"
		options["password"] = input.Password
	default:
		return session.Asset{}, ipc.BadParam(fmt.Errorf("快速连接仅支持密码或 SSH agent 认证"))
	}
	if input.AcceptHostKey {
		options["autoAcceptUnknownHost"] = true
	}
	return session.Asset{
		ID: ids.New(), Name: quickConnectDisplayName(username, host, port), Kind: session.KindSSH, Options: options,
	}, nil
}

func quickConnectDisplayName(username, host string, port int) string {
	if port == 22 {
		return fmt.Sprintf("%s@%s", username, host)
	}
	return fmt.Sprintf("%s@%s:%d", username, host, port)
}

func quickConnectDefaultUsername() string {
	current, err := user.Current()
	if err != nil || current.Username == "" {
		return "root"
	}
	if index := strings.LastIndex(current.Username, `\`); index >= 0 {
		return current.Username[index+1:]
	}
	return current.Username
}
