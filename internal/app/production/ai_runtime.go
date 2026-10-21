package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	aicontext "github.com/Hello-CTF/NexTerm/internal/ai/context"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/subagent"
	"github.com/Hello-CTF/NexTerm/internal/ai/takeover"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/outcome"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/cloudwego/eino/components/model"
)

var productionMemoryScope = memory.Scope{Tenant: "local", Subject: "default"}

func productionMemoryScopeFor(desktop bool) func(context.Context) (memory.Scope, error) {
	return func(ctx context.Context) (memory.Scope, error) {
		if desktop || owner.OpenAccess(ctx) {
			return productionMemoryScope, nil
		}
		ownerID, err := owner.ID(ctx, false)
		if err != nil {
			return memory.Scope{}, err
		}
		return memory.Scope{Tenant: "user", Subject: ownerID}, nil
	}
}

type cronAgentRunner struct {
	*agent.Runner
	open bool
}

func cronExecutionContext(ctx context.Context, open bool) context.Context {
	if open && ipc.OwnerID(ctx) == "" {
		return owner.WithOpenAccess(session.WithOpenAccess(ctx))
	}
	return ctx
}

func (r cronAgentRunner) Start(ctx context.Context, args agent.ChatArgs, factory agent.StreamFactory) (agent.StartResponse, error) {
	return r.Runner.Start(cronExecutionContext(ctx, r.open), args, factory)
}

func productionGrantMutationValidator(services *ProductionServices) guard.MutationValidator {
	return func(ctx context.Context) error {
		_, err := owner.ID(ctx, services.desktop)
		return err
	}
}

func productionGrantTargetValidator(services *ProductionServices) guard.TargetValidator {
	return func(ctx context.Context, deviceID string) error {
		if _, err := owner.ID(ctx, services.desktop); err != nil {
			return err
		}
		if services.desktop || owner.OpenAccess(ctx) {
			return nil
		}
		_, err := services.Store.AssetGet(ctx, deviceID)
		if err == nil {
			return nil
		}
		if !strings.HasPrefix(deviceID, "device/") {
			var appErr *ipc.Error
			if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
				return ipc.NewError(ipc.CodeForbidden, "授权资产不存在或不属于当前用户")
			}
			return err
		}
		userID, ok := ipc.UserIDFromContext(ctx)
		if !ok {
			return ipc.NewError(ipc.CodeForbidden, "AI 请求缺少账号身份")
		}
		userDevice := query.Use(services.Store.GORM()).UserDevice
		count, err := userDevice.WithContext(ctx).
			Where(userDevice.ID.Eq(strings.TrimPrefix(deviceID, "device/")), userDevice.UserID.Eq(userID), userDevice.RevokedAt.IsNull()).
			Count()
		if err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		if count != 1 {
			return ipc.NewError(ipc.CodeForbidden, "授权设备不存在或不属于当前用户")
		}
		return nil
	}
}

func productionScopeError(err error) error {
	return ipc.WrapError(ipc.CodeForbidden, "AI 终端范围无效或无权访问", err)
}

type productionScopeDatabase interface {
	Tables(context.Context, string, string) ([]string, error)
}

type productionScopeResolver struct {
	sessions *session.Manager
	storage  *store.Store
	database productionScopeDatabase
	desktop  bool
}

func productionScopeValidator(services *ProductionServices) *productionScopeResolver {
	return productionScopeValidatorWith(services.Sessions, services.Store, services.Database, services.desktop)
}

func productionScopeValidatorWith(sessions *session.Manager, storage *store.Store, database productionScopeDatabase, desktop bool) *productionScopeResolver {
	return &productionScopeResolver{sessions: sessions, storage: storage, database: database, desktop: desktop}
}

