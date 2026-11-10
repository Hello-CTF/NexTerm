package sshconfig

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var secureCRTLine = regexp.MustCompile(`^([A-Za-z]):"([^"]*)"=(.*)$`)

// PreviewSecureCRT reads the .ini session files below a SecureCRT Sessions folder; subfolders become groups.
func PreviewSecureCRT(dir string, existing []ExistingAsset, existingKeys []ExistingKey, limits Limits) (*ImportPreview, error) {
	c := newClientCollector("SecureCRT", limits)
	if err := walkClientSessions(dir, ".ini", c, func(file, group, name string, data []byte) {
		addSecureCRTSession(c, decodeClientText(data), file, group, name)
	}); err != nil {
		return nil, err
	}
	return previewClientResult(SourceSecureCRT, c.finish("SecureCRT"), existing, existingKeys, limits), nil
}

func addSecureCRTSession(c *clientCollector, text, where, group, name string) {
	switch strings.ToLower(name) {
	case "__folderdata__":
		return
	case "default":
		if group == "" {
			c.diagnose("template-skipped", where, "%s 中的 %q 是新会话的默认模板，不作为主机导入", "SecureCRT", name)
			return
		}
	}
	values := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		match := secureCRTLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		key := strings.ToLower(match[2])
		switch strings.ToUpper(match[1]) {
		case "S":
			values[key] = strings.TrimSpace(match[3])
		case "D":
			if value, err := strconv.ParseUint(strings.TrimSpace(match[3]), 16, 32); err == nil {
				values[key] = strconv.FormatUint(value, 10)
			}
		}
	}
	if _, ok := values["protocol name"]; !ok {
		c.diagnose("config-unparsed", where, "%s 不是 SecureCRT 会话文件，已跳过", where)
		return
	}
	protocol := values["protocol name"]
	if strings.EqualFold(protocol, "SSH2") {
		protocol = "ssh"
	}
	item := clientSession{
		name:     name,
		group:    group,
		protocol: protocol,
		host:     values["hostname"],
		port:     values["[ssh2] port"],
		username: values["username"],
		where:    where,
	}
	identity := values["identity filename v2"]
	if identity == "" {
		identity = values["identity filename"]
	}
	// V2 entries append "::rawkey" or "::rawcert" to the path.
	if cut := strings.LastIndex(identity, "::"); cut >= 0 {
		identity = identity[:cut]
	}
	item.keyFile = identity
	if firewall := values["firewall name"]; firewall != "" && !strings.EqualFold(firewall, "None") {
		item.warnings = append(item.warnings, fmt.Sprintf("会话配置的防火墙/代理 %q 不会导入；如需代理或跳板，请在资产中重新设置", firewall))
	}
	c.add(item)
}
