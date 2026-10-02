package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type TokenStore interface {
	SyncToken(context.Context) (string, error)
	RotateSyncToken(context.Context) (string, error)
}

func RunTokenCommand(ctx context.Context, command Command, store TokenStore, stdout io.Writer) error {
	if command != CommandToken && command != CommandRotateToken {
		return fmt.Errorf("command %s is not a token command", command)
	}
	if store == nil {
		return ipc.NewError(ipc.CodeUnsupported, "同步令牌存储尚未接入")
	}

	var (
		token string
		err   error
	)
	if command == CommandRotateToken {
		token, err = store.RotateSyncToken(ctx)
	} else {
		token, err = store.SyncToken(ctx)
	}
	if err != nil {
		return err
	}
	if token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n") {
		return ipc.NewError(ipc.CodeInternal, "令牌存储返回了无效令牌")
	}
	_, err = fmt.Fprintln(stdout, token)
	return err
}
