//go:build linux && !android

package production

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// clipboardImageCommand picks the clipboard tool for the running session: wl-copy on Wayland,
// xclip on X11. Both keep serving the selection from a background process after they return.
func clipboardImageCommand(getenv func(string) string, lookPath func(string) (string, error)) ([]string, error) {
	var candidates [][]string
	if getenv("WAYLAND_DISPLAY") != "" {
		candidates = append(candidates, []string{"wl-copy", "--type", "image/png"})
	}
	if getenv("DISPLAY") != "" {
		candidates = append(candidates, []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-i"})
	}
	if len(candidates) == 0 {
		return nil, ipc.NewError(ipc.CodeUnsupported, "当前桌面会话没有可用的图形剪贴板")
	}
	for _, candidate := range candidates {
		if path, err := lookPath(candidate[0]); err == nil {
			return append([]string{path}, candidate[1:]...), nil
		}
	}
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		names = append(names, candidate[0])
	}
	return nil, ipc.NewError(ipc.CodeUnsupported, fmt.Sprintf("写入图片需要安装 %s", strings.Join(names, " / ")))
}

func setClipboardPNG(ctx context.Context, data []byte) error {
	argv, err := clipboardImageCommand(os.Getenv, exec.LookPath)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return ipc.WrapError(ipc.CodeIO, fmt.Sprintf("图片未能写入系统剪贴板: %s", detail), err)
	}
	return nil
}
