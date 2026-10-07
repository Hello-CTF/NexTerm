package main

import (
	"errors"
	"html"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/platform"
)

func TestStartupErrorHTMLShowsActualErrorAndDataDir(t *testing.T) {
	paths := platform.NewPaths("/tmp/nexterm-startup-error-test")
	startupErr := ipc.WrapError(ipc.CodeDBMigrate,
		"数据库迁移错误: execute migration 1 (init): SQL logic error: table asset_group already exists (1)",
		errors.New("table asset_group already exists"))
	doc := startupErrorHTML(paths, startupErr)
	for _, want := range []string{
		startupErr.Error(),
		paths.DataDir,
		paths.LogFile,
		"数据目录",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("startup error HTML missing %q", want)
		}
	}
}

func TestStartupErrorHTMLEscapesErrorText(t *testing.T) {
	paths := platform.NewPaths("/tmp/nexterm-startup-error-test")
	startupErr := errors.New(`<script>alert("pwned")</script>`)
	doc := startupErrorHTML(paths, startupErr)
	if strings.Contains(doc, `<script>alert`) {
		t.Fatal("startup error HTML contains unescaped error text")
	}
	if !strings.Contains(doc, html.EscapeString(startupErr.Error())) {
		t.Fatal("startup error HTML missing escaped error text")
	}
}

func TestStartupErrorHTMLDoesNotSpeculateAboutCause(t *testing.T) {
	paths := platform.NewPaths("/tmp/nexterm-startup-error-test")
	doc := startupErrorHTML(paths, errors.New("some failure"))
	for _, banned := range []string{"旧版", "版本过旧", "升级", "自动修复", "自动删除"} {
		if strings.Contains(doc, banned) {
			t.Errorf("startup error HTML must stay factual, found speculative phrase %q", banned)
		}
	}
}

func TestStartupErrorHTMLMakesNoAbsoluteDataClaim(t *testing.T) {
	paths := platform.NewPaths("/tmp/nexterm-startup-error-test")
	doc := startupErrorHTML(paths, errors.New("some failure"))
	// 初始化可能已提交迁移后才失败，页面不得断言用户数据未发生变化
	for _, banned := range []string{"你的数据没有被删除或修改", "数据没有被删除", "数据未被修改", "数据未发生变化"} {
		if strings.Contains(doc, banned) {
			t.Errorf("startup error HTML makes an unverifiable absolute data claim %q", banned)
		}
	}
	for _, want := range []string{"本窗口不会尝试修复、删除或迁移", "在确认数据已备份之前"} {
		if !strings.Contains(doc, want) {
			t.Errorf("startup error HTML missing scoped no-action or backup note %q", want)
		}
	}
}