// aiRunAssetResolver resolves the real asset identity of the tool awaiting
// confirmation from server-side state: a database tool binds the scope
// connection's live DB asset (never the tunnel viaAssetId), while any other
// tool binds the session's connected asset — even when one conversation scope
// mixes both, so pending evidence and the resume comparison match the actual
// call. An unresolvable session or connection resolves to empty, which keeps
// asset evidence unknown and confirmations fail-closed.
func aiRunAssetResolver(services *ProductionServices) func(context.Context, string, string) (string, error) {
	return func(ctx context.Context, runID, tool string) (string, error) {
		row, err := services.Store.RunGet(ctx, runID)
		if err != nil {
			return "", err
		}
		conversation, err := services.Store.ConvGet(ctx, row.ConversationID)
		if err != nil {
			return "", err
		}
		var envelope struct {
			Scope tools.Scope `json:"scope"`
		}
		if err := json.Unmarshal([]byte(conversation.ScopeJSON), &envelope); err != nil {
			return "", err
		}
		var database connAssetIdentity
		if services.Database != nil {
			database = services.Database
		}
		return resolveScopeToolAsset(ctx, database, services.Sessions, envelope.Scope, tool)
	}
}

// connAssetIdentity is the live connection identity seam behind run asset
// evidence; *db.Service implements it.
type connAssetIdentity interface {
	AssetIDOf(context.Context, string) (string, bool)
}

// resolveScopeToolAsset resolves the real asset identity a confirming tool
// acts on: database tools resolve the connection's live owner-scoped DB
// asset; every other tool resolves the session/tab asset. Both directions
// resolve to empty when the identity cannot be proven — inline, stale, or
// foreign-owner connections and session-less scopes never gain an invented
// identity.
func resolveScopeToolAsset(ctx context.Context, database connAssetIdentity, sessions *session.Manager, scope tools.Scope, tool string) (string, error) {
	if tools.DBGrantCall(tool) {
		if database == nil || scope.ConnID == "" {
			return "", nil
		}
		assetID, ok := database.AssetIDOf(ctx, scope.ConnID)
		if !ok {
			return "", nil
		}
		return assetID, nil
	}
	sessionID := scope.SessionID
	if sessionID == "" && scope.TabID != "" {
		tab, err := sessions.TabFor(ctx, scope.TabID)
		if err != nil {
			return "", err
		}
		sessionID = tab.SessionID
	}
	if sessionID == "" {
		return "", nil
	}
	connected, err := sessions.SessionFor(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return connected.Asset().ID, nil
}

// aiAssetEnvironmentResolver resolves a target asset's environment name from
// the authoritative asset row, never from client claims. A not-found row is
// tolerated only when the owner's own session evidence proves the asset is a
// synthetic session-only asset (StoreBacked=false); a deleted or replaced
// store-backed row, a foreign owner's asset, an unreadable store, or an
// asset with no session evidence all fail closed with a visible error and
// stop authorization and recovery.
func aiAssetEnvironmentResolver(services *ProductionServices) func(context.Context, string) (string, error) {
	return func(ctx context.Context, assetID string) (string, error) {
		if assetID == "" {
			return "", nil
		}
		asset, err := services.Store.AssetGetLive(ctx, assetID)
		if err != nil {
			var appErr *ipc.Error
			if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
				synthetic, serr := syntheticSessionAsset(ctx, services, assetID)
				if serr != nil {
					return "", serr
				}
				if synthetic {
					return "", nil
				}
				return "", err
			}
			return "", err
		}
		return store.AssetEnvironment(asset), nil
	}
}

// syntheticSessionAsset reports whether the asset is a session-only
// synthetic asset (StoreBacked=false), proven through the owner's own
// sessions. A store-backed asset whose row is gone, a foreign owner's asset,
// or an asset with no session evidence cannot be proven synthetic and fails
// closed.
func syntheticSessionAsset(ctx context.Context, services *ProductionServices, assetID string) (bool, error) {
	infos, err := services.Sessions.ListSessionsFor(ctx)
	if err != nil {
		return false, err
	}
	for _, info := range infos {
		if info.AssetID != assetID {
			continue
		}
		connected, err := services.Sessions.SessionFor(ctx, info.ID)
		if err != nil {
			return false, err
		}
		return !connected.Asset().StoreBacked, nil
	}
	return false, nil
}

// aiRequiredEnvironmentNames resolves the production environment names a
// confirmation must type back for a target asset, from the request owner and
// the real asset through the configurable production classification;
// non-production targets get no challenge.
func aiRequiredEnvironmentNames(services *ProductionServices) func(context.Context, string) ([]string, error) {
	environmentOf := aiAssetEnvironmentResolver(services)
	return func(ctx context.Context, assetID string) ([]string, error) {
		environment, err := environmentOf(ctx, assetID)
		if err != nil {
			return nil, err
		}
		if environment == "" {
			return nil, nil
		}
		production, err := guard.IsProductionEnvironment(ctx, services.Store, environment)
		if err != nil {
			return nil, err
		}
		if !production {
			return nil, nil
		}
		return guard.ProductionEnvironmentNames([]string{environment}), nil
	}
}

