package forward

import (
	"context"
	"net"
	"strconv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	StatusStopped   = "stopped"
	savedListenHost = "127.0.0.1"
)

type RuleInput = store.ForwardRuleInput

type RuleStore interface {
	ForwardRuleList(context.Context) ([]store.ForwardRuleRow, error)
	ForwardRuleGet(context.Context, string) (store.ForwardRuleRow, error)
	ForwardRuleSave(context.Context, store.ForwardRuleInput) (store.ForwardRuleRow, error)
	ForwardRuleDelete(context.Context, string) error
	ForwardRuleAssetAvailable(context.Context, string) error
	ForwardRuleAssetEnvironment(context.Context, string) (string, error)
}

type Rule struct {
	ID         string `json:"id"`
	AssetID    string `json:"assetId"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	ListenHost string `json:"listenHost"`
	ListenPort int32  `json:"listenPort"`
	TargetHost string `json:"targetHost"`
	TargetPort int32  `json:"targetPort"`
	Status     string `json:"status"`
	SessionID  string `json:"sessionId,omitempty"`
	Error      string `json:"error,omitempty"`
	// AutoStart stays out of the IPC JSON surface until the forward contract
	// regeneration lands; startup recovery already persists and honors it.
	AutoStart bool  `json:"-"`
	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

func ruleFromRow(row store.ForwardRuleRow) Rule {
	return Rule{
		ID: row.ID, AssetID: row.AssetID, Name: row.Name, Kind: KindLocal,
		ListenHost: savedListenHost, ListenPort: row.ListenPort,
		TargetHost: row.TargetHost, TargetPort: row.TargetPort, Status: StatusStopped,
		AutoStart: row.AutoStart,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func ruleWithSpec(row store.ForwardRuleRow, spec Spec) Rule {
	rule := ruleFromRow(row)
	rule.ListenHost = spec.ListenHost
	rule.Status = spec.Status
	rule.SessionID = spec.SessionID
	rule.Error = spec.Error
	return rule
}

func (s *Service) ruleStore() (RuleStore, error) {
	if s.rules == nil {
		return nil, ipc.NewError(ipc.CodeUnsupported, "转发规则存储未配置")
	}
	return s.rules, nil
}

func (s *Service) runtimeForRule(ctx context.Context, id string) (Spec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.forwards[id]
	if f == nil || f.ruleID != id || f.owner != ipc.OwnerID(ctx) {
		return Spec{}, false
	}
	return f.spec, true
}

func (s *Service) savedRuntime(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.forwards[id]; f != nil && f.ruleID != "" {
		return id, true
	}
	normalized := strings.TrimSpace(id)
	if normalized != id {
		if f := s.forwards[normalized]; f != nil && f.ruleID != "" {
			return normalized, true
		}
	}
	return "", false
}

func (s *Service) RuleList(ctx context.Context) ([]Rule, error) {
	rules, err := s.ruleStore()
	if err != nil {
		return nil, err
	}
	rows, err := rules.ForwardRuleList(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Rule, 0, len(rows))
	for _, row := range rows {
		rule := ruleFromRow(row)
		if spec, ok := s.runtimeForRule(ctx, row.ID); ok {
			rule = ruleWithSpec(row, spec)
		}
		result = append(result, rule)
	}
	return result, nil
}

func (s *Service) SaveRule(ctx context.Context, input RuleInput) (Rule, error) {
	rules, err := s.ruleStore()
	if err != nil {
		return Rule{}, err
	}
	input.ID = strings.TrimSpace(input.ID)
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	if input.ID != "" {
		if _, err := rules.ForwardRuleGet(ctx, input.ID); err != nil {
			return Rule{}, err
		}
		s.mu.Lock()
		running := s.forwards[input.ID] != nil
		s.mu.Unlock()
		if running {
			return Rule{}, ipc.NewError(ipc.CodeBadParam, "请先停止该规则的运行时，再编辑规则")
		}
	}
	row, err := rules.ForwardRuleSave(ctx, input)
	if err != nil {
		return Rule{}, err
	}
	rule := ruleFromRow(row)
	if spec, ok := s.runtimeForRule(ctx, row.ID); ok {
		rule = ruleWithSpec(row, spec)
	}
	return rule, nil
}

func validRulePort(port int32) (uint16, error) {
	if port < 1 || port > 65535 {
		return 0, ipc.NewError(ipc.CodeBadParam, "转发规则端口必须在 1-65535 之间")
	}
	return uint16(port), nil
}

func ruleRuntimeMatches(row store.ForwardRuleRow, spec Spec) bool {
	if spec.ID != row.ID || spec.HostID != row.AssetID || spec.Kind != KindLocal ||
		spec.ListenHost != savedListenHost || int32(spec.ListenPort) != row.ListenPort ||
		spec.TargetHost == nil || spec.TargetPort == nil {
		return false
	}
	return *spec.TargetHost == row.TargetHost && int32(*spec.TargetPort) == row.TargetPort
}

func (s *Service) StartRule(ctx context.Context, id, sessionID string) (Rule, error) {
	if err := s.available(); err != nil {
		return Rule{}, err
	}
	rules, err := s.ruleStore()
	if err != nil {
		return Rule{}, err
	}
	id = strings.TrimSpace(id)
	sessionID = strings.TrimSpace(sessionID)
	if id == "" || sessionID == "" {
		return Rule{}, ipc.NewError(ipc.CodeBadParam, "规则 ID 和会话 ID 不能为空")
	}
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	row, err := rules.ForwardRuleGet(ctx, id)
	if err != nil {
		return Rule{}, err
	}
	if err := rules.ForwardRuleAssetAvailable(ctx, row.AssetID); err != nil {
		return Rule{}, err
	}
	listenPort, err := validRulePort(row.ListenPort)
	if err != nil {
		return Rule{}, err
	}
	targetPort, err := validRulePort(row.TargetPort)
	if err != nil {
		return Rule{}, err
	}
	if strings.TrimSpace(row.TargetHost) == "" {
		return Rule{}, ipc.NewError(ipc.CodeBadParam, "转发规则目标主机不能为空")
	}
	if s.sessionHost == nil {
		return Rule{}, ipc.NewError(ipc.CodeUnsupported, "转发规则需要会话资产校验接口")
	}
	if _, err := s.validateSession(ctx, sessionID); err != nil {
		return Rule{}, err
	}
	hostID, err := s.hostFor(ctx, sessionID)
	if err != nil {
		return Rule{}, err
	}
	if hostID != row.AssetID {
		return Rule{}, ipc.NewError(ipc.CodeBadParam, "所选会话不属于规则绑定的资产")
	}

	s.mu.Lock()
	existing := s.forwards[id]
	if existing != nil && existing.owner != ipc.OwnerID(ctx) {
		s.mu.Unlock()
		return Rule{}, session.IPCError(session.ErrForbidden)
	}
	if existing != nil && (existing.ruleID != id || !ruleRuntimeMatches(row, existing.spec)) {
		s.mu.Unlock()
		return Rule{}, ipc.NewError(ipc.CodeBadParam, "规则运行时配置不一致，请先停止后再启动")
	}
	if existing != nil && existing.spec.Status == StatusActive {
		spec := existing.spec
		s.mu.Unlock()
		return ruleWithSpec(row, spec), nil
	}
	s.mu.Unlock()
	if existing != nil {
		spec, err := s.retry(ctx, ipc.OwnerID(ctx), id, sessionID)
		if err != nil {
			return Rule{}, err
		}
		return ruleWithSpec(row, spec), nil
	}

	targetHost := strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(row.TargetHost), "]"), "[")
	target := net.JoinHostPort(targetHost, strconv.Itoa(int(targetPort)))
	address := net.JoinHostPort(savedListenHost, strconv.Itoa(int(listenPort)))
	listener, err := s.listen("tcp", address)
	if err != nil {
		return Rule{}, ipc.WrapError(ipc.CodeIO, "监听转发规则端口失败: "+err.Error(), err)
	}
	spec, err := s.startWithID(ctx, id, sessionID, hostID, KindLocal, listener, savedListenHost, target, &targetHost, &targetPort)
	if err != nil {
		return Rule{}, err
	}
	if spec.ListenPort != listenPort {
		s.mu.Lock()
		f := s.forwards[id]
		s.mu.Unlock()
		if f != nil {
			s.discard(f)
		}
		return Rule{}, ipc.NewError(ipc.CodeIO, "转发规则监听器未使用指定的固定端口")
	}
	return ruleWithSpec(row, spec), nil
}

func (s *Service) stopRuleLocked(ctx context.Context, row store.ForwardRuleRow) (Rule, error) {
	s.mu.Lock()
	f := s.forwards[row.ID]
	if f != nil && f.owner != ipc.OwnerID(ctx) {
		s.mu.Unlock()
		return Rule{}, session.IPCError(session.ErrForbidden)
	}
	if f != nil && f.ruleID != row.ID {
		s.mu.Unlock()
		return Rule{}, ipc.NewError(ipc.CodeBadParam, "运行时不属于该持久规则")
	}
	s.mu.Unlock()
	if f == nil {
		return ruleFromRow(row), nil
	}
	if err := f.stopAndWait(); err != nil {
		return Rule{}, ipc.WrapError(ipc.CodeIO, "转发规则监听关闭失败，规则定义已保留: "+err.Error(), err)
	}
	s.mu.Lock()
	if s.forwards[row.ID] == f {
		delete(s.forwards, row.ID)
	}
	s.mu.Unlock()
	return ruleFromRow(row), nil
}

func (s *Service) StopRule(ctx context.Context, id string) (Rule, error) {
	rules, err := s.ruleStore()
	if err != nil {
		return Rule{}, err
	}
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	row, err := rules.ForwardRuleGet(ctx, id)
	if err != nil {
		return Rule{}, err
	}
	return s.stopRuleLocked(ctx, row)
}

func (s *Service) DeleteRule(ctx context.Context, id string) error {
	rules, err := s.ruleStore()
	if err != nil {
		return err
	}
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	row, err := rules.ForwardRuleGet(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.stopRuleLocked(ctx, row); err != nil {
		return err
	}
	return rules.ForwardRuleDelete(ctx, id)
}
