package forward

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type ruleRequest struct {
	ID string `json:"id"`
}

type startRuleRequest struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
}

func (s *Service) RegisterRuleCommands(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "forward_rule_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]Rule, error) {
		return s.RuleList(forwardRequestContext(ctx))
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "forward_rule_save", func(ctx context.Context, _ *ipc.Call, input RuleInput) (Rule, error) {
		return s.SaveRule(forwardRequestContext(ctx), input)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "forward_rule_delete", func(ctx context.Context, _ *ipc.Call, input ruleRequest) (any, error) {
		return nil, s.DeleteRule(forwardRequestContext(ctx), input.ID)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "forward_rule_start", func(ctx context.Context, _ *ipc.Call, input startRuleRequest) (Rule, error) {
		return s.StartRule(forwardRequestContext(ctx), input.ID, input.SessionID)
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "forward_rule_stop", func(ctx context.Context, _ *ipc.Call, input ruleRequest) (Rule, error) {
		return s.StopRule(forwardRequestContext(ctx), input.ID)
	})
}
