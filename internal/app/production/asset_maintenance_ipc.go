package production

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
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
				return terminal.probeAssetBatch(ctx, input)
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
