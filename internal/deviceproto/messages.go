package deviceproto

import (
	"io/fs"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type ErrorMsg struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PathMsg struct {
	Path string `json:"path"`
}

// ListChunk carries one batch of directory entries; a list reply is zero or
// more chunks terminated by a FrameEnd.
type ListChunk struct {
	Entries []base.FileEntry `json:"entries"`
}

type ReadMsg struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"maxBytes"`
}

type RangeMsg struct {
	Path     string `json:"path"`
	Offset   int64  `json:"offset"`
	MaxBytes int64  `json:"maxBytes"`
}

// FileDataMsg heads a read reply: Size is the total file size, followed by
// FrameData chunks (the requested window) and a FrameEnd.
type FileDataMsg struct {
	Size int64 `json:"size"`
}

// WriteMsg heads a write request, followed by FrameData chunks holding
// exactly Size bytes and a FrameEnd; the server answers FrameOK.
type WriteMsg struct {
	Path   string `json:"path"`
	Backup bool   `json:"backup"`
	Size   int64  `json:"size"`
}

type RenameMsg struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type DeleteMsg struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
}

type ChmodMsg struct {
	Path string      `json:"path"`
	Mode fs.FileMode `json:"mode"`
}

type ChecksumMsg struct {
	Path      string `json:"path"`
	Algorithm string `json:"algorithm"`
}

type ChecksumReply struct {
	Sum string `json:"sum"`
}

type StatReply struct {
	Size int64 `json:"size"`
}

type OpenWriteMsg struct {
	Path   string `json:"path"`
	Append bool   `json:"append"`
}

// OpenedMsg acknowledges an open request; Size is set for reads only.
type OpenedMsg struct {
	Size int64 `json:"size"`
}

type DialMsg struct {
	Network string `json:"network"`
	Address string `json:"address"`
}

type ListenMsg struct {
	Network string `json:"network"`
	Address string `json:"address"`
}

// ListenAckMsg reports the actual bound address, which matters when the
// request asked for port 0.
type ListenAckMsg struct {
	Address string `json:"address"`
}

// InboundMsg announces one accepted connection on the listen bridge; Address
// is the remote peer address.
type InboundMsg struct {
	ConnID  string `json:"connId"`
	Address string `json:"address"`
}

// AcceptMsg heads an accept bridge, naming the parked inbound connection the
// client claims.
type AcceptMsg struct {
	ConnID string `json:"connId"`
}

// WriteVersionMsg heads a conditional write: like WriteMsg, and the server
// replaces the file only while it still matches Expected.
type WriteVersionMsg struct {
	Path     string                  `json:"path"`
	Backup   bool                    `json:"backup"`
	Size     int64                   `json:"size"`
	Expected conditional.Expectation `json:"expected"`
}

// StatInfoReply describes one path without following a final symlink's
// target for Kind; ModTime is the modification time.
type StatInfoReply struct {
	Size    int64         `json:"size"`
	Mode    fs.FileMode   `json:"mode"`
	ModTime time.Time     `json:"modTime"`
	Kind    base.FileKind `json:"kind"`
}

// WatchMsg names the directories a watch stream holds; FrameWatch starts the
// stream and each FrameWatchSet replaces the set. The server answers
// FrameOK once, then sends FrameWatchEvent frames carrying base.WatchEvent.
type WatchMsg struct {
	Dirs []string `json:"dirs"`
}

// ExtractedMsg answers FrameExtract with the directory the archive was
// unpacked into, in the request's path style.
type ExtractedMsg struct {
	Dir string `json:"dir"`
}
