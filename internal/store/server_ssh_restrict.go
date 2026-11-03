package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// ServerSSHFields lists the SSH settings that make the server process run a command or read a
// server file. Multi-user servers reject them; the connector and the write path share this one list.
type ServerSSHFields struct {
	ProxyCommand string
	AgentSocket  string
	ForwardAgent bool
	AuthKind     string
	KeyPath      string
	CertPath     string
}

const serverSSHAlternatives = "请改用跳板机或 SOCKS/HTTP 代理，私钥请上传私钥内容或使用凭据库中的私钥"

// RejectServerSSHFields returns an unsupported error naming the first restricted field that is set.
func RejectServerSSHFields(fields ServerSSHFields) error {
	reject := func(field, why string) error {
		return ipc.NewError(ipc.CodeUnsupported, fmt.Sprintf("多用户服务端不支持 SSH 选项「%s」：%s。%s", field, why, serverSSHAlternatives))
	}
	switch {
	case strings.TrimSpace(fields.ProxyCommand) != "":
		return reject("代理命令 proxyCommand", "它会在服务器上执行命令")
	case fields.AgentSocket != "":
		return reject("Agent socket agentSocket", "它会让服务器进程读取服务器上的 SSH Agent")
	case fields.ForwardAgent:
		return reject("转发 Agent forwardAgent", "它会使用服务器上的 SSH Agent")
	case fields.AuthKind == "agent":
		return reject("认证方式 agent", "它会使用服务器进程的 SSH Agent")
	case fields.KeyPath != "":
		return reject("私钥路径 keyPath", "它会读取服务器上的文件")
	case fields.CertPath != "":
		return reject("证书路径 certPath", "它会读取服务器上的文件")
	}
	return nil
}

// RejectServerSSHFile rejects a private-key credential that references a server file.
func RejectServerSSHFile() error {
	return ipc.NewError(ipc.CodeUnsupported, "多用户服务端不支持引用服务器文件的私钥凭据（会读取服务器上的文件）。"+serverSSHAlternatives)
}

// serverSSHFieldsFromJSON reads the restricted fields from stored asset options. Non-object payloads
// carry none of them. The connector decodes options with encoding/json into a struct, which matches
// keys case-insensitively and lets the last duplicate win; to never disagree with it, every key that
// folds to a restricted name is inspected on its own and any restricted value counts, whatever its
// case or position. A value of the wrong type makes the connector fail before anything runs, so it
// carries nothing here.
func serverSSHFieldsFromJSON(raw string) ServerSSHFields {
	var options map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &options) != nil {
		return ServerSSHFields{}
	}
	var fields ServerSSHFields
	setText := func(target *string, value json.RawMessage) {
		var text string
		if json.Unmarshal(value, &text) == nil && *target == "" && text != "" {
			*target = text
		}
	}
	for key, value := range options {
		switch {
		case strings.EqualFold(key, "proxyCommand"):
			setText(&fields.ProxyCommand, value)
		case strings.EqualFold(key, "agentSocket"):
			setText(&fields.AgentSocket, value)
		case strings.EqualFold(key, "authKind"):
			var kind string
			if json.Unmarshal(value, &kind) == nil && kind == "agent" {
				fields.AuthKind = kind
			}
		case strings.EqualFold(key, "keyPath"):
			setText(&fields.KeyPath, value)
		case strings.EqualFold(key, "certPath"):
			setText(&fields.CertPath, value)
		case strings.EqualFold(key, "forwardAgent"):
			var forward bool
			if json.Unmarshal(value, &forward) == nil && forward {
				fields.ForwardAgent = true
			}
		}
	}
	return fields
}

func isSSHAssetKind(kind string) bool { return kind == "ssh" || kind == "docker" }

// checkServerSSHAssetInput validates a user-written asset on a multi-user server (owner in ctx).
// Desktop and --auth=off writes carry no owner and are left alone. Only the supplied values are
// checked, so legacy rows stay editable; the connector rejects them when they are used.
func checkServerSSHAssetInput(ctx context.Context, kind string, optionsJSON *string, authKind, keyPath *string) error {
	if ipc.OwnerID(ctx) == "" || !isSSHAssetKind(kind) {
		return nil
	}
	var fields ServerSSHFields
	if optionsJSON != nil {
		fields = serverSSHFieldsFromJSON(*optionsJSON)
	}
	if authKind != nil {
		fields.AuthKind = *authKind
	}
	if keyPath != nil {
		fields.KeyPath = strings.TrimSpace(*keyPath)
	}
	return RejectServerSSHFields(fields)
}

func checkServerSSHGroupDefaults(ctx context.Context, defaults GroupDefaults) error {
	if ipc.OwnerID(ctx) == "" {
		return nil
	}
	return RejectServerSSHFields(ServerSSHFields{AuthKind: defaults.AuthKind, KeyPath: strings.TrimSpace(defaults.KeyPath)})
}

func patchOptionalValue(value Optional[string]) *string {
	if !value.Set {
		return nil
	}
	return value.Value
}

// checkServerLocalAssetKind refuses new local-terminal assets for accounts: they would start a shell on the
// server. Existing rows stay editable (the kind cannot change) and the connector refuses to open them.
func checkServerLocalAssetKind(ctx context.Context, kind string) error {
	if kind == "local" && ipc.OwnerID(ctx) != "" {
		return ipc.NewError(ipc.CodeUnsupported, "多用户服务端不支持创建本地终端资产（会在服务器上启动 shell），请改用 SSH 资产或设备")
	}
	return nil
}

// ServerSSHAssetWarning explains why an asset written outside the user-facing validation (sync merge,
// bundle import) cannot connect on a multi-user server. It returns "" for desktop contexts, non-SSH
// kinds and assets without restricted settings.
func ServerSSHAssetWarning(ctx context.Context, row AssetRow) string {
	if ipc.OwnerID(ctx) == "" || !isSSHAssetKind(row.Kind) {
		return ""
	}
	fields := serverSSHFieldsFromJSON(row.OptionsJSON)
	if row.AuthKind != nil {
		fields.AuthKind = *row.AuthKind
	}
	if row.KeyPath != nil {
		fields.KeyPath = strings.TrimSpace(*row.KeyPath)
	}
	err := RejectServerSSHFields(fields)
	if err == nil {
		return ""
	}
	var appErr *ipc.Error
	message := err.Error()
	if errors.As(err, &appErr) {
		message = appErr.Message
	}
	return fmt.Sprintf("资产「%s」已保存，但在服务端无法连接：%s", row.Name, message)
}