func (r *productionScopeResolver) ValidateScope(ctx context.Context, scope tools.Scope) error {
	_, _, err := r.resolve(ctx, scope, false)
	return err
}

func (r *productionScopeResolver) ResolveScope(ctx context.Context, conversationID string, scope tools.Scope) (tools.Scope, error) {
	resolved, changed, err := r.resolve(ctx, scope, conversationID != "")
	if err != nil || !changed || conversationID == "" {
		return resolved, err
	}
	if err := r.persist(ctx, conversationID, resolved); err != nil {
		return tools.Scope{}, err
	}
	return resolved, nil
}

func (r *productionScopeResolver) resolve(ctx context.Context, scope tools.Scope, allowMissingSession bool) (tools.Scope, bool, error) {
	if _, err := owner.ID(ctx, r.desktop); err != nil {
		return tools.Scope{}, false, err
	}
	validationCtx := ctx
	if ipc.OwnerID(ctx) == "" && (r.desktop || owner.OpenAccess(ctx)) {
		validationCtx = session.WithOpenAccess(ctx)
	}
	sessionID := scope.SessionID
	if r.sessions == nil && (scope.TabID != "" || sessionID != "") {
		return tools.Scope{}, false, ipc.NewError(ipc.CodeUnsupported, "终端会话服务未配置")
	}
	if r.storage == nil && scope.AssetID != "" && sessionID == "" {
		return tools.Scope{}, false, ipc.NewError(ipc.CodeUnsupported, "资产存储未配置")
	}
	var tabErr error
	if scope.TabID != "" {
		tab, err := r.sessions.TabFor(validationCtx, scope.TabID)
		if err != nil {
			if !errors.Is(err, session.ErrTabNotFound) || scope.AssetID == "" || sessionID == "" {
				return tools.Scope{}, false, productionScopeError(err)
			}
			tabErr = productionScopeError(err)
		} else {
			if sessionID != "" && tab.SessionID != sessionID {
				return tools.Scope{}, false, ipc.NewError(ipc.CodeForbidden, "AI 终端范围中的 session 与 tab 不匹配")
			}
			sessionID = tab.SessionID
		}
	}
	resolved := scope
	changed := false
	if sessionID != "" {
		connected, sessionErr := r.sessions.SessionFor(validationCtx, sessionID)
		if sessionErr != nil {
			if !errors.Is(sessionErr, session.ErrSessionNotFound) || scope.AssetID == "" || !allowMissingSession {
				return tools.Scope{}, false, productionScopeError(sessionErr)
			}
		}
		if sessionErr == nil && scope.AssetID != "" && connected.Asset().ID != scope.AssetID {
			return tools.Scope{}, false, ipc.NewError(ipc.CodeForbidden, "AI 终端范围中的 asset 与实际 session 不匹配")
		}
		if scope.AssetID != "" {
			if err := r.validateScopeAsset(validationCtx, scope.AssetID); err != nil {
				return tools.Scope{}, false, err
			}
		}
		if sessionErr == nil && (scope.AssetID == "" || connected.Info().Status == session.StatusConnected) {
			if tabErr != nil {
				return tools.Scope{}, false, tabErr
			}
			return scope, false, r.validateDatabase(validationCtx, scope.ConnID)
		}
		replacement, err := r.liveSessionForAsset(validationCtx, scope.AssetID)
		if err != nil {
			return tools.Scope{}, false, err
		}
		resolved.SessionID = replacement
		resolved.TabID = ""
		changed = true
	} else if scope.AssetID != "" {
		if err := r.validateScopeAsset(validationCtx, scope.AssetID); err != nil {
			return tools.Scope{}, false, err
		}
	}
	return resolved, changed, r.validateDatabase(validationCtx, resolved.ConnID)
}

