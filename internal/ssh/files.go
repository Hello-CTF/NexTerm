package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/fs/remote"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

// HelperFiles is a file system served by the helper's file channel: one SSH
// exec channel running the helper in file mode, multiplexing an operation per
// stream, as the login user.
type HelperFiles struct {
	*remote.FileSystem
	mux *remote.Mux
}

func (h *HelperFiles) Close() error {
	return h.mux.Close()
}

// OpenFileChannel ensures the helper on the client's host and opens its file
// channel. Windows hosts keep SFTP, whose path mapping the file manager
// already follows there.
func (r *Resolver) OpenFileChannel(ctx context.Context, client *transportssh.Client) (*HelperFiles, error) {
	daemon, err := r.EnsureDaemon(ctx, client)
	if err != nil {
		return nil, err
	}
	if daemon.Target.GOOS == "windows" {
		return nil, fmt.Errorf("%w: Windows 主机的文件继续使用 SFTP", base.ErrUnsupported)
	}
	command := "exec " + quoteShell(daemon.Binary) + " " + supervisor.HelperCommand + " " + supervisor.FilesOption
	host := daemon.host
	mux := remote.NewMux(func(context.Context) (net.Conn, error) {
		// The channel lives until the mux or the SSH connection closes.
		return host.DialExec(context.Background(), command)
	})
	probe, err := mux.Open(ctx)
	if err != nil {
		_ = mux.Close()
		if strings.Contains(err.Error(), "unknown option "+supervisor.FilesOption) {
			return nil, errors.New("主机上运行的 helper 版本较旧，不支持文件通道；helper 更新后自动启用")
		}
		return nil, err
	}
	_ = probe.Close()
	files := remote.NewFileSystem(remote.Options{
		Dial:     mux.Open,
		Exec:     client.Exec,
		Platform: daemon.Target.GOOS,
		Channel:  base.FileChannelHelper,
	})
	return &HelperFiles{FileSystem: files, mux: mux}, nil
}
