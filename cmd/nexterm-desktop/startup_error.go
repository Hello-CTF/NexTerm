package main

import (
	"fmt"
	"html"
	"log/slog"
	"os"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/platform"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func startupErrorHTML(paths platform.Paths, startupErr error) string {
	var b strings.Builder
	b.WriteString(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>NexTerm 启动失败</title>
<style>
body { font-family: system-ui, sans-serif; margin: 0; padding: 32px; background: #f6f7f9; color: #1c1e21; }
main { max-width: 640px; margin: 0 auto; background: #fff; border: 1px solid #e0e3e8; border-radius: 8px; padding: 24px 28px; }
h1 { font-size: 18px; margin: 0 0 12px; }
h2 { font-size: 14px; margin: 20px 0 6px; color: #555; }
pre { white-space: pre-wrap; word-break: break-all; background: #f0f1f4; border-radius: 6px; padding: 10px 12px; font-size: 13px; margin: 0; }
code { font-size: 13px; }
ol { margin: 6px 0 0; padding-left: 20px; }
li { margin: 4px 0; }
.note { color: #555; font-size: 13px; margin-top: 20px; }
</style>
</head>
<body>
<main>
<h1>NexTerm 启动失败</h1>
<p>NexTerm 无法完成启动初始化。</p>
<h2>错误信息</h2>
<pre>`)
	b.WriteString(html.EscapeString(startupErr.Error()))
	b.WriteString(`</pre>
<h2>数据目录</h2>
<pre>`)
	b.WriteString(html.EscapeString(paths.DataDir))
	b.WriteString(`</pre>
<h2>接下来可以</h2>
<ol>
<li>关闭此窗口，重新启动 NexTerm 重试。</li>
<li>如果仍然失败，请查看日志文件 <code>`)
	b.WriteString(html.EscapeString(paths.LogFile))
	b.WriteString(`</code>，并把上面的错误信息和日志一并反馈。</li>
</ol>
<p class="note">本窗口不会尝试修复、删除或迁移数据目录中的文件。在确认数据已备份之前，请不要删除或修改数据目录中的内容。</p>
</main>
</body>
</html>`)
	return b.String()
}

func showStartupError(paths platform.Paths, startupErr error, logger *slog.Logger) int {
	app := application.New(application.Options{
		Name:        "NexTerm",
		Description: "NexTerm startup error",
		Logger:      logger,
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "NexTerm 启动失败",
		HTML:   startupErrorHTML(paths, startupErr),
		Width:  720,
		Height: 560,
	})
	window.Show()
	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop: show startup error window:", err)
	}
	return 1
}