func (r *productionScopeResolver) liveSessionForAsset(ctx context.Context, assetID string) (string, error) {
	if r.storage == nil {
		return "", ipc.NewError(ipc.CodeUnsupported, "资产存储未配置")
	}
	infos, err := r.sessions.ListSessionsFor(ctx)
	if err != nil {
		return "", productionScopeError(err)
	}
	for _, info := range infos {
		if info.AssetID != assetID || info.Status != session.StatusConnected {
			continue
		}
		connected, err := r.sessions.SessionFor(ctx, info.ID)
		if err == nil && connected.Asset().ID == assetID && connected.Info().Status == session.StatusConnected {
			return info.ID, nil
		}
	}
	return "", ipc.NewError(ipc.CodeDisconnected, "AI 终端范围对应的主机没有当前用户可用的健康在线会话；请先手动连接主机，不会自动连接或恢复旧标签")
}

func (r *productionScopeResolver) validateScopeAsset(ctx context.Context, assetID string) error {
	if r.storage == nil {
		return ipc.NewError(ipc.CodeUnsupported, "资产存储未配置")
	}
	if deviceID, ok := strings.CutPrefix(assetID, "device/"); ok {
		userDevice := query.Use(r.storage.GORM()).UserDevice
		deviceQuery := userDevice.WithContext(ctx).
			Where(userDevice.ID.Eq(deviceID), userDevice.RevokedAt.IsNull())
		if ownerID := ipc.OwnerID(ctx); ownerID != "" {
			deviceQuery = deviceQuery.Where(userDevice.UserID.Eq(ownerID))
		}
		count, err := deviceQuery.Count()
		if err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		if count != 1 {
			return ipc.NewError(ipc.CodeForbidden, "AI 终端范围中的设备不存在、已撤销或不属于当前用户")
		}
		return nil
	}
	if _, err := r.storage.AssetGetLive(ctx, assetID); err != nil {
		return productionScopeError(err)
	}
	return nil
}

func productionAITargetContext(ctx context.Context, desktop bool) (context.Context, error) {
	if _, err := owner.ID(ctx, desktop); err != nil {
		return nil, err
	}
	if ipc.OwnerID(ctx) == "" && (desktop || owner.OpenAccess(ctx)) {
		return session.WithOpenAccess(ctx), nil
	}
	return ctx, nil
}

func resolveProductionAssetSession(ctx context.Context, services *ProductionServices, assetID string) (string, error) {
	if services == nil || services.Store == nil || services.Sessions == nil {
		return "", ipc.NewError(ipc.CodeUnsupported, "按资产创建终端未配置")
	}
	validationCtx, err := productionAITargetContext(ctx, services.desktop)
	if err != nil {
		return "", err
	}
	expectedKind := session.KindDevice
	if _, device := strings.CutPrefix(assetID, "device/"); device {
		if err := productionScopeValidator(services).validateScopeAsset(validationCtx, assetID); err != nil {
			return "", err
		}
	} else {
		asset, err := services.Store.AssetGetLive(validationCtx, assetID)
		if err != nil {
			return "", productionScopeError(err)
		}
		expectedKind = asset.Kind
	}
	switch expectedKind {
	case session.KindLocal, session.KindSSH, session.KindDocker, session.KindWinRM, session.KindDevice:
	default:
		return "", ipc.NewError(ipc.CodeUnsupported, "该资产类型不支持创建终端")
	}
	sessionID, err := productionScopeValidator(services).liveSessionForAsset(validationCtx, assetID)
	if err != nil {
		// The explicit not-connected verdict maps to the tools layer's
		// ErrNoLiveSession, so terminal_create connect=true can dial through
		// the R-07 gates; forbidden, unsupported, and transient failures
		// propagate untouched and never trigger a connect.
		var appErr *ipc.Error
		if errors.As(err, &appErr) && appErr.Code == ipc.CodeDisconnected {
			return "", ipc.WrapError(ipc.CodeDisconnected, appErr.Error(), tools.ErrNoLiveSession)
		}
		return "", err
	}
	connected, err := services.Sessions.SessionFor(validationCtx, sessionID)
	if err != nil {
		return "", productionScopeError(err)
	}
	info := connected.Info()
	if connected.Asset().ID != assetID || connected.Asset().Kind != expectedKind || info.Kind != expectedKind {
		return "", ipc.NewError(ipc.CodeForbidden, "资产与在线会话类型或身份不匹配")
	}
	if info.Status != session.StatusConnected {
		return "", ipc.NewError(ipc.CodeDisconnected, "资产没有健康的在线会话；请先手动连接")
	}
	transport, err := services.Sessions.TransportFor(validationCtx, sessionID)
	if err != nil {
		return "", productionScopeError(err)
	}
	if !transport.IsAlive() {
		return "", ipc.NewError(ipc.CodeDisconnected, "资产在线会话的传输已断开；请先手动重连")
	}
	return sessionID, nil
}

