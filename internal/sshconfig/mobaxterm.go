package sshconfig

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// mobaSSHType is the session type code MobaXterm writes for SSH bookmarks.
const mobaSSHType = "109"

// Field positions in the %-separated settings of a MobaXterm SSH bookmark.
const (
	mobaFieldHost        = 1
	mobaFieldPort        = 2
	mobaFieldUser        = 3
	mobaFieldGatewayHost = 8
	mobaFieldKey         = 14
)

var mobaPathVariable = regexp.MustCompile(`^_[A-Za-z]+_`)

// PreviewMobaXterm reads the bookmarks of a MobaXterm.ini file.
func PreviewMobaXterm(path string, existing []ExistingAsset, existingKeys []ExistingKey, limits Limits) (*ImportPreview, error) {
	data, err := readClientFile(path)
	if err != nil {
		return nil, err
	}
	result, err := ParseMobaXterm(decodeClientText(data), path, limits)
	if err != nil {
		return nil, err
	}
	return previewClientResult(SourceMobaXterm, result, existing, existingKeys, limits), nil
}

// ParseMobaXterm maps the SSH bookmarks of MobaXterm.ini to hosts. Each [Bookmarks] or [Bookmarks_N] section is one
// folder named by its SubRep value; a bookmark line is name=#type#settings#terminal#....
func ParseMobaXterm(text, where string, limits Limits) (*Result, error) {
	c := newClientCollector("MobaXterm", limits)
	home, _ := os.UserHomeDir()
	found := false
	for _, section := range parseINI(text) {
		if !strings.HasPrefix(strings.ToLower(section.name), "bookmarks") {
			continue
		}
		found = true
		group := section.get("SubRep")
		for _, key := range section.keys {
			lower := strings.ToLower(key)
			if lower == "subrep" || lower == "imgnum" {
				continue
			}
			value := section.get(key)
			if !strings.HasPrefix(value, "#") {
				continue
			}
			parts := strings.Split(value, "#")
			if len(parts) < 3 {
				continue
			}
			sessionType := strings.TrimSpace(parts[1])
			if sessionType != mobaSSHType {
				c.diagnose("session-protocol-skipped", where, "会话 %q 不是 SSH 会话（MobaXterm 类型 %s），已跳过", key, sessionType)
				continue
			}
			fields := strings.Split(parts[2], "%")
			field := func(index int) string {
				if index < len(fields) {
					return strings.TrimSpace(fields[index])
				}
				return ""
			}
			item := clientSession{
				name:     key,
				group:    group,
				protocol: "ssh",
				host:     field(mobaFieldHost),
				port:     field(mobaFieldPort),
				username: field(mobaFieldUser),
				where:    where,
			}
			if gateway := field(mobaFieldGatewayHost); gateway != "" {
				item.warnings = append(item.warnings, fmt.Sprintf("会话配置的 SSH 网关 %s 不会导入；如需跳板，请在资产中设置跳板机", gateway))
			}
			if keyFile := field(mobaFieldKey); keyFile != "" {
				keyFile = strings.Replace(keyFile, "_ProfileDir_", home, 1)
				if mobaPathVariable.MatchString(keyFile) {
					item.keyWarning = fmt.Sprintf("私钥路径 %s 使用了 MobaXterm 的路径变量，无法解析；请在导入后为该主机绑定密钥", keyFile)
				} else {
					item.keyFile = keyFile
				}
			}
			c.add(item)
		}
	}
	if !found {
		return nil, fmt.Errorf("%w: no [Bookmarks] section in %s", ErrNoClientSessions, where)
	}
	return c.finish("MobaXterm"), nil
}
