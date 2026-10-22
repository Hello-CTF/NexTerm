package guard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

const DeviceGrantsSettingKey = "ai.device_grants"

// Sentinel validation errors for grant mutations; IPC layers classify them
// with errors.Is instead of matching error text.
var (
	ErrGrantKindsRequired  = errors.New("设备授权至少需要一个种类")
	ErrInvalidGrantKind    = errors.New("设备授权种类仅支持 terminal_write/session_exec/db_query")
	ErrGrantDeviceRequired = errors.New("设备授权缺少设备 ID")
)

func DeviceGrantsSettingKeyForUser(ctx context.Context) string {
	if userID, ok := ipc.UserIDFromContext(ctx); ok {
		return DeviceGrantsSettingKey + ".user." + userID
	}
	return DeviceGrantsSettingKey
}

type GrantKind string

const (
	GrantKindTerminalWrite GrantKind = "terminal_write"
	GrantKindSessionExec   GrantKind = "session_exec"
	GrantKindDBQuery       GrantKind = "db_query"
)

// DeviceGrant is a long-term device authorization. Permanent is explicit in the
// persisted record: records written before the field existed are permanent.
type DeviceGrant struct {
	DeviceID   string      `json:"deviceId"`
	Kinds      []GrantKind `json:"kinds"`
	Permanent  bool        `json:"permanent"`
	UpdatedAt  int64       `json:"updatedAt"`
	generation string
}