func resolveProductionExecTargets(ctx context.Context, services *ProductionServices, assetIDs, tags []string) (tools.ExecTargetSet, error) {
	if services == nil || services.Store == nil || services.Sessions == nil {
		return tools.ExecTargetSet{}, ipc.NewError(ipc.CodeUnsupported, "多主机执行未配置")
	}
	validationCtx, err := productionAITargetContext(ctx, services.desktop)
	if err != nil {
		return tools.ExecTargetSet{}, err
	}
	candidates := make([]store.AssetRow, 0, len(assetIDs))
	if len(assetIDs) > 0 {
		seen := make(map[string]struct{}, len(assetIDs))
		for _, assetID := range assetIDs {
			if _, duplicate := seen[assetID]; duplicate {
				continue
			}
			seen[assetID] = struct{}{}
			asset, err := services.Store.AssetGetLive(validationCtx, assetID)
			if err != nil {
				return tools.ExecTargetSet{}, productionScopeError(err)
			}
			if asset.Kind != session.KindSSH {
				return tools.ExecTargetSet{}, ipc.NewError(ipc.CodeUnsupported, "多主机 exec_commands 只支持 SSH 资产: "+assetID)
			}
			if !productionAssetMatchesTags(asset, tags) {
				return tools.ExecTargetSet{}, ipc.NewError(ipc.CodeBadParam, "显式资产不匹配 tags 过滤: "+assetID)
			}
			candidates = append(candidates, asset)
		}
	} else {
		assets, err := services.Store.AssetList(validationCtx, false)
		if err != nil {
			return tools.ExecTargetSet{}, err
		}
		for _, asset := range assets {
			if asset.Kind == session.KindSSH && productionAssetMatchesTags(asset, tags) {
				candidates = append(candidates, asset)
			}
		}
	}
	if len(candidates) == 0 {
		return tools.ExecTargetSet{}, ipc.NewError(ipc.CodeDisconnected, "没有匹配的当前用户 SSH 资产；不会自动连接")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	targets := tools.ExecTargetSet{SessionIDs: make([]string, 0, len(candidates))}
	resolver := productionScopeValidator(services)
	connect := newSurfaceConnector(services, TargetSetConnectOptInSettingKeyForUser, TargetSetConnectProductionOptInSettingKeyForUser)
	for _, asset := range candidates {
		sessionID, err := resolver.liveSessionForAsset(validationCtx, asset.ID)
		if err != nil {
			// R-07: a disconnected asset is connected only with the target-set
			// surface's explicit opt-in and every authorization gate; the
			// decline keeps the original no-auto-connect error.
			if _, connectErr := connectSurfaceAsset(validationCtx, connect, ipc.OwnerID(validationCtx), asset.ID, err); connectErr != nil {
				return tools.ExecTargetSet{}, connectErr
			}
			sessionID, err = resolver.liveSessionForAsset(validationCtx, asset.ID)
			if err != nil {
				return tools.ExecTargetSet{}, err
			}
		}
		connected, err := services.Sessions.SessionFor(validationCtx, sessionID)
		if err != nil {
			return tools.ExecTargetSet{}, productionScopeError(err)
		}
		if connected.Asset().ID != asset.ID || connected.Asset().Kind != session.KindSSH || connected.Info().Kind != session.KindSSH {
			return tools.ExecTargetSet{}, ipc.NewError(ipc.CodeForbidden, "多主机目标与在线会话身份或类型不匹配")
		}
		targets.SessionIDs = append(targets.SessionIDs, sessionID)
		production, err := guard.IsProductionEnvironment(validationCtx, services.Store, store.AssetEnvironment(asset))
		if err != nil {
			return tools.ExecTargetSet{}, fmt.Errorf("resolve production classification for %s: %w", asset.ID, err)
		}
		targets.Production = targets.Production || production
	}
	return targets, nil
}

func productionAssetMatchesTags(asset store.AssetRow, tags []string) bool {
	for _, wanted := range tags {
		matched := false
		for _, actual := range store.AssetTagValues(asset.Tags) {
			if strings.EqualFold(actual, wanted) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func (r *productionScopeResolver) validateDatabase(ctx context.Context, connID string) error {
	if connID == "" {
		return nil
	}
	if r.database == nil {
		return ipc.NewError(ipc.CodeUnsupported, "数据库服务未配置")
	}
	_, err := r.database.Tables(ctx, connID, "")
	return err
}

func (r *productionScopeResolver) persist(ctx context.Context, conversationID string, scope tools.Scope) error {
	encoded, err := json.Marshal(map[string]any{"scope": scope})
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "AI 终端范围编码失败", err)
	}
	conversation := query.Use(r.storage.GORM()).ConversationRow
	update := conversation.WithContext(ctx).Where(conversation.ID.Eq(conversationID))
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		update = update.Where(conversation.OwnerID.Eq(ownerID))
	}
	result, err := update.UpdateColumns(map[string]any{"scope_json": string(encoded), "updated_at": time.Now().UnixMilli()})
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "AI 终端范围持久化失败", err)
	}
	if result.RowsAffected != 1 {
		return ipc.NewError(ipc.CodeNotFound, "AI 会话不存在或无权更新终端范围")
	}
	return nil
}

