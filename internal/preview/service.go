package preview

import (
	"context"
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// Service registers the in-app browser commands.
type Service struct {
	desktop bool
	client  *http.Client
}

// NewService returns a service whose commands are only registered in desktop mode: the embed check
// makes the backend request an arbitrary URL, which on a shared server would be an SSRF primitive.
func NewService(desktop bool) *Service {
	return &Service{desktop: desktop}
}

// EmbedCheckArgs is the input of preview_embed_check.
type EmbedCheckArgs struct {
	URL string `json:"url"`
}

func (s *Service) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	if !s.desktop {
		return nil
	}
	return ipc.Register(dispatcher, "preview_embed_check", func(ctx context.Context, _ *ipc.Call, input EmbedCheckArgs) (EmbedCheck, error) {
		result, err := CheckEmbeddable(ctx, s.client, input.URL)
		if err != nil {
			return EmbedCheck{}, ipc.BadParam(err)
		}
		return result, nil
	})
}
