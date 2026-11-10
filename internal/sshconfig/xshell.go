package sshconfig

import (
	"fmt"
	"strings"
)

// PreviewXshell reads the .xsh session files below an Xshell sessions folder; subfolders become groups.
func PreviewXshell(dir string, existing []ExistingAsset, existingKeys []ExistingKey, limits Limits) (*ImportPreview, error) {
	c := newClientCollector("Xshell", limits)
	if err := walkClientSessions(dir, ".xsh", c, func(file, group, name string, data []byte) {
		addXshellSession(c, decodeClientText(data), file, group, name)
	}); err != nil {
		return nil, err
	}
	return previewClientResult(SourceXshell, c.finish("Xshell"), existing, existingKeys, limits), nil
}

func addXshellSession(c *clientCollector, text, where, group, name string) {
	sections := map[string]*iniSection{}
	for _, section := range parseINI(text) {
		sections[strings.ToUpper(section.name)] = section
	}
	get := func(section, key string) string {
		if s := sections[section]; s != nil {
			return s.get(key)
		}
		return ""
	}
	if sections["CONNECTION"] == nil {
		c.diagnose("config-unparsed", where, "%s 不是 Xshell 会话文件，已跳过", where)
		return
	}
	protocol := get("CONNECTION", "Protocol")
	if protocol == "" {
		protocol = "SSH"
	}
	item := clientSession{
		name:     name,
		group:    group,
		protocol: protocol,
		host:     get("CONNECTION", "Host"),
		port:     get("CONNECTION", "Port"),
		username: get("CONNECTION:AUTHENTICATION", "UserName"),
		where:    where,
	}
	// Method 1 is public-key authentication; the key is a name in Xshell's own key store, not a file.
	if key := get("CONNECTION:AUTHENTICATION", "UserKey"); key != "" && get("CONNECTION:AUTHENTICATION", "Method") == "1" {
		item.keyWarning = fmt.Sprintf("会话使用 Xshell 用户密钥 %q，它保存在 Xshell 的密钥管理器中，无法直接导入；请在 Xshell 中将其导出为 OpenSSH 格式后导入密钥并绑定到该主机", key)
	}
	if proxy := get("CONNECTION:PROXY", "Proxy"); proxy != "" {
		item.warnings = append(item.warnings, fmt.Sprintf("会话配置的代理 %q 不会导入；如需代理，请在资产中重新设置", proxy))
	}
	c.add(item)
}