// The production database service must keep satisfying the tools connection
// identity seam: AI database grant evaluation resolves the real DB asset of a
// connection through it, so a method-set drift here would silently disable
// the grant branch.
var _ tools.ConnAssetIdentity = (*db.Service)(nil)

// Per-user explicit opt-in settings for the AI terminal_create connect
// surface. The surface has its own pair — the cron, broadcast, and
// target-set opt-ins never authorize an AI terminal connect — and both
// default to off.
const (
	aiTerminalConnectOptInSettingKey           = "ai.terminal_connect_opt_in"
	aiTerminalConnectProductionOptInSettingKey = "ai.terminal_connect_production"
)

func aiTerminalConnectOptInSettingKeyForUser(ctx context.Context) string {
	return connectOptInSettingKeyForUser(ctx, aiTerminalConnectOptInSettingKey)
}

func aiTerminalConnectProductionOptInSettingKeyForUser(ctx context.Context) string {
	return connectOptInSettingKeyForUser(ctx, aiTerminalConnectProductionOptInSettingKey)
}

// aiTerminalAssetConnector adapts the R-07 surface connect gate to the tools
// AssetConnector contract for terminal_create connect=true: the per-user
// opt-in, owner match, known-host-key verdict, vault readiness, and
// production confirmation all pass guard.AuthorizeConnect before the strict
// host-key dial, which never auto-accepts unknown or changed keys. A
// declined or unverifiable gate fails closed with its sentinel error instead
// of connecting.
type aiTerminalAssetConnector struct {
	services  *ProductionServices
	connector *cron.Connector
}

func newAITerminalAssetConnector(services *ProductionServices) *aiTerminalAssetConnector {
	return &aiTerminalAssetConnector{
		services:  services,
		connector: newSurfaceConnector(services, aiTerminalConnectOptInSettingKeyForUser, aiTerminalConnectProductionOptInSettingKeyForUser),
	}
}

func (c *aiTerminalAssetConnector) ConnectAsset(ctx context.Context, assetID string) (string, error) {
	validationCtx, err := productionAITargetContext(ctx, c.services.desktop)
	if err != nil {
		return "", err
	}
	return c.connector.Connect(validationCtx, cron.Trigger{OwnerID: ipc.OwnerID(validationCtx)}, assetID)
}

