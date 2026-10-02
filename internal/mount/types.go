package mount

import (
	"context"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

const MacOSUnavailableReason = "磁盘挂载在 macOS 上暂不可用：依赖 macFUSE（系统扩展）+ sshfs，尚未完成适配"

type Entry struct {
	ID         string  `json:"id"`
	LocalPoint string  `json:"localPoint"`
	Remote     string  `json:"remote"`
	SessionID  *string `json:"sessionId"`
	CreatedAt  *int64  `json:"createdAt"`
}

type CreateArgs struct {
	SessionID  string `json:"sessionId"`
	RemotePath string `json:"remotePath"`
	LocalPoint string `json:"localPoint"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
}

type RemoveArgs struct {
	LocalPoint string `json:"localPoint"`
	SessionID  string `json:"sessionId,omitempty"`
}

type Auditor interface {
	AuditInsert(context.Context, store.AuditInput) error
}

type Config struct {
	Auditor        Auditor
	CommandTimeout time.Duration
	NewID          func() string
	Now            func() time.Time
	OnError        func(error)
}
