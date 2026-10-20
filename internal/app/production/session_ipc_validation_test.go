package production

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestProductionBytesValidationMessage(t *testing.T) {
	_, err := productionBytes([]int{104, 101, 256})
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("productionBytes = %+v, want bad_param", err)
	}
	if appErr.Message != "参数错误: 序数 2 的终端字节超出 0..255 范围" {
		t.Fatalf("productionBytes message = %q", appErr.Message)
	}
	if _, err := productionBytes([]int{0, 127, 255}); err != nil {
		t.Fatalf("valid bytes rejected: %v", err)
	}
}

func TestDockerImageValidationMessages(t *testing.T) {
	transport := &dockerShellTransport{kind: "ssh"}
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return transport, nil
	}), OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "s1", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	service := &terminalCommandService{sessions: manager}
	dispatcher := ipc.NewDispatcher()
	if err := service.registerDocker(dispatcher); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"docker_image_pull", "docker_image_remove"} {
		response := dispatchStoreTest(dispatcher, command, `{"sessionId":"`+connected.ID+`","image":" "}`)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
			t.Fatalf("%s = %+v, want bad_param", command, response)
		}
		if response.Error.Message != "参数错误: 镜像引用不能为空" {
			t.Fatalf("%s message = %q", command, response.Error.Message)
		}
	}
}