func composeAIRuntime(ctx context.Context, services *ProductionServices) error {
	if services.Store == nil || services.Sessions == nil || services.Profiles == nil {
		return errors.New("AI runtime requires store, sessions and profiles services")
	}
	memoryStore, err := prepareProductionMemory(ctx, services)
	if err != nil {
		return err
	}
	guardManager, err := guard.NewManager(ctx, services.Store, guard.WithShared(services.desktop), guard.WithTargetValidator(productionGrantTargetValidator(services)))
	if err != nil {
		return err
	}

	outcomeStore, err := outcome.NewSQLiteStore(services.Store.GORM())
	if err != nil {
		return err
	}
	outcomeLedger, err := outcome.New(outcome.Options{Store: outcomeStore, Auditor: services.Store.OutcomeAuditor()})
	if err != nil {
		return err
	}

	cronScheduler, cronExecutor, err := composeCronScheduler(ctx, services, cron.Options{})
	if err != nil {
		return err
	}

	toolsDeps := tools.WithSession(tools.Dependencies{LocalHistory: services.desktop}, services.Sessions)
	toolsDeps = tools.WithStore(toolsDeps, services.Store, services.Database)
	toolsDeps.ResolveAssetSession = func(ctx context.Context, assetID string) (string, error) {
		return resolveProductionAssetSession(ctx, services, assetID)
	}
	toolsDeps.ResolveExecTargets = func(ctx context.Context, assetIDs, tags []string) (tools.ExecTargetSet, error) {
		return resolveProductionExecTargets(ctx, services, assetIDs, tags)
	}
	if services.ExecBatches == nil {
		services.ExecBatches = newExecBatchServiceWithSettings(services.Sessions, services.Store, services.Store, services.Store, services.Store)
	}
	toolsDeps.ExecBatch = func(ctx context.Context, request execbatch.Request) (execbatch.Batch, error) {
		requestCtx := productionSessionRequestContext(ctx)
		return services.ExecBatches.Execute(requestCtx, ipc.OwnerID(ctx), request)
	}
	toolsDeps.Outcome = outcomeLedger
	// terminal_create connect=true connects through the R-07 surface gate:
	// the same opt-in, owner, known-host-key, vault, and production
	// confirmation evidence as the other connect surfaces, with the AI
	// terminal surface's own per-user settings.
	toolsDeps.AssetConnector = newAITerminalAssetConnector(services)
	toolsDeps.Reminders = reminderScheduler{scheduler: cronScheduler, conversations: services.Store, scopeValidator: productionScopeValidator(services)}
	// The skills backend is the single services-level manager: the ai-skills
	// IPC module registers the same instance, so the load_skill tool and the
	// IPC surface share per-owner state instead of splitting two managers. A
	// typed nil must never reach the interface field: without a manager the
	// seam stays unset and load_skill is honestly unavailable.
	if services.Skills != nil {
		toolsDeps.Skills = services.Skills
	}
	toolsDeps.Notify = func(ctx context.Context, text string) error {
		return ipc.Emit(ctx, services.Events, ipc.TopicAINotification, ipc.AINotificationEvent{OwnerID: tools.NotifyOwnerID(ctx), Text: text})
	}
	if services.Docker != nil {
		toolsDeps = tools.WithDocker(toolsDeps, services.Docker, "")
	}

	grantsManager, err := guard.NewGrants(ctx, services.Store, func(ctx context.Context, event guard.GrantAuditEvent) error {
		return toolsDeps.Audit(ctx, tools.AuditEntry{AssetID: event.DeviceID, Kind: "ai_grant", Payload: map[string]any{
			"action": event.Action, "kinds": event.Kinds, "operation": event.Operation, "runId": event.RunID,
			"ruleId": event.RuleID, "rulePath": event.RulePath,
		}})
	}, guard.WithShared(services.desktop), guard.WithTargetValidator(productionGrantTargetValidator(services)), guard.WithMutationValidator(productionGrantMutationValidator(services)), guard.WithEnvironmentResolver(aiAssetEnvironmentResolver(services)))
	if err != nil {
		return err
	}
	toolsDeps.Grants = grantsManager

	registry := tools.NewRegistry(toolsDeps)

	contextDeps := aicontext.WithSession(aicontext.Dependencies{}, services.Sessions, services.Store, services.Database)
	if services.Docker != nil {
		contextDeps = aicontext.WithDocker(contextDeps, services.Docker)
	}
	builder := aicontext.NewBuilder(contextDeps)

	roster := agent.NewRoster(services.Store, agent.NewSessionRosterProbe(services.Store, services.Sessions))

	runner := agent.NewRunner(agent.Config{
		Profiles:        services.Profiles,
		ModelForProfile: aiProfileModelFactory(services.Profiles),
		Permissions:     guardManager,
		Grants:          grantsManager,
		Permission:      unattendedPermissions(guardManager),
		Tools:           registry,
		Context:         builder,
		ScopeValidator:  productionScopeValidator(services),
		ScopeResolver:   productionScopeValidator(services),
		Store:           services.Store,
		Runs:            services.Store,
		Checkpoints:     agent.NewStoreCheckpoints(services.Store),
		Subagents: &tools.SubagentConfig{
			Model:           subagent.NewProfileModelFactory(services.Profiles),
			ModelForProfile: subagent.NewProfileModelFactoryByID(services.Profiles),
		},
		Memory:         memoryStore,
		MemoryScope:    productionMemoryScope,
		MemoryScopeFor: productionMemoryScopeFor(services.desktop),
		ServerMode:     !services.desktop,

		HITLAssetResolver:       aiRunAssetResolver(services),
		HITLEnvironmentResolver: aiRequiredEnvironmentNames(services),
		Roster:                  roster,
	})
	if err := runner.RecoverRuns(store.WithAIHistoryInternalAccess(owner.WithOpenAccess(session.WithOpenAccess(ctx)))); err != nil {
		return err
	}

	takeoverDeps := takeover.Dependencies{
		Model:       aiModelFactory(services.Profiles),
		Permission:  guardManager.Snapshot,
		DeviceMode:  guardManager.WithDeviceMode,
		Grants:      grantsManager,
		Checkpoints: agent.NewStoreCheckpoints(services.Store),
		Audit:       toolsDeps.Audit,
		Shared:      services.desktop,
	}
	takeoverDeps = takeover.WithSession(takeoverDeps, services.Sessions)
	takeoverManager := takeover.NewManager(takeoverDeps)

	services.Guard = guardManager
	if services.Sync != nil {
		services.Sync.SetPermissionsReload(guardManager.Reload)
		services.Sync.SetGrantsReload(grantsManager.Reload)
	}
	services.Agent = runner
	services.Takeover = takeoverManager

	cronExecutor.Runner = cronAgentRunner{Runner: services.Agent, open: services.desktop || owner.OpenAccess(ctx)}
	services.cron = &cronRuntime{scheduler: cronScheduler, conversations: services.Store, profiles: services.Profiles, scopeValidator: productionScopeValidator(services)}

	services.aiRelease = takeover.InstallSessionHooks(services.Sessions, takeoverManager)

	// The watch service shares the AI runtime's roster and store: executors
	// start lazily per owner, and session events drive the lifecycle
	// reconcile through the emitter hook installed in the builder.
	services.watches = newWatchService(services.Store, services.Sessions, services.Events, roster, nil)
	return nil
}