func ParseGrantKinds(values []string) ([]GrantKind, error) {
	if len(values) == 0 {
		return nil, ErrGrantKindsRequired
	}
	seen := make(map[GrantKind]struct{}, len(values))
	kinds := make([]GrantKind, 0, len(values))
	for _, value := range values {
		kind := GrantKind(strings.TrimSpace(value))
		switch kind {
		case GrantKindTerminalWrite, GrantKindSessionExec, GrantKindDBQuery:
		default:
			return nil, ErrInvalidGrantKind
		}
		if _, ok := seen[kind]; ok {
			continue
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	return kinds, nil
}

func (g DeviceGrant) Covers(kind GrantKind) bool {
	if kind == "" {
		return false
	}
	for _, granted := range g.Kinds {
		if granted == kind {
			return true
		}
	}
	return false
}

func OperationGrantKind(operation string) GrantKind {
	switch operation {
	case "send_keys":
		return GrantKindTerminalWrite
	case "exec_commands", "terminal_create":
		return GrantKindSessionExec
	case "db_list_tables", "db_describe", "db_query", "db_redis_scan":
		return GrantKindDBQuery
	default:
		return ""
	}
}

type GrantAuditEvent struct {
	Action    string      `json:"action"`
	DeviceID  string      `json:"deviceId"`
	Kinds     []GrantKind `json:"kinds,omitempty"`
	Operation string      `json:"operation,omitempty"`
	RunID     string      `json:"runId,omitempty"`
	RuleID    string      `json:"ruleId,omitempty"`
	RulePath  string      `json:"rulePath,omitempty"`
}

type GrantAuditor func(context.Context, GrantAuditEvent) error

type grantState struct {
	grants    map[string]DeviceGrant
	rules     []GrantRule
	grantsRaw string
	rulesRaw  string
}

type deviceGrantRecord struct {
	DeviceGrant
	Generation string `json:"generation,omitempty"`
}

type grantRuleRecord struct {
	GrantRule
	Generation string `json:"generation,omitempty"`
}

type Grants struct {
	settings               Settings
	audit                  GrantAuditor
	now                    func() time.Time
	newID                  func() string
	shared                 bool
	targetValidator        TargetValidator
	mutationValidator      MutationValidator
	environmentResolver    EnvironmentResolver
	revalidationAfterState func()
	conns                  *connAssetRegistry

	mu     sync.RWMutex
	states map[string]*grantState
}

func NewGrants(ctx context.Context, settings Settings, audit GrantAuditor, opts ...Option) (*Grants, error) {
	configuration := options{shared: true}
	for _, apply := range opts {
		apply(&configuration)
	}
	manager := &Grants{
		settings: settings, audit: audit, now: time.Now, newID: ids.New, shared: configuration.shared,
		targetValidator: configuration.targetValidator, mutationValidator: configuration.mutationValidator,
		environmentResolver: configuration.environmentResolver, revalidationAfterState: configuration.revalidationAfterState,
		conns:  newConnAssetRegistry(configuration.shared),
		states: map[string]*grantState{},
	}
	if _, err := owner.ID(ctx, manager.shared); err == nil {
		if err := manager.Reload(ctx); err != nil {
			return nil, err
		}
	}
	return manager, nil
}

// loadGrants is read-only: legacy and sync-rewritten records migrate to a
// fresh persisted generation only on their first automatic evaluation, never
// merely because a manager was constructed or reloaded.
func (g *Grants) loadGrants(ctx context.Context) (grantState, error) {
	result := grantState{grants: make(map[string]DeviceGrant)}
	raw, found, err := g.settings.SettingGet(ctx, DeviceGrantsSettingKeyForUser(ctx))
	if err != nil {
		return grantState{}, err
	}
	result.grantsRaw = raw
	if found && raw != "" {
		var records map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &records); err != nil {
			return grantState{}, err
		}
		for id, item := range records {
			var record deviceGrantRecord
			if err := json.Unmarshal(item, &record); err != nil {
				return grantState{}, err
			}
			grant := record.DeviceGrant
			grant.generation = record.Generation
			var marker struct {
				Permanent *bool `json:"permanent"`
			}
			if err := json.Unmarshal(item, &marker); err != nil {
				return grantState{}, err
			}
			if marker.Permanent == nil {
				// Records written before permanence was explicit are permanent device grants.
				grant.Permanent = true
			}
			result.grants[id] = grant
		}
	}
	raw, found, err = g.settings.SettingGet(ctx, GrantRulesSettingKeyForUser(ctx))
	if err != nil {
		return grantState{}, err
	}
	result.rulesRaw = raw
	if found && raw != "" {
		var records []grantRuleRecord
		if err := json.Unmarshal([]byte(raw), &records); err != nil {
			return grantState{}, err
		}
		for _, record := range records {
			rule := record.GrantRule
			rule.generation = record.Generation
			result.rules = append(result.rules, rule)
		}
	}
	return result, nil
}

var errGenerationConflict = errors.New("授权代际迁移期间设置已变化")

func (g *Grants) persistGenerationValue(ctx context.Context, key, expectedRaw string, value any) (string, error) {
	switch values := value.(type) {
	case map[string]DeviceGrant:
		records := make(map[string]deviceGrantRecord, len(values))
		for id, grant := range values {
			records[id] = deviceGrantRecord{DeviceGrant: grant, Generation: grant.generation}
		}
		value = records
	case []GrantRule:
		records := make([]grantRuleRecord, 0, len(values))
		for _, rule := range values {
			records = append(records, grantRuleRecord{GrantRule: rule, Generation: rule.generation})
		}
		value = records
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	updated := string(encoded)
	if settings, ok := g.settings.(interface{ GORM() *gorm.DB }); ok {
		setting := query.Use(settings.GORM()).Setting
		result, err := setting.WithContext(ctx).Where(setting.Key.Eq(key), setting.Value.Eq(expectedRaw)).UpdateColumn(setting.Value, updated)
		if err != nil {
			return updated, err
		}
		if result.RowsAffected == 0 {
			return updated, errGenerationConflict
		}
		if result.RowsAffected != 1 {
			return updated, errors.New("授权代际条件更新未恰好命中一行")
		}
		return updated, nil
	}
	settings, ok := g.settings.(interface {
		SettingCompareAndSwap(context.Context, string, string, string) (bool, error)
	})
	if !ok {
		return updated, errors.New("授权设置存储不支持条件更新")
	}
	applied, err := settings.SettingCompareAndSwap(ctx, key, expectedRaw, updated)
	if err != nil {
		return updated, err
	}
	if !applied {
		return updated, errGenerationConflict
	}
	return updated, nil
}

func (g *Grants) ensureGenerations(ctx context.Context, entry *grantState) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	nextGrants := cloneGrantMap(entry.grants)
	grantsChanged := false
	for id, grant := range nextGrants {
		if grant.generation == "" {
			grant.generation = ids.New()
			nextGrants[id] = grant
			grantsChanged = true
		}
	}
	nextRules := append([]GrantRule(nil), entry.rules...)
	rulesChanged := false
	for index, rule := range nextRules {
		if rule.generation == "" {
			rule.generation = ids.New()
			nextRules[index] = rule
			rulesChanged = true
		}
	}
	var grantsRaw string
	var grantErr error
	if grantsChanged {
		grantsRaw, grantErr = g.persistGenerationValue(ctx, DeviceGrantsSettingKeyForUser(ctx), entry.grantsRaw, nextGrants)
	}
	var rulesRaw string
	var ruleErr error
	if rulesChanged {
		rulesRaw, ruleErr = g.persistGenerationValue(ctx, GrantRulesSettingKeyForUser(ctx), entry.rulesRaw, nextRules)
	}
	if err := errors.Join(grantErr, ruleErr); err != nil {
		return err
	}
	if grantsChanged {
		entry.grants = nextGrants
		entry.grantsRaw = grantsRaw
	}
	if rulesChanged {
		entry.rules = nextRules
		entry.rulesRaw = rulesRaw
	}
	return nil
}

func (g *Grants) stateForEvaluation(ctx context.Context) (*grantState, error) {
	for {
		entry, err := g.stateFor(ctx)
		if err != nil {
			return nil, err
		}
		err = g.ensureGenerations(ctx, entry)
		if errors.Is(err, errGenerationConflict) {
			if reloadErr := g.Reload(ctx); reloadErr != nil {
				return nil, errors.Join(err, reloadErr)
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		return entry, nil
	}
}

func (g *Grants) stateFor(ctx context.Context) (*grantState, error) {
	ownerID, err := owner.ID(ctx, g.shared)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stateForLocked(ctx, ownerID)
}

func (g *Grants) stateForLocked(ctx context.Context, ownerID string) (*grantState, error) {
	if entry, ok := g.states[ownerID]; ok {
		return entry, nil
	}
	entry, err := g.loadGrants(ctx)
	if err != nil {
		return nil, err
	}
	g.states[ownerID] = &entry
	return &entry, nil
}

func (g *Grants) Reload(ctx context.Context) error {
	ownerID, err := owner.ID(ctx, g.shared)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, err := g.loadGrants(ctx)
	if err != nil {
		return err
	}
	g.states[ownerID] = &entry
	return nil
}

func (g *Grants) authorizeMutation(ctx context.Context) error {
	if g.mutationValidator != nil {
		return g.mutationValidator(ctx)
	}
	return nil
}

func (g *Grants) authorizeTarget(ctx context.Context, deviceID string) error {
	if g.targetValidator != nil {
		return g.targetValidator(ctx, deviceID)
	}
	return nil
}

func (g *Grants) Grant(ctx context.Context, deviceID string, kinds []GrantKind) (DeviceGrant, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return DeviceGrant{}, ErrGrantDeviceRequired
	}
	if len(kinds) == 0 {
		return DeviceGrant{}, errors.New("设备授权至少需要一个种类")
	}
	if err := g.authorizeMutation(ctx); err != nil {
		return DeviceGrant{}, err
	}
	if err := g.authorizeTarget(ctx, deviceID); err != nil {
		return DeviceGrant{}, err
	}
	seen := make(map[GrantKind]struct{}, len(kinds))
	normalized := make([]GrantKind, 0, len(kinds))
	for _, kind := range kinds {
		parsed, err := ParseGrantKinds([]string{string(kind)})
		if err != nil {
			return DeviceGrant{}, err
		}
		if _, ok := seen[parsed[0]]; ok {
			continue
		}
		seen[parsed[0]] = struct{}{}
		normalized = append(normalized, parsed[0])
	}
	ownerID, err := owner.ID(ctx, g.shared)
	if err != nil {
		return DeviceGrant{}, err
	}
	grant := DeviceGrant{DeviceID: deviceID, Kinds: normalized, Permanent: true, UpdatedAt: g.now().UnixMilli()}
	g.mu.Lock()
	entry, err := g.stateForLocked(ctx, ownerID)
	if err != nil {
		g.mu.Unlock()
		return DeviceGrant{}, err
	}
	grant.generation = ids.New()
	next := cloneGrantMap(entry.grants)
	next[deviceID] = grant
	if err := g.persistLocked(ctx, next); err != nil {
		g.mu.Unlock()
		return DeviceGrant{}, err
	}
	entry.grants = next
	g.mu.Unlock()
	g.emitAudit(ctx, GrantAuditEvent{Action: "grant", DeviceID: deviceID, Kinds: normalized})
	return grant, nil
}

func (g *Grants) Revoke(ctx context.Context, deviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return ErrGrantDeviceRequired
	}
	if err := g.authorizeMutation(ctx); err != nil {
		return err
	}
	if err := g.authorizeTarget(ctx, deviceID); err != nil {
		return err
	}
	ownerID, err := owner.ID(ctx, g.shared)
	if err != nil {
		return err
	}
	g.mu.Lock()
	entry, err := g.stateForLocked(ctx, ownerID)
	if err != nil {
		g.mu.Unlock()
		return err
	}
	if _, ok := entry.grants[deviceID]; !ok {
		g.mu.Unlock()
		return nil
	}
	next := cloneGrantMap(entry.grants)
	delete(next, deviceID)
	if err := g.persistLocked(ctx, next); err != nil {
		g.mu.Unlock()
		return err
	}
	entry.grants = next
	g.mu.Unlock()
	if err := recordAIGrantTombstone(ctx, g.settings, AIGrantSyncDevicePrefix+deviceID); err != nil {
		return err
	}
	g.emitAudit(ctx, GrantAuditEvent{Action: "revoke", DeviceID: deviceID})
	return nil
}

func (g *Grants) AddRule(ctx context.Context, deviceID, action, pattern string, expiresAt time.Time) (GrantRule, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return GrantRule{}, ErrRuleDeviceRequired
	}
	if !RuleActionSupported(action) {
		return GrantRule{}, ErrRuleActionUnsupported
	}
	pattern = strings.TrimSpace(pattern)
	if RuleActionNeedsPath(action) && pattern == "" {
		return GrantRule{}, ErrRulePathRequired
	}
	if !RuleActionNeedsPath(action) && pattern != "" {
		return GrantRule{}, ErrRulePathUnsupported
	}
	if err := g.authorizeMutation(ctx); err != nil {
		return GrantRule{}, err
	}
	if err := g.authorizeTarget(ctx, deviceID); err != nil {
		return GrantRule{}, err
	}
	var expiresAtMS int64
	if !expiresAt.IsZero() {
		expiresAtMS = expiresAt.UnixMilli()
		if expiresAtMS <= g.now().UnixMilli() {
			return GrantRule{}, ErrRuleExpiryInvalid
		}
	}
	ownerID, err := owner.ID(ctx, g.shared)
	if err != nil {
		return GrantRule{}, err
	}
	g.mu.Lock()
	entry, err := g.stateForLocked(ctx, ownerID)
	if err != nil {
		g.mu.Unlock()
		return GrantRule{}, err
	}
	now := g.now().UnixMilli()
	next := append([]GrantRule(nil), entry.rules...)
	for index, existing := range next {
		if existing.DeviceID != deviceID || existing.Action != action || existing.Path != pattern {
			continue
		}
		existing.ExpiresAt = expiresAtMS
		existing.UpdatedAt = now
		existing.generation = ids.New()
		next[index] = existing
		if err := g.persistRulesLocked(ctx, next); err != nil {
			g.mu.Unlock()
			return GrantRule{}, err
		}
		entry.rules = next
		g.mu.Unlock()
		g.emitAudit(ctx, GrantAuditEvent{Action: "rule_grant", DeviceID: deviceID, Operation: action, RuleID: existing.ID, RulePath: pattern})
		return existing, nil
	}
	rule := GrantRule{ID: g.newID(), DeviceID: deviceID, Action: action, Path: pattern, ExpiresAt: expiresAtMS, CreatedAt: now, UpdatedAt: now, generation: ids.New()}
	next = append(next, rule)
	if err := g.persistRulesLocked(ctx, next); err != nil {
		g.mu.Unlock()
		return GrantRule{}, err
	}
	entry.rules = next
	g.mu.Unlock()
	g.emitAudit(ctx, GrantAuditEvent{Action: "rule_grant", DeviceID: deviceID, Operation: action, RuleID: rule.ID, RulePath: pattern})
	return rule, nil
}

