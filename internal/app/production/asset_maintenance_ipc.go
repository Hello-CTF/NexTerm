package production

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

const (
	publicKeyDeployAddedMarker   = "NEXTERM_PUBLIC_KEY_ADDED"
	publicKeyDeployPresentMarker = "NEXTERM_PUBLIC_KEY_ALREADY_PRESENT"
	publicKeyDeployBackupMarker  = "NEXTERM_PUBLIC_KEY_BACKUP"
	maxPublicKeyBytes            = 16 << 10
)

type AssetBulkEditIPCRequest struct {
	Targets      []store.AssetBulkEditTarget `json:"targets"`
	GroupID      *string                     `json:"groupId,omitempty"`
	Tags         *string                     `json:"tags,omitempty"`
	JumpAssetID  *string                     `json:"jumpAssetId,omitempty"`
	CredentialID *string                     `json:"credentialId,omitempty"`
	Port         *int32                      `json:"port,omitempty"`
}

type AssetBulkEditIPCResult struct {
	AssetID   string `json:"assetId"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
	Conflict  bool   `json:"conflict,omitempty"`
	Error     string `json:"error,omitempty"`
}

type AssetBulkEditIPCResponse struct {
	Results []AssetBulkEditIPCResult `json:"results"`
}

type DeployPublicKeyRequest struct {
	BatchID                string                            `json:"batchId"`
	SessionIDs             []string                          `json:"sessionIds"`
	PublicKey              string                            `json:"publicKey"`
	Path                   string                            `json:"path,omitempty"`
	TimeoutMS              int64                             `json:"timeoutMs,omitempty"`
	MaxConcurrent          int                               `json:"maxConcurrent,omitempty"`
	ProductionConfirmation *execbatch.ProductionConfirmation `json:"productionConfirmation,omitempty"`
}

func registerAssetMaintenanceCommands(dispatcher *ipc.Dispatcher, database *store.Store, batches *execbatch.Service, terminal *terminalCommandService) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "exec_batch_asset_edit", func(ctx context.Context, _ *ipc.Call, input AssetBulkEditIPCRequest) (AssetBulkEditIPCResponse, error) {
				if database == nil {
					return AssetBulkEditIPCResponse{}, ipc.NewError(ipc.CodeUnsupported, "资产批量编辑不可用")
				}
				results, err := database.AssetBulkEdit(ctx, input.Targets, assetBulkEditStorePatch(input))
				if err != nil {
					return AssetBulkEditIPCResponse{}, err
				}
				response := AssetBulkEditIPCResponse{Results: make([]AssetBulkEditIPCResult, len(results))}
				for index, result := range results {
					response.Results[index] = AssetBulkEditIPCResult{
						AssetID: result.AssetID, Conflict: result.Conflict, Error: assetBulkEditResultError(result.Err),
					}
					if result.Asset != nil {
						response.Results[index].UpdatedAt = result.Asset.UpdatedAt
					}
				}
				return response, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "exec_batch_deploy_public_key", func(ctx context.Context, _ *ipc.Call, input DeployPublicKeyRequest) (execbatch.Batch, error) {
				if batches == nil {
					return execbatch.Batch{}, ipc.NewError(ipc.CodeUnsupported, "公钥批量部署不可用")
				}
				command, err := buildPublicKeyDeployCommand(input.PublicKey, input.Path)
				if err != nil {
					return execbatch.Batch{}, err
				}
				ctx = productionSessionRequestContext(ctx)
				batch, err := batches.Execute(ctx, ipc.OwnerID(ctx), execbatch.Request{
					BatchID: input.BatchID, SessionIDs: input.SessionIDs, Command: command, TimeoutMS: input.TimeoutMS,
					MaxConcurrent: input.MaxConcurrent, ProductionConfirmation: input.ProductionConfirmation,
				})
				if err != nil {
					return execbatch.Batch{}, execBatchIPCError(err)
				}
				return batch, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "exec_batch_test_login", func(ctx context.Context, _ *ipc.Call, input assetProbeBatchRequest) (assetProbeBatchDTO, error) {
				if terminal == nil {
					return assetProbeBatchDTO{}, ipc.NewError(ipc.CodeUnsupported, "批量连接测试不可用")
				}
				return terminal.testLoginAssetBatch(ctx, input)
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func assetBulkEditStorePatch(input AssetBulkEditIPCRequest) store.AssetBulkEditPatch {
	patch := store.AssetBulkEditPatch{Tags: input.Tags}
	if input.GroupID != nil {
		patch.GroupID = assetBulkEditIDValue(*input.GroupID)
	}
	if input.JumpAssetID != nil {
		patch.JumpAssetID = assetBulkEditIDValue(*input.JumpAssetID)
	}
	if input.CredentialID != nil {
		patch.CredentialID = assetBulkEditIDValue(*input.CredentialID)
	}
	if input.Port != nil {
		patch.Port = store.Value(*input.Port)
	}
	return patch
}

func assetBulkEditIDValue(value string) store.Optional[string] {
	value = strings.TrimSpace(value)
	if value == "" {
		return store.Null[string]()
	}
	return store.Value(value)
}

func assetBulkEditResultError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, store.ErrWriteConflict) {
		return "资产已被其他修改更新，请刷新后重试"
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return appErr.Message
	}
	return err.Error()
}

func buildPublicKeyDeployCommand(publicKey, path string) (string, error) {
	publicKey = strings.TrimSpace(publicKey)
	if publicKey == "" || len(publicKey) > maxPublicKeyBytes || strings.ContainsAny(publicKey, "\r\n\x00") {
		return "", ipc.BadParam(fmt.Errorf("公钥必须是单行且不超过 16 KiB"))
	}
	_, _, _, rest, err := gossh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		return "", ipc.BadParam(fmt.Errorf("公钥不是有效的 OpenSSH authorized_keys 条目"))
	}
	pathExpression, err := authorizedKeysPathExpression(path)
	if err != nil {
		return "", err
	}
	quotedKey := shellSingleQuote(publicKey)
	command := "set -e; umask 077; nx_path=" + pathExpression + "; nx_key=" + quotedKey + "; " +
		"nx_dir=${nx_path%/*}; if [ -z \"$nx_dir\" ]; then nx_dir=/; fi; mkdir -p \"$nx_dir\"; " +
		"if [ -f \"$nx_path\" ]; then " +
		"if grep -Fqx -e \"$nx_key\" \"$nx_path\"; then printf '%s\\n' '" + publicKeyDeployPresentMarker + "'; exit 0; " +
		"else nx_grep_status=$?; if [ \"$nx_grep_status\" -ne 1 ]; then exit \"$nx_grep_status\"; fi; fi; " +
		"nx_backup=\"$nx_path.nexterm-backup-$(date +%Y%m%d%H%M%S)-$$\"; cp -p \"$nx_path\" \"$nx_backup\"; chmod a-w \"$nx_backup\"; " +
		"printf '%s %s\\n' '" + publicKeyDeployBackupMarker + "' \"$nx_backup\"; " +
		"if [ -s \"$nx_path\" ] && [ \"$(tail -c 1 \"$nx_path\" | wc -l)\" -eq 0 ]; then printf '\\n' >> \"$nx_path\"; fi; fi; " +
		"printf '%s\\n' \"$nx_key\" >> \"$nx_path\"; printf '%s\\n' '" + publicKeyDeployAddedMarker + "'"
	return command, nil
}

func authorizedKeysPathExpression(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "~/.ssh/authorized_keys"
	}
	if len(path) > 4096 || strings.ContainsAny(path, "\r\n\x00") {
		return "", ipc.BadParam(fmt.Errorf("authorized_keys 路径无效"))
	}
	if strings.HasPrefix(path, "/") {
		return shellSingleQuote(path), nil
	}
	if strings.HasPrefix(path, "~/") && len(path) > 2 {
		return "\"$HOME\"/" + shellSingleQuote(strings.TrimPrefix(path, "~/")), nil
	}
	return "", ipc.BadParam(fmt.Errorf("authorized_keys 路径必须是绝对路径或以 ~/ 开头"))
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (s *terminalCommandService) testLoginAssetBatch(ctx context.Context, input assetProbeBatchRequest) (assetProbeBatchDTO, error) {
	if len(input.AssetIDs) == 0 {
		return assetProbeBatchDTO{}, ipc.BadParam(fmt.Errorf("连接测试至少需要选择一个资产"))
	}
	if len(input.AssetIDs) > maxAssetProbeBatch {
		return assetProbeBatchDTO{}, ipc.BadParam(fmt.Errorf("连接测试一次最多 %d 个资产", maxAssetProbeBatch))
	}
	timeout := clampProbeTimeout(input.TimeoutMS, 5*time.Second)
	limit := input.MaxConcurrent
	if limit <= 0 {
		limit = defaultAssetProbeLimit
	}
	if limit > maxAssetProbeLimit {
		limit = maxAssetProbeLimit
	}

	assetIDs := dedupeAssetIDs(input.AssetIDs)
	targets := make([]assetProbeTarget, len(assetIDs))
	for index, assetID := range assetIDs {
		targets[index] = s.resolveAssetProbeTarget(ctx, assetID)
	}
	if limit > len(targets) {
		limit = len(targets)
	}

	results := make([]assetProbeResultDTO, len(targets))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range limit {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = s.testLoginAssetTarget(ctx, targets[index], timeout)
			}
		}()
	}
	for index := range targets {
		if ctx.Err() != nil {
			results[index] = canceledLoginResult(targets[index].assetID)
			continue
		}
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	return assetProbeBatchDTO{Results: results}, nil
}

func (s *terminalCommandService) testLoginAssetTarget(ctx context.Context, target assetProbeTarget, timeout time.Duration) (result assetProbeResultDTO) {
	result.AssetID = target.assetID
	if target.failure != nil {
		return localizedProbeFailure(*target.failure)
	}
	started := time.Now()
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()
	if target.kind == session.KindLocal {
		result.Reachable = true
		return result
	}
	if target.options.Host == "" {
		result.Kind = "config"
		result.Error = "资产没有填写主机地址，请先补全后再测试"
		return result
	}
	var err error
	portOnly := false
	switch target.kind {
	case session.KindSSH, session.KindDocker:
		err = s.testLoginSSHAsset(ctx, target, timeout)
	case "mysql", "postgres", "redis":
		if s.databases != nil {
			err = s.databases.TestAsset(ctx, target.assetID, timeout)
			if err != nil && ctx.Err() == nil {
				result.Kind, result.Error = db.DescribeTestFailure(err, target.kind, databaseEndpoint(target))
				slog.Warn("asset test login failed", "asset", target.assetID, "kind", result.Kind, "error", errorWithoutDetail(err))
				return result
			}
			break
		}
		portOnly = true
		err = s.probeSSHReachability(ctx, target, timeout)
	case session.KindWinRM:
		portOnly = true
		err = s.probeSSHReachability(ctx, target, timeout)
	default:
		result.Kind = "unsupported"
		result.Error = "这类资产暂不支持连接测试"
		return result
	}
	if err == nil {
		result.Reachable = true
		if portOnly {
			// Reachable without a login check; the UI must not present this as authenticated.
			result.Kind = loginKindPortOnly
			result.Error = "仅确认端口可以连通，没有验证账号和密码"
		}
		return result
	}
	if ctx.Err() != nil {
		return canceledLoginResult(target.assetID)
	}
	var connectErr *ssh.ConnectError
	if errors.As(err, &connectErr) {
		result.Kind, result.Error = ssh.DescribeLoginFailure(connectErr, portOnly)
		slog.Warn("asset test login failed", "asset", target.assetID, "kind", result.Kind, "error", connectErr)
		return result
	}
	result.Kind, result.Error = describeNonConnectLoginFailure(err)
	slog.Warn("asset test login failed", "asset", target.assetID, "kind", result.Kind, "error", err)
	return result
}

// databaseEndpoint labels a database asset for user-facing messages.
func databaseEndpoint(target assetProbeTarget) string {
	if target.options.Port > 0 {
		return net.JoinHostPort(target.options.Host, strconv.Itoa(int(target.options.Port)))
	}
	return target.options.Host
}

// errorWithoutDetail keeps driver errors out of logs: they can embed server banners
// or account names, so only the error type and ipc code are recorded.
func errorWithoutDetail(err error) string {
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return fmt.Sprintf("%T code=%s", err, appErr.Code)
	}
	return fmt.Sprintf("%T", err)
}

// loginKindPortOnly marks a reachable result that did not verify credentials.
const loginKindPortOnly = "port_only"

// describeNonConnectLoginFailure maps failures that happen before any network
// work (credential lookup, asset configuration) to fixed user-facing text.
// The underlying error is logged by the caller, never returned.
func describeNonConnectLoginFailure(err error) (kind, message string) {
	switch probeFailureKind(err) {
	case "vault":
		return "vault", "凭据库尚未解锁，或该资产绑定的凭据无法读取。请先解锁凭据库，再检查资产绑定的凭据"
	case "config":
		return "config", "资产配置不完整或无效，请检查主机、端口、用户名和认证方式"
	case "unsupported":
		return "unsupported", "当前环境不支持连接测试"
	case "not_found":
		return "not_found", "资产不存在，可能已被删除，请刷新后重试"
	}
	return "internal", "测试没有完成，请重试；如果反复出现，请查看应用日志"
}

// localizedProbeFailure rewrites pre-resolved failures from the shared probe
// resolver with the same wording the test-login results use.
func localizedProbeFailure(failure assetProbeResultDTO) assetProbeResultDTO {
	switch failure.Kind {
	case "not_found":
		failure.Error = "资产不存在，可能已被删除，请刷新后重试"
	case "config":
		failure.Error = "资产配置不完整或无效，请检查主机、端口、用户名和认证方式"
	}
	return failure
}

// testLoginSSHAsset runs a real host-key and credential handshake without
// registering a managed session. Generation 0 keeps AutoAcceptUnknownHost
// disabled so unknown host keys are never accepted, and the read-only
// ssh.TestLogin check never persists trust.
func (s *terminalCommandService) testLoginSSHAsset(ctx context.Context, target assetProbeTarget, timeout time.Duration) error {
	if s.sshConnector == nil {
		return ipc.NewError(ipc.CodeUnsupported, "SSH 连接测试不可用")
	}
	config, err := s.sshConnector.sshConfig(ctx, target.options, 0, []string{target.assetID})
	if err != nil {
		return err
	}
	stripProbeAgentForwarding(&config)
	config.ConnectTimeout = timeout
	return ssh.TestLogin(ctx, config)
}

func canceledLoginResult(assetID string) assetProbeResultDTO {
	return assetProbeResultDTO{AssetID: assetID, Kind: "canceled", Error: "已取消，没有测试"}
}