func aiModelFactory(manager *profiles.Manager) agent.ModelFactory {
	return func(ctx context.Context) (model.BaseChatModel, uint64, error) {
		if _, ok, err := manager.ActiveProfile(ctx); err != nil {
			return nil, 0, err
		} else if !ok {
			return nil, 0, errors.New("尚未选择要使用的 AI 模型档案")
		}
		client, err := manager.ActiveClient(ctx)
		if err != nil {
			return nil, 0, err
		}
		return client.BaseChatModel(ctx)
	}
}

func aiProfileModelFactory(manager *profiles.Manager) agent.ProfileModelFactory {
	return func(ctx context.Context, profileID string) (model.BaseChatModel, uint64, error) {
		client, err := manager.ClientFor(ctx, profileID)
		if err != nil {
			return nil, 0, err
		}
		return client.BaseChatModel(ctx)
	}
}

func (s *ProductionServices) closeAIRuntime() {
	if s.watches != nil {
		_ = s.watches.Shutdown(context.Background())
		s.watches = nil
	}
	if s.cron != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.cron.Shutdown(shutdownCtx)
		s.cron = nil
	}
	if s.aiRelease != nil {
		s.aiRelease()
		s.aiRelease = nil
	}
	if s.Takeover != nil {
		_ = s.Takeover.Shutdown(context.Background())
		s.Takeover = nil
	}
	if s.Agent != nil {
		_ = s.Agent.Close()
		s.Agent = nil
	}
}