func (g *Grants) RevokeRule(ctx context.Context, ruleID string) error {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return ErrRuleIDRequired
	}
	if err := g.authorizeMutation(ctx); err != nil {
		return err
	}
	ownerID, err := owner.ID(ctx, g.shared)
	if err != nil {
		return err
	}
	g.mu.Lock()
	entry, err := g.stateForLocked(ctx, ownerID)
	if err != nil {
		g.mu.Unlock()
		return err
	}
	index := -1
	var previous GrantRule
	for i, rule := range entry.rules {
		if rule.ID == ruleID {
			index = i
			previous = rule
			break
		}
	}
	if index < 0 {
		g.mu.Unlock()
		return nil
	}
	if err := g.authorizeTarget(ctx, previous.DeviceID); err != nil {
		g.mu.Unlock()
		return err
	}
	next := append([]GrantRule(nil), entry.rules[:index]...)
	next = append(next, entry.rules[index+1:]...)
	if err := g.persistRulesLocked(ctx, next); err != nil {
		g.mu.Unlock()
		return err
	}
	entry.rules = next
	g.mu.Unlock()
	if err := recordAIGrantTombstone(ctx, g.settings, AIGrantSyncRulePrefix+ruleID); err != nil {
		return err
	}
	g.emitAudit(ctx, GrantAuditEvent{Action: "rule_revoke", DeviceID: previous.DeviceID, Operation: previous.Action, RuleID: previous.ID, RulePath: previous.Path})
	return nil
}

