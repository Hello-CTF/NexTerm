package subagent

import (
	"context"
	"errors"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/cloudwego/eino/components/model"
)

func NewProfileModelFactory(manager *profiles.Manager) ModelFactory {
	return func(ctx context.Context) (model.BaseChatModel, error) {
		if manager == nil {
			return nil, errors.New("subagent profile manager is nil")
		}
		if _, ok := manager.ActiveProfile(); !ok {
			return nil, errors.New("未配置活动 AI 模型")
		}
		client, err := manager.ActiveClient()
		if err != nil {
			return nil, err
		}
		chatModel, _, err := client.BaseChatModel(ctx)
		return chatModel, err
	}
}
