//go:build darwin && !ios

package production

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// setClipboardPNG hands the image to the pasteboard through AppleScript, which reads it from a
// private temporary file as PNG data. The path goes in as an argument, never into the script.
func setClipboardPNG(ctx context.Context, data []byte) error {
	file, err := os.CreateTemp("", "nexterm-clipboard-*.png")
	if err != nil {
		return ipc.WrapError(ipc.CodeIO, "无法暂存剪贴板图片: "+err.Error(), err)
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return ipc.WrapError(ipc.CodeIO, "无法暂存剪贴板图片: "+err.Error(), err)
	}
	if err := file.Close(); err != nil {
		return ipc.WrapError(ipc.CodeIO, "无法暂存剪贴板图片: "+err.Error(), err)
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript",
		"-e", "on run argv",
		"-e", "set the clipboard to (read (POSIX file (item 1 of argv)) as «class PNGf»)",
		"-e", "end run",
		path)
	if output, err := cmd.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return ipc.WrapError(ipc.CodeIO, fmt.Sprintf("图片未能写入系统剪贴板: %s", detail), err)
	}
	return nil
}
