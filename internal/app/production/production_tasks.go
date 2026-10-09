package production

import (
	"context"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/tasks"
)

type PersistentTasks interface {
	Run(context.Context, tasks.Owner, tasks.Command, time.Duration) (tasks.Result, error)
	Get(context.Context, tasks.Owner, string) (tasks.Info, error)
	List(context.Context, tasks.Owner) ([]tasks.Info, error)
	Output(context.Context, tasks.Owner, string) (tasks.Output, error)
	ReadOutput(context.Context, tasks.Owner, string, int64, int) (tasks.Chunk, error)
	Kill(context.Context, tasks.Owner, string) (tasks.Info, error)
}

func (p *Production) PersistentTasks() PersistentTasks {
	if p == nil || p.Services.Tasks == nil {
		return nil
	}
	return p.Services.Tasks
}

var _ PersistentTasks = (*tasks.Manager)(nil)
