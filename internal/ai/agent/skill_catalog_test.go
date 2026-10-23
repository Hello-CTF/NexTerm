package agent

import (
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

func TestSkillCatalogPromptNamesEverySkillAndTheSlashTrigger(t *testing.T) {
	prompt := skillCatalogPrompt([]tools.SkillSummary{
		{Name: "nginx-check", Description: "检查 nginx 配置并重载"},
		{Name: "disk-report", Description: "整理磁盘占用"},
	})
	for _, fragment := range []string{"[Available Skills]", "load_skill", "/<skill name>", "- nginx-check: 检查 nginx 配置并重载", "- disk-report: 整理磁盘占用"} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("prompt missing %q:\n%s", fragment, prompt)
		}
	}
}
