//go:build !(darwin && !ios) && !(linux && !android)

package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// setClipboardPNG is not needed where the webview writes images itself (WebView2 on Windows).
func setClipboardPNG(context.Context, []byte) error {
	return ipc.NewError(ipc.CodeUnsupported, "此平台不支持由应用写入剪贴板图片")
}
