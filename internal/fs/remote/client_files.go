package remote

import (
	"context"
	"fmt"
	"io/fs"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// WriteFileVersion replaces the file only while it still matches expected.
func (f *FileSystem) WriteFileVersion(ctx context.Context, path string, data []byte, backup bool, expected conditional.Expectation) error {
	client, done, err := f.client(ctx)
	if err != nil {
		return err
	}
	defer done()
	return f.unsupportedRequest(client.WriteFileVersion(ctx, path, data, backup, expected), "安全条件写入")
}

// AtomicRename replaces the destination in one step. A server that predates
// the request is a Unix agent, whose plain rename already replaces atomically.
func (f *FileSystem) AtomicRename(ctx context.Context, from, to string) error {
	client, done, err := f.client(ctx)
	if err != nil {
		return err
	}
	err = client.AtomicRename(ctx, from, to)
	done()
	if deviceproto.UnsupportedRequest(err) && f.platform != "windows" {
		return f.Rename(ctx, from, to)
	}
	return err
}

func (f *FileSystem) statInfo(ctx context.Context, path string) (deviceproto.StatInfoReply, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return deviceproto.StatInfoReply{}, err
	}
	defer done()
	info, err := client.StatInfo(ctx, path)
	return info, f.unsupportedRequest(err, "读取文件信息")
}

func (f *FileSystem) FileMode(ctx context.Context, path string) (fs.FileMode, error) {
	info, err := f.statInfo(ctx, path)
	return info.Mode, err
}

func (f *FileSystem) ModTime(ctx context.Context, path string) (time.Time, error) {
	info, err := f.statInfo(ctx, path)
	return info.ModTime, err
}

// Watch holds one connection for the watch stream.
func (f *FileSystem) Watch(ctx context.Context, sink func(base.WatchEvent), done func(error)) (base.WatchHandle, error) {
	conn, err := f.dial(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := deviceproto.NewClient(conn).Watch(ctx, nil, sink, done)
	if err != nil {
		return nil, f.unsupportedRequest(err, "实时监视文件变化")
	}
	return stream, nil
}

// unsupportedRequest reports a request the server predates as
// base.ErrUnsupported, naming what to upgrade for this channel.
func (f *FileSystem) unsupportedRequest(err error, what string) error {
	if !deviceproto.UnsupportedRequest(err) {
		return err
	}
	server := "文件服务"
	remedy := "升级后重试"
	switch f.channel {
	case base.FileChannelDevice:
		server, remedy = "设备代理", "请在设备上升级设备代理后重试"
	case base.FileChannelHelper:
		server, remedy = "主机上的持久终端组件", "组件更新后重试"
	}
	return fmt.Errorf("%w: %s版本过旧，不支持%s，%s: %w", base.ErrUnsupported, server, what, remedy, err)
}

var (
	_ conditional.Writer = (*FileSystem)(nil)
)
