package base

import "context"

// WatchEvent reports a change inside one watched directory. Names lists the
// entries that changed; it is empty when the whole directory must be listed
// again (too many changes at once, or the watch overflowed). Unwatchable
// reports that the directory cannot be watched at all, so the caller polls it.
type WatchEvent struct {
	Dir         string   `json:"dir"`
	Names       []string `json:"names,omitempty"`
	Overflow    bool     `json:"overflow,omitempty"`
	Unwatchable bool     `json:"unwatchable,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

// WatchHandle controls one running watch. Set replaces the watched
// directories; Close stops the watch and releases everything it holds.
type WatchHandle interface {
	Set(dirs []string) error
	Close() error
}

// FileWatcher watches directories without recursion and delivers change
// events to sink, from one goroutine at a time, until the handle is closed,
// ctx ends or the watch fails; done, when not nil, receives the failure.
type FileWatcher interface {
	Watch(ctx context.Context, sink func(WatchEvent), done func(error)) (WatchHandle, error)
}

// FileChannel names the protocol that serves a file system.
const (
	FileChannelSFTP   = "sftp"
	FileChannelHelper = "helper"
	FileChannelDevice = "device"
	FileChannelLocal  = "local"
	FileChannelWinRM  = "winrm"
)

// FileChannelReporter is implemented by file systems that name their channel.
type FileChannelReporter interface {
	FileChannel() string
}
