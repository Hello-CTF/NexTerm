//go:build darwin || linux

package production

import (
	"context"
	"encoding/base64"
	"os"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

// TestFilesSaveImageProductionAssemblyGuard 验证真实装配: builder 把 config.Desktop
// 接进 ProductionServices.desktop, 桌面端允许本地保存, 服务端装配一律 unsupported 且不落盘。
func TestFilesSaveImageProductionAssemblyGuard(t *testing.T) {
	newAssembly := func(desktop bool) *Production {
		production, err := NewProduction(t.Context(), ProductionConfig{DataDir: t.TempDir(), Desktop: desktop})
		if err != nil {
			t.Fatal(err)
		}
		if err := production.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		return production
	}

	desktop := newAssembly(true)
	desktopPath := t.TempDir() + "/desktop-assembly.png"
	response := dispatchStoreTest(desktop.Dispatcher, "files_save_image",
		`{"path":`+quotedJSON(desktopPath)+`,"contentBase64":`+quotedJSON(base64.StdEncoding.EncodeToString([]byte("png")))+`}`)
	var view filesSaveImageView
	requireStoreTestResponse(t, response, &view)
	if view.Path != desktopPath || view.Bytes != 3 {
		t.Fatalf("desktop assembly save = %+v", view)
	}
	if err := desktop.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	server := newAssembly(false)
	serverPath := t.TempDir() + "/server-assembly.png"
	response = dispatchStoreTest(server.Dispatcher, "files_save_image",
		`{"path":`+quotedJSON(serverPath)+`,"contentBase64":`+quotedJSON(base64.StdEncoding.EncodeToString([]byte("png")))+`}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("server assembly files_save_image = %+v, want unsupported", response)
	}
	if _, err := os.Stat(serverPath); !os.IsNotExist(err) {
		t.Fatalf("server assembly wrote the file: %v", err)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
