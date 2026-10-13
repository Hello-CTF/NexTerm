package tools

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	NotifyTool       = "notify"
	notifyMessageMax = 200
)

type NotifyArgs struct {
	Message string `json:"message" jsonschema:"required"`
}

type notifyOwnerContextKey struct{}

func WithNotifyOwner(ctx context.Context, ownerID string) context.Context {
	return context.WithValue(ctx, notifyOwnerContextKey{}, ownerID)
}

func NotifyOwnerID(ctx context.Context) string {
	ownerID, _ := ctx.Value(notifyOwnerContextKey{}).(string)
	return ownerID
}

func (r *Registry) notify(ctx context.Context, raw []byte) Output {
	if r.deps.Notify == nil {
		return Fail(errors.New("通知服务未配置"))
	}
	var args NotifyArgs
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	message := strings.TrimSpace(args.Message)
	if message == "" {
		return Fail(invalid("message 不能为空"))
	}
	if !utf8.ValidString(message) || len([]rune(message)) > notifyMessageMax {
		return Fail(invalid("通知内容过长或不是有效 UTF-8（上限 %d 字符）", notifyMessageMax))
	}
	if err := r.deps.Notify(ctx, message); err != nil {
		return Fail(err)
	}
	return OK("应用内通知已送达；通知内容需同时写进当轮回复，弹窗可能被错过")
}
