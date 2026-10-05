package production

import (
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
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
	service := &terminalCommandService{}
	dispatcher := ipc.NewDispatcher()
	if err := service.registerDocker(dispatcher); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"docker_image_pull", "docker_image_remove"} {
		response := dispatchStoreTest(dispatcher, command, `{"sessionId":"s1","image":" "}`)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
			t.Fatalf("%s = %+v, want bad_param", command, response)
		}
		if response.Error.Message != "参数错误: 镜像引用不能为空" {
			t.Fatalf("%s message = %q", command, response.Error.Message)
		}
	}
}
