package production

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// clipboardImageMaxBytes bounds one PNG written to the system clipboard.
const clipboardImageMaxBytes = 64 << 20

// clipboardImageTimeout bounds the helper process that hands the image to the system clipboard.
const clipboardImageTimeout = 10 * time.Second

type clipboardImageWriteRequest struct {
	PNGBase64 string `json:"pngBase64"`
}

type clipboardImageWriteDTO struct {
	Bytes int `json:"bytes"`
}

// writeClipboardImage puts a PNG on the system clipboard for the desktop webview, whose own
// clipboard API cannot write images on WebKit without a user gesture and whose native runtime
// writes text only.
func writeClipboardImage(ctx context.Context, pngBase64 string) (clipboardImageWriteDTO, error) {
	data, err := base64.StdEncoding.DecodeString(pngBase64)
	if err != nil {
		return clipboardImageWriteDTO{}, ipc.BadParam(fmt.Errorf("图片内容不是有效的 Base64: %w", err))
	}
	if len(data) == 0 {
		return clipboardImageWriteDTO{}, ipc.BadParam(fmt.Errorf("图片内容为空"))
	}
	if len(data) > clipboardImageMaxBytes {
		return clipboardImageWriteDTO{}, ipc.BadParam(fmt.Errorf("图片大小 %d 字节超过剪贴板上限 %d 字节", len(data), clipboardImageMaxBytes))
	}
	ctx, cancel := context.WithTimeout(ctx, clipboardImageTimeout)
	defer cancel()
	if err := setClipboardPNG(ctx, data); err != nil {
		return clipboardImageWriteDTO{}, err
	}
	return clipboardImageWriteDTO{Bytes: len(data)}, nil
}
