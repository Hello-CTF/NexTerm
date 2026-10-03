package aicontext

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func Render(bundle Bundle) string {
	var output strings.Builder
	if bundle.Session != nil {
		output.WriteString("[会话上下文]\n")
		fmt.Fprintf(&output, "- 连接：%s（%s）\n", bundle.Session.Name, bundle.Session.Kind)
		if bundle.Session.Host != "" {
			fmt.Fprintf(&output, "- 主机：%s\n", bundle.Session.Host)
		}
		if bundle.Session.Username != "" {
			fmt.Fprintf(&output, "- 用户：%s\n", bundle.Session.Username)
		}
		if bundle.Session.CWD != "" {
			fmt.Fprintf(&output, "- 当前目录：%s\n", bundle.Session.CWD)
		}
	}
	if bundle.Screen != "" {
		output.WriteString("[终端当前屏幕]\n")
		output.WriteString(bundle.Screen)
		output.WriteByte('\n')
	}
	if len(bundle.Tail) != 0 {
		output.WriteString("[终端最近输出]\n")
		output.WriteString(tailRunes(strings.Join(bundle.Tail, "\n"), 8000))
		output.WriteByte('\n')
	}
	if bundle.Recon != "" {
		output.WriteString("[环境侦察快照]\n")
		output.WriteString(prefixRunes(bundle.Recon, 12000))
		output.WriteByte('\n')
	}
	if len(bundle.Tables) != 0 {
		output.WriteString("[数据库表]\n")
		for _, table := range bundle.Tables {
			fmt.Fprintf(&output, "%s.%s\n", table.Schema, table.Name)
		}
	}
	if len(bundle.Containers) != 0 {
		output.WriteString("[Docker 容器]\n")
		for _, container := range bundle.Containers {
			fmt.Fprintf(&output, "%s [%s] %s (%s)\n", container.Name, container.State, container.Image, container.Status)
		}
	}
	if bundle.Selection != "" {
		output.WriteString("[用户选中的文本]\n")
		output.WriteString(bundle.Selection)
		output.WriteByte('\n')
	}
	return output.String()
}

func TrimToBudget(bundle *Bundle) {
	if len(Render(*bundle)) <= BudgetBytes {
		return
	}
	bundle.Selection = ""
	if len(Render(*bundle)) <= BudgetBytes {
		return
	}
	bundle.Tables = nil
	bundle.Containers = nil
	if len(Render(*bundle)) <= BudgetBytes {
		return
	}
	bundle.Recon = prefixBytes(bundle.Recon, 8000)
	if len(Render(*bundle)) <= BudgetBytes {
		return
	}
	bundle.Screen = prefixBytes(bundle.Screen, 16000)
	if len(bundle.Tail) > 40 {
		bundle.Tail = append([]string(nil), bundle.Tail[len(bundle.Tail)-40:]...)
	}
	for len(Render(*bundle)) > BudgetBytes && len(bundle.Tail) > 1 {
		bundle.Tail = bundle.Tail[1:]
	}
	if len(Render(*bundle)) > BudgetBytes {
		bundle.Recon = ""
	}
	for len(Render(*bundle)) > BudgetBytes && bundle.Screen != "" {
		excess := len(Render(*bundle)) - BudgetBytes
		bundle.Screen = prefixBytes(bundle.Screen, len(bundle.Screen)-excess-1)
	}
	if len(Render(*bundle)) > BudgetBytes {
		bundle.Tail = nil
	}
	if len(Render(*bundle)) > BudgetBytes {
		bundle.Session = nil
	}
}

func prefixRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func tailRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[len(runes)-limit:])
}

func prefixBytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}
