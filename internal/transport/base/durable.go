package base

import "context"

type DurableCreateOptions struct {
	ID      string
	Command []string
	Dir     string
	Env     []string
	Cols    uint32
	Rows    uint32
}

type DurableProvider interface {
	Create(context.Context, DurableCreateOptions) (DurableAttachment, error)
	Attach(context.Context, string) (DurableAttachment, error)
}

type DurableAttachment interface {
	Channel
	Kill(context.Context) error
}
