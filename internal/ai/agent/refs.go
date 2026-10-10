package agent

import (
	"context"

	aicontext "github.com/Hello-CTF/NexTerm/internal/ai/context"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

func refSpecs(refs []RefArg) []aicontext.RefSpec {
	specs := make([]aicontext.RefSpec, 0, len(refs))
	for _, ref := range refs {
		specs = append(specs, aicontext.RefSpec{
			Kind:      ref.Kind,
			ID:        ref.ID,
			Label:     ref.Label,
			SessionID: ref.SessionID,
			Path:      ref.Path,
		})
	}
	return specs
}

func (r *Runner) refFileReader() aicontext.RefFileReader {
	if r.config.Tools == nil {
		return nil
	}
	return func(ctx context.Context, sessionID, path string) (string, error) {
		return r.config.Tools.ReadRefFile(ctx, tools.Scope{SessionID: sessionID}, path)
	}
}