func (g *Grants) Rules(ctx context.Context) ([]GrantRule, error) {
	entry, err := g.stateFor(ctx)
	if err != nil {
		return nil, err
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]GrantRule(nil), entry.rules...), nil
}

func matchRule(entry *grantState, now time.Time, deviceID, action, path string) (GrantRule, bool) {
	for _, rule := range entry.rules {
		if rule.DeviceID == deviceID && !rule.Expired(now) && rule.Covers(action, path) {
			return rule, true
		}
	}
	return GrantRule{}, false
}

func (g *Grants) persistRulesLocked(ctx context.Context, rules []GrantRule) error {
	records := make([]grantRuleRecord, 0, len(rules))
	for _, rule := range rules {
		records = append(records, grantRuleRecord{GrantRule: rule, Generation: rule.generation})
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return g.settings.SettingSet(ctx, GrantRulesSettingKeyForUser(ctx), string(encoded))
}

func (g *Grants) Get(ctx context.Context, deviceID string) (DeviceGrant, bool, error) {
	entry, err := g.stateFor(ctx)
	if err != nil {
		return DeviceGrant{}, false, err
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	grant, ok := entry.grants[deviceID]
	if !ok {
		return DeviceGrant{}, false, nil
	}
	grant.Kinds = append([]GrantKind(nil), grant.Kinds...)
	return grant, true, nil
}

func (g *Grants) List(ctx context.Context) ([]DeviceGrant, error) {
	entry, err := g.stateFor(ctx)
	if err != nil {
		return nil, err
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]DeviceGrant, 0, len(entry.grants))
	for _, grant := range entry.grants {
		grant.Kinds = append([]GrantKind(nil), grant.Kinds...)
		result = append(result, grant)
	}
	return result, nil
}

func (g *Grants) Evaluate(ctx context.Context, config Config, ruling Ruling, memory *Memory, deviceID, operation, runID string) Decision {
	return g.EvaluateResource(ctx, config, ruling, memory, deviceID, operation, runID, Resource{})
}

func (g *Grants) EvaluateResource(ctx context.Context, config Config, ruling Ruling, memory *Memory, deviceID, operation, runID string, resource Resource) Decision {
	if ruling.Risk == Forbidden {
		return Decision{Action: ActionDeny, Ruling: ruling, Reason: ruling.Reason}
	}
	// The production classification is resolved from the request owner and the
	// real target asset through the owner's configured list, never from
	// client claims; a resolver failure denies visibly instead of letting a
	// production target pass as unclassified.
	if deviceID != "" && resource.Environment == "" && g.environmentResolver != nil {
		environment, err := g.environmentResolver(ctx, deviceID)
		if err != nil {
			return Decision{Action: ActionDeny, Ruling: ruling, Reason: err.Error()}
		}
		if environment != "" {
			production, err := IsProductionEnvironment(ctx, g.settings, environment)
			if err != nil {
				return Decision{Action: ActionDeny, Ruling: ruling, Reason: err.Error()}
			}
			resource.Environment = environment
			resource.Production = production
		}
	}
	if ruling.Risk != NeedsConfirm || deviceID == "" {
		return DecideResource(config, ruling, memory, resource)
	}
	entry, err := g.stateForEvaluation(ctx)
	if err != nil {
		return Decision{Action: ActionDeny, Ruling: ruling, Reason: err.Error()}
	}
	action := resource.Action
	if action == "" {
		action = operation
	}
	g.mu.RLock()
	rule, ruleOK := matchRule(entry, g.now(), deviceID, action, resource.Path)
	grant, grantOK := entry.grants[deviceID]
	grant.Kinds = append([]GrantKind(nil), grant.Kinds...)
	g.mu.RUnlock()
	grantCovers := grantOK && grant.Covers(OperationGrantKind(operation))
	if ruleOK || grantCovers {
		if err := g.authorizeTarget(ctx, deviceID); err != nil {
			return Decision{Action: ActionDeny, Ruling: ruling, Reason: err.Error()}
		}
	}
	if ruleOK {
		g.emitAudit(ctx, GrantAuditEvent{Action: "use_rule", DeviceID: deviceID, Operation: action, RunID: runID, RuleID: rule.ID, RulePath: rule.Path})
		return Decision{Action: ActionAllow, Ruling: ruling, Reason: "路径规则授权", Authorization: &AuthorizationBinding{Kind: "rule", DeviceID: deviceID, RuleID: rule.ID, Generation: rule.generation}}
	}
	if grantCovers {
		g.emitAudit(ctx, GrantAuditEvent{Action: "use", DeviceID: deviceID, Kinds: grant.Kinds, Operation: operation, RunID: runID})
		return Decision{Action: ActionAllow, Ruling: ruling, Reason: "设备长期授权", Authorization: &AuthorizationBinding{Kind: "grant", DeviceID: deviceID, Generation: grant.generation}}
	}
	return DecideResource(config, ruling, memory, resource)
}

// RevalidateAuthorization is the admission linearization point for an
// automatic grant or rule. A successful return admits the call; a revoke that
// completes first removes or advances the exact record and rejects the old
// decision. The registry excludes explicit one-time HITL from this automatic
// authorization check while retaining its outcome evidence.
func (g *Grants) RevalidateAuthorization(ctx context.Context, binding *AuthorizationBinding) error {
	if binding == nil {
		return nil
	}
	if binding.Generation == "" {
		return errors.New("自动授权缺少代际绑定，请重新评估")
	}
	if err := g.authorizeTarget(ctx, binding.DeviceID); err != nil {
		return err
	}
	ownerID, err := owner.ID(ctx, g.shared)
	if err != nil {
		return err
	}
	for {
		entry, err := g.stateFor(ctx)
		if err != nil {
			return err
		}
		if g.revalidationAfterState != nil {
			g.revalidationAfterState()
		}
		valid := false
		g.mu.RLock()
		if g.states[ownerID] != entry {
			g.mu.RUnlock()
			continue
		}
		switch binding.Kind {
		case "grant":
			grant, ok := entry.grants[binding.DeviceID]
			valid = ok && grant.generation == binding.Generation
		case "rule":
			for _, rule := range entry.rules {
				if rule.ID == binding.RuleID && rule.DeviceID == binding.DeviceID {
					valid = rule.generation == binding.Generation && !rule.Expired(g.now())
					break
				}
			}
		}
		g.mu.RUnlock()
		if !valid {
			return errors.New("自动授权已撤销或变化，请重新评估")
		}
		return nil
	}
}

func (g *Grants) persistLocked(ctx context.Context, grants map[string]DeviceGrant) error {
	records := make(map[string]deviceGrantRecord, len(grants))
	for id, grant := range grants {
		records[id] = deviceGrantRecord{DeviceGrant: grant, Generation: grant.generation}
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return g.settings.SettingSet(ctx, DeviceGrantsSettingKeyForUser(ctx), string(encoded))
}

func cloneGrantMap(grants map[string]DeviceGrant) map[string]DeviceGrant {
	result := make(map[string]DeviceGrant, len(grants)+1)
	for id, grant := range grants {
		grant.Kinds = append([]GrantKind(nil), grant.Kinds...)
		result[id] = grant
	}
	return result
}

func (g *Grants) emitAudit(ctx context.Context, event GrantAuditEvent) {
	if g.audit != nil {
		_ = g.audit(ctx, event)
	}
}
