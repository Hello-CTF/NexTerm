package production

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/keys"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/sshconfig"
	"github.com/Hello-CTF/NexTerm/internal/sshconfig/termiusdb"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	gossh "golang.org/x/crypto/ssh"
)

type sshImportPreviewRequest struct {
	Source    string `json:"source"`
	Path      string `json:"path"`
	Confirmed bool   `json:"confirmed"`
}

type sshImportItemAction struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

type sshImportApplyRequest struct {
	Source    string                `json:"source"`
	Path      string                `json:"path"`
	Confirmed bool                  `json:"confirmed"`
	Hosts     []sshImportItemAction `json:"hosts"`
	Keys      []sshImportItemAction `json:"keys"`
}

type sshImportHostDTO struct {
	ID             string   `json:"id"`
	Alias          string   `json:"alias"`
	Hostname       string   `json:"hostname"`
	Port           int      `json:"port"`
	Username       string   `json:"username"`
	IdentityFiles  []string `json:"identityFiles"`
	KeyName        string   `json:"keyName"`
	KeyFingerprint string   `json:"keyFingerprint"`
	ProxyJump      string   `json:"proxyJump"`
	AuthMethod     string   `json:"authMethod"`
	Source         string   `json:"source"`
	Action         string   `json:"action"`
	Warnings       []string `json:"warnings"`
}

type sshImportKeyDTO struct {
	ID          string   `json:"id"`
	Aliases     []string `json:"aliases"`
	Fingerprint string   `json:"fingerprint"`
	KeyType     string   `json:"keyType"`
	Path        string   `json:"path"`
	Source      string   `json:"source"`
	Action      string   `json:"action"`
	Warnings    []string `json:"warnings"`
}

type sshImportDiagnosticDTO struct {
	Code    string `json:"code"`
	Source  string `json:"source"`
	Message string `json:"message"`
}

type sshImportPreviewDTO struct {
	Source      string                   `json:"source"`
	Path        string                   `json:"path"`
	Hosts       []sshImportHostDTO       `json:"hosts"`
	Keys        []sshImportKeyDTO        `json:"keys"`
	Diagnostics []sshImportDiagnosticDTO `json:"diagnostics"`
	Truncated   bool                     `json:"truncated"`
}

type sshImportApplyDTO struct {
	AssetsCreated      int      `json:"assetsCreated"`
	AssetsUpdated      int      `json:"assetsUpdated"`
	CredentialsCreated int      `json:"credentialsCreated"`
	CredentialsUpdated int      `json:"credentialsUpdated"`
	Skipped            int      `json:"skipped"`
	Warnings           []string `json:"warnings"`
}

type vaultGenerateKeyRequest struct {
	Name       string `json:"name"`
	Algorithm  string `json:"algorithm"`
	Passphrase string `json:"passphrase"`
}

type generatedKeyDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"publicKey"`
}

type sshImportPlanner struct {
	database         *store.Store
	vault            *vault.Vault
	termiusKeySource func() (termiusdb.KeySource, error)
	desktop          bool
}

type sshImportPlan struct {
	preview     *sshconfig.ImportPreview
	path        string
	termiusKeys map[string]string
	existing    sshImportExisting
}

type sshImportExisting struct {
	assets        []sshconfig.ExistingAsset
	keys          []sshconfig.ExistingKey
	assetIDByName map[string]string
	credIDByName  map[string]string
	credIDByFP    map[string]string
}

func registerSSHImportCommands(dispatcher *ipc.Dispatcher, database *store.Store, credentialVault *vault.Vault, desktop bool) error {
	planner := &sshImportPlanner{database: database, vault: credentialVault, termiusKeySource: termiusdb.PlatformKeySource, desktop: desktop}
	return planner.registerCommands(dispatcher)
}

func (p *sshImportPlanner) registerCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.RegisterNested(dispatcher, "ssh_import_preview", func(ctx context.Context, _ *ipc.Call, input sshImportPreviewRequest) (sshImportPreviewDTO, error) {
				plan, err := p.derive(ctx, input.Source, input.Path, input.Confirmed, false)
				if err != nil {
					return sshImportPreviewDTO{}, err
				}
				return sshImportPreviewDTOFromPlan(plan), nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "ssh_import_apply", func(ctx context.Context, _ *ipc.Call, input sshImportApplyRequest) (sshImportApplyDTO, error) {
				return p.apply(ctx, input)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "vault_generate_key", func(ctx context.Context, _ *ipc.Call, input vaultGenerateKeyRequest) (generatedKeyDTO, error) {
				return p.generateKey(ctx, input)
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

func sshImportPreviewDTOFromPlan(plan *sshImportPlan) sshImportPreviewDTO {
	hostIDs, keyIDs := sshImportPreviewIDs(plan.preview)
	dto := sshImportPreviewDTO{
		Source:      plan.preview.Source,
		Path:        plan.path,
		Hosts:       make([]sshImportHostDTO, 0, len(plan.preview.Hosts)),
		Keys:        make([]sshImportKeyDTO, 0, len(plan.preview.Keys)),
		Diagnostics: make([]sshImportDiagnosticDTO, 0, len(plan.preview.Diagnostics)),
		Truncated:   plan.preview.Truncated,
	}
	for i, host := range plan.preview.Hosts {
		dto.Hosts = append(dto.Hosts, sshImportHostDTO{
			ID:             hostIDs[i],
			Alias:          host.Alias,
			Hostname:       host.Hostname,
			Port:           host.Port,
			Username:       host.Username,
			IdentityFiles:  host.IdentityFiles,
			KeyName:        host.KeyName,
			KeyFingerprint: host.KeyFingerprint,
			ProxyJump:      host.ProxyJump,
			AuthMethod:     host.AuthMethod,
			Source:         host.Source,
			Action:         string(host.Action),
			Warnings:       host.Warnings,
		})
	}
	for i, key := range plan.preview.Keys {
		dto.Keys = append(dto.Keys, sshImportKeyDTO{
			ID:          keyIDs[i],
			Aliases:     key.Aliases,
			Fingerprint: key.Fingerprint,
			KeyType:     key.KeyType,
			Path:        key.Path,
			Source:      key.Source,
			Action:      string(key.Action),
			Warnings:    key.Warnings,
		})
	}
	for _, diagnostic := range plan.preview.Diagnostics {
		dto.Diagnostics = append(dto.Diagnostics, sshImportDiagnosticDTO{
			Code:    diagnostic.Code,
			Source:  diagnostic.Source,
			Message: diagnostic.Message,
		})
	}
	return dto
}

func sshImportHostIdentity(item *sshconfig.HostPreview, occurrence int) string {
	return fmt.Sprintf("%s|%s@%s:%d#%d", item.Alias, item.Username, item.Hostname, item.Port, occurrence)
}

func sshImportKeyIdentity(item *sshconfig.KeyPreview, occurrence int) string {
	name := ""
	if len(item.Aliases) > 0 {
		name = item.Aliases[0]
	}
	return fmt.Sprintf("%s|%s|%s#%d", name, item.Fingerprint, item.Path, occurrence)
}

func sshImportPreviewIDs(preview *sshconfig.ImportPreview) ([]string, []string) {
	hostIDs := make([]string, len(preview.Hosts))
	hostCounts := map[string]int{}
	for i := range preview.Hosts {
		item := &preview.Hosts[i]
		base := sshImportHostIdentity(item, 0)
		hostIDs[i] = sshImportHostIdentity(item, hostCounts[base])
		hostCounts[base]++
	}
	keyIDs := make([]string, len(preview.Keys))
	keyCounts := map[string]int{}
	for i := range preview.Keys {
		item := &preview.Keys[i]
		base := sshImportKeyIdentity(item, 0)
		keyIDs[i] = sshImportKeyIdentity(item, keyCounts[base])
		keyCounts[base]++
	}
	return hostIDs, keyIDs
}

func (p *sshImportPlanner) derive(ctx context.Context, source, path string, confirmed, withMaterial bool) (*sshImportPlan, error) {
	existing, err := p.loadExisting(ctx)
	if err != nil {
		return nil, err
	}
	plan := &sshImportPlan{existing: existing}
	switch source {
	case "ssh-config":
		effective := strings.TrimSpace(path)
		if effective == "" {
			effective = sshconfig.DefaultConfigPath()
		}
		if effective == "" {
			return nil, ipc.NewError(ipc.CodeBadParam, "找不到默认 SSH 配置文件路径，请手动指定")
		}
		if err := p.guardServerImportPath(effective); err != nil {
			return nil, err
		}
		result, err := sshconfig.Parse(effective, sshconfig.Limits{})
		if err != nil {
			if os.IsNotExist(err) {
				return nil, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("找不到 SSH 配置文件 %s", effective))
			}
			return nil, ipc.NewError(ipc.CodeBadParam, "解析 SSH 配置失败: "+err.Error())
		}
		plan.preview = sshconfig.PreviewSSHConfig(result, existing.assets, existing.keys, sshconfig.Limits{})
		plan.path = effective
	case "ssh-home":
		dir := strings.TrimSpace(path)
		if dir == "" {
			dir = sshconfig.DefaultSSHHome()
		}
		if dir == "" {
			return nil, ipc.NewError(ipc.CodeBadParam, "找不到默认 SSH 目录路径，请手动指定")
		}
		if err := p.guardServerImportPath(dir); err != nil {
			return nil, err
		}
		preview, err := sshconfig.PreviewHome(dir, existing.assets, existing.keys, sshconfig.Limits{})
		if err != nil {
			return nil, ipc.NewError(ipc.CodeBadParam, "扫描 SSH 目录失败: "+err.Error())
		}
		plan.preview = preview
		plan.path = dir
	case "termius":
		if !confirmed {
			return nil, ipc.NewError(ipc.CodeBadParam, "读取本机 Termius 数据需要显式确认")
		}
		dbPath := strings.TrimSpace(path)
		effectiveDBPath := dbPath
		if effectiveDBPath == "" {
			effectiveDBPath = termiusdb.DefaultDBPath()
		}
		if err := p.guardServerImportPath(effectiveDBPath); err != nil {
			return nil, err
		}
		platformSource, err := p.termiusKeySource()
		if err != nil {
			return nil, ipc.NewError(ipc.CodeUnsupported, "当前平台不支持读取 Termius 数据: "+err.Error())
		}
		var cachedKey []byte
		keySource := termiusdb.KeySource(func() ([]byte, error) {
			if cachedKey == nil {
				key, err := platformSource()
				if err != nil {
					return nil, err
				}
				cachedKey = key
			}
			return cachedKey, nil
		})
		if withMaterial {
			_, keyRecords, err := termiusdb.Export(dbPath, keySource)
			if err != nil {
				return nil, ipc.NewError(ipc.CodeBadParam, "读取 Termius 数据失败: "+err.Error())
			}
			plan.termiusKeys = map[string]string{}
			for _, record := range keyRecords {
				fingerprint, _, err := sshconfig.FingerprintPrivateKey([]byte(record.PrivateKey))
				if err != nil {
					continue
				}
				plan.termiusKeys[fingerprint] = record.PrivateKey
			}
		}
		preview, err := sshconfig.PreviewTermius(sshconfig.TermiusOptions{DBPath: dbPath, Confirmed: true, KeySource: keySource}, existing.assets, existing.keys, sshconfig.Limits{})
		if err != nil {
			return nil, ipc.NewError(ipc.CodeBadParam, "读取 Termius 数据失败: "+err.Error())
		}
		plan.preview = preview
		plan.path = effectiveDBPath
	default:
		return nil, ipc.BadParam(fmt.Errorf("未知的导入来源 %q", source))
	}
	blockUnmappableJumps(plan.preview)
	return plan, nil
}

// guardServerImportPath 把服务端装配(nexterm-server, desktop=false)下的导入路径限制在
// 当前用户 home 的 .ssh 目录内, 拒绝越界路径, 任意路径读不能经服务端 /rpc 到达;
// 桌面端路径来自系统对话框, 不过限。
func (p *sshImportPlanner) guardServerImportPath(path string) error {
	if p.desktop {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ipc.NewError(ipc.CodeUnsupported, "服务端装配下无法定位当前用户 home 目录")
	}
	rel, err := filepath.Rel(filepath.Join(home, ".ssh"), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("服务端装配下只能导入当前用户 .ssh 目录内的路径: %s", path))
	}
	return nil
}

func blockUnmappableJumps(preview *sshconfig.ImportPreview) {
	for i := range preview.Hosts {
		host := &preview.Hosts[i]
		if host.Action != sshconfig.PlanAdd && host.Action != sshconfig.PlanConflictAlias {
			continue
		}
		if len(sshImportJumpHops(host.ProxyJump)) <= 1 {
			continue
		}
		host.Action = sshconfig.PlanBlockedJump
		host.Warnings = append(host.Warnings, "多跳 ProxyJump 暂不支持自动映射；该主机已阻止导入，请拆分单跳后重试或导入后手工配置跳板")
	}
}

func (p *sshImportPlanner) loadExisting(ctx context.Context) (sshImportExisting, error) {
	var result sshImportExisting
	rows, err := p.database.AssetList(ctx, false)
	if err != nil {
		return result, err
	}
	result.assetIDByName = map[string]string{}
	for _, row := range rows {
		if row.Kind != session.KindSSH && row.Kind != session.KindDocker {
			continue
		}
		asset := sshconfig.ExistingAsset{Name: row.Name}
		if row.Host != nil {
			asset.Host = *row.Host
		}
		if row.Port != nil {
			asset.Port = *row.Port
		}
		if row.Username != nil {
			asset.Username = *row.Username
		}
		result.assets = append(result.assets, asset)
		result.assetIDByName[strings.ToLower(row.Name)] = row.ID
	}
	credRows, err := p.database.CredentialList(ctx)
	if err != nil {
		return result, err
	}
	result.credIDByName = map[string]string{}
	result.credIDByFP = map[string]string{}
	unlocked := p.vault.Status().Unlocked
	for _, row := range credRows {
		if row.Kind != vault.KindPrivateKey {
			continue
		}
		item := sshconfig.ExistingKey{Name: row.Name}
		result.credIDByName[strings.ToLower(row.Name)] = row.ID
		if unlocked {
			if plaintext, err := p.vault.DecryptCredentialString(ctx, row); err == nil {
				payload := vault.ParsePrivateKeyPayload(plaintext)
				item.Fingerprint = productionPayloadFingerprint(payload)
				item.Path = stringValue(payload.File)
			}
		}
		if item.Fingerprint != "" {
			result.credIDByFP[item.Fingerprint] = row.ID
		}
		result.keys = append(result.keys, item)
	}
	return result, nil
}

func productionPayloadFingerprint(payload vault.PrivateKeyPayload) string {
	if payload.Key != nil {
		if fingerprint, err := productionPEMFingerprint([]byte(*payload.Key), payload.Passphrase); err == nil {
			return fingerprint
		}
	}
	if payload.File != nil && *payload.File != "" {
		return productionKeyFileFingerprint(*payload.File, payload.Passphrase)
	}
	return ""
}

func productionPEMFingerprint(pemData []byte, passphrase *string) (string, error) {
	var (
		signer gossh.Signer
		err    error
	)
	if passphrase != nil && *passphrase != "" {
		signer, err = gossh.ParsePrivateKeyWithPassphrase(pemData, []byte(*passphrase))
	} else {
		signer, err = gossh.ParsePrivateKey(pemData)
	}
	if err != nil {
		return "", err
	}
	return gossh.FingerprintSHA256(signer.PublicKey()), nil
}

func productionKeyFileFingerprint(path string, passphrase *string) string {
	if info, err := sshconfig.InspectKeyFile(path); err == nil {
		return info.Fingerprint
	}
	if passphrase == nil || *passphrase == "" {
		return ""
	}
	data, err := productionReadKeyFileBounded(path)
	if err != nil {
		return ""
	}
	fingerprint, err := productionPEMFingerprint(data, passphrase)
	if err != nil {
		return ""
	}
	return fingerprint
}

const maxImportKeyFileBytes = 1 << 20

func productionReadKeyFileBounded(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if mode := info.Mode(); mode&os.ModeSymlink != 0 {
		target, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !target.Mode().IsRegular() {
			return nil, fmt.Errorf("key file %s is not a regular file", path)
		}
	} else if !mode.IsRegular() {
		return nil, fmt.Errorf("key file %s is not a regular file", path)
	}
	if info.Size() > maxImportKeyFileBytes {
		return nil, fmt.Errorf("key file %s exceeds size limit %d", path, maxImportKeyFileBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxImportKeyFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxImportKeyFileBytes {
		return nil, fmt.Errorf("key file %s exceeds size limit %d", path, maxImportKeyFileBytes)
	}
	return data, nil
}

func (p *sshImportPlanner) apply(ctx context.Context, input sshImportApplyRequest) (sshImportApplyDTO, error) {
	result := sshImportApplyDTO{Warnings: []string{}}
	plan, err := p.derive(ctx, input.Source, input.Path, input.Confirmed, true)
	if err != nil {
		return result, err
	}
	if !p.vault.Status().Unlocked {
		return result, ipc.NewError(ipc.CodeForbidden, "凭据库已锁定，请先解锁再导入")
	}
	preview := plan.preview

	hostIDs, keyIDs := sshImportPreviewIDs(preview)
	keyByID := map[string]*sshconfig.KeyPreview{}
	for i, id := range keyIDs {
		keyByID[id] = &preview.Keys[i]
	}
	credIDByKeyFP := map[string]string{}
	overwrittenCredIDs := map[string]bool{}
	invalidateCredentialFingerprints := func(id string) {
		for fingerprint, mapped := range credIDByKeyFP {
			if mapped == id {
				delete(credIDByKeyFP, fingerprint)
			}
		}
		for fingerprint, mapped := range plan.existing.credIDByFP {
			if mapped == id {
				delete(plan.existing.credIDByFP, fingerprint)
			}
		}
	}
	type keyOutcome struct {
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
		Action      string `json:"action"`
	}
	keyOutcomes := make([]keyOutcome, 0, len(input.Keys))
	for _, selection := range input.Keys {
		item := keyByID[selection.ID]
		if item == nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("密钥条目与最新预览不一致（源数据可能已变化），已跳过: %q", selection.ID))
			result.Skipped++
			continue
		}
		name := ""
		if len(item.Aliases) > 0 {
			name = item.Aliases[0]
		}
		outcome := keyOutcome{Name: name, Fingerprint: item.Fingerprint}
		switch {
		case selection.Action == "import" && item.Action == sshconfig.PlanAdd:
			payload, err := p.importKeyPayload(plan, item)
			if err != nil {
				return result, err
			}
			id, err := putProductionCredential(ctx, p.vault, p.database, "", name, vault.KindPrivateKey, payload)
			if err != nil {
				return result, err
			}
			if item.Fingerprint != "" {
				credIDByKeyFP[item.Fingerprint] = id
			}
			result.CredentialsCreated++
			outcome.Action = "created"
		case selection.Action == "overwrite" && item.Action == sshconfig.PlanConflictAlias:
			id, ok := plan.existing.credIDByName[strings.ToLower(name)]
			if !ok {
				result.Warnings = append(result.Warnings, fmt.Sprintf("密钥 %q 的冲突对象已不存在，已跳过", name))
				result.Skipped++
				outcome.Action = "skipped"
				break
			}
			if overwrittenCredIDs[id] {
				result.Warnings = append(result.Warnings, fmt.Sprintf("凭据 %q 已在本次导入中被覆盖，跳过重复的覆盖（其材料以首次覆盖为准）", name))
				result.Skipped++
				outcome.Action = "skipped"
				break
			}
			payload, err := p.importKeyPayload(plan, item)
			if err != nil {
				return result, err
			}
			if _, err := putProductionCredential(ctx, p.vault, p.database, id, name, vault.KindPrivateKey, payload); err != nil {
				return result, err
			}
			invalidateCredentialFingerprints(id)
			if item.Fingerprint != "" {
				credIDByKeyFP[item.Fingerprint] = id
			}
			overwrittenCredIDs[id] = true
			result.CredentialsUpdated++
			outcome.Action = "updated"
		default:
			result.Skipped++
			outcome.Action = "skipped"
		}
		keyOutcomes = append(keyOutcomes, outcome)
	}

	hostByID := map[string]*sshconfig.HostPreview{}
	for i, id := range hostIDs {
		hostByID[id] = &preview.Hosts[i]
	}
	type appliedHost struct {
		item    *sshconfig.HostPreview
		assetID string
	}
	applied := make([]appliedHost, 0, len(input.Hosts))
	assetIDByAlias := map[string]string{}
	type hostOutcome struct {
		Alias  string `json:"alias"`
		Action string `json:"action"`
	}
	hostOutcomes := make([]hostOutcome, 0, len(input.Hosts))
	for _, selection := range input.Hosts {
		item := hostByID[selection.ID]
		if item == nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("主机条目与最新预览不一致（源数据可能已变化），已跳过: %q", selection.ID))
			result.Skipped++
			continue
		}
		outcome := hostOutcome{Alias: item.Alias}
		switch {
		case selection.Action == "import" && item.Action == sshconfig.PlanAdd:
			id, err := p.importHost(ctx, plan, item, credIDByKeyFP, &result)
			if err != nil {
				return result, err
			}
			applied = append(applied, appliedHost{item: item, assetID: id})
			if _, ok := assetIDByAlias[strings.ToLower(item.Alias)]; !ok {
				assetIDByAlias[strings.ToLower(item.Alias)] = id
			}
			result.AssetsCreated++
			outcome.Action = "created"
		case selection.Action == "overwrite" && item.Action == sshconfig.PlanConflictAlias:
			id, ok := plan.existing.assetIDByName[strings.ToLower(item.Alias)]
			if !ok {
				result.Warnings = append(result.Warnings, fmt.Sprintf("主机 %q 的冲突对象已不存在，已跳过", item.Alias))
				result.Skipped++
				outcome.Action = "skipped"
				break
			}
			if err := p.overwriteHost(ctx, plan, item, id, credIDByKeyFP, &result); err != nil {
				return result, err
			}
			if _, ok := assetIDByAlias[strings.ToLower(item.Alias)]; !ok {
				assetIDByAlias[strings.ToLower(item.Alias)] = id
			}
			applied = append(applied, appliedHost{item: item, assetID: id})
			result.AssetsUpdated++
			outcome.Action = "updated"
		default:
			result.Skipped++
			outcome.Action = "skipped"
		}
		hostOutcomes = append(hostOutcomes, outcome)
	}

	for _, done := range applied {
		if done.item.ProxyJump == "" {
			continue
		}
		if err := p.mapProxyJump(ctx, done.item, done.assetID, plan, assetIDByAlias, &result); err != nil {
			return result, err
		}
	}

	auditPayload := map[string]any{
		"source":             input.Source,
		"assetsCreated":      result.AssetsCreated,
		"assetsUpdated":      result.AssetsUpdated,
		"credentialsCreated": result.CredentialsCreated,
		"credentialsUpdated": result.CredentialsUpdated,
		"skipped":            result.Skipped,
		"hosts":              hostOutcomes,
		"keys":               keyOutcomes,
	}
	if err := p.database.AuditInsert(ctx, store.AuditInput{Source: "user", Kind: "ssh-import", Payload: auditPayload}); err != nil {
		return result, err
	}
	return result, nil
}

func (p *sshImportPlanner) importKeyPayload(plan *sshImportPlan, item *sshconfig.KeyPreview) (string, error) {
	if item.Source == "termius" {
		material, ok := plan.termiusKeys[item.Fingerprint]
		if !ok {
			return "", ipc.NewError(ipc.CodeInternal, fmt.Sprintf("Termius 密钥 %q 的材料不可用", item.Aliases[0]))
		}
		return vault.InlinePrivateKey(material, nil).Encode(), nil
	}
	return vault.ReferencedPrivateKey(item.Path, nil).Encode(), nil
}

func (p *sshImportPlanner) hostAuth(plan *sshImportPlan, item *sshconfig.HostPreview, credIDByKeyFP map[string]string) (authKind, credID, keyPath string, warnings []string) {
	if item.Source == "termius" {
		if item.KeyFingerprint != "" {
			if id, ok := credIDByKeyFP[item.KeyFingerprint]; ok {
				return "key", id, "", nil
			}
			if id, ok := plan.existing.credIDByFP[item.KeyFingerprint]; ok {
				return "key", id, "", nil
			}
		}
		if item.KeyName != "" || item.AuthMethod == "key" {
			warnings = append(warnings, fmt.Sprintf("主机 %q 引用的密钥未入库（被跳过、无法解析或与同名凭据指纹不同），保持未绑定", item.Alias))
			return "key", "", "", warnings
		}
		if item.AuthMethod == "password" {
			warnings = append(warnings, fmt.Sprintf("主机 %q 使用密码认证，密码不会导入，请手动绑定凭据", item.Alias))
			return "password", "", "", warnings
		}
		return "agent", "", "", nil
	}
	candidates := previewKeysForHost(plan, item)
	for _, keyItem := range candidates {
		if keyItem.Fingerprint == "" {
			continue
		}
		if id, ok := credIDByKeyFP[keyItem.Fingerprint]; ok {
			return "key", id, "", nil
		}
		if keyItem.Action == sshconfig.PlanSkipDuplicate {
			if id, ok := plan.existing.credIDByFP[keyItem.Fingerprint]; ok {
				return "key", id, "", nil
			}
		}
	}
	if len(candidates) > 0 {
		warnings = append(warnings, fmt.Sprintf("主机 %q 的 IdentityFile 未入库（被跳过或与同名凭据指纹不同）；已直接引用文件，若私钥有口令请绑定凭据", item.Alias))
		return "key", "", item.IdentityFiles[0], warnings
	}
	if len(item.IdentityFiles) > 0 {
		warnings = append(warnings, fmt.Sprintf("主机 %q 的 IdentityFile 无法检查，已直接引用文件；若私钥有口令请绑定凭据", item.Alias))
		return "key", "", item.IdentityFiles[0], warnings
	}
	return "agent", "", "", nil
}

func previewKeysForHost(plan *sshImportPlan, item *sshconfig.HostPreview) []*sshconfig.KeyPreview {
	var out []*sshconfig.KeyPreview
	for _, path := range item.IdentityFiles {
		for i := range plan.preview.Keys {
			key := &plan.preview.Keys[i]
			if key.Path == path || slices.Contains(key.AltPaths, path) {
				out = append(out, key)
				break
			}
		}
	}
	return out
}

func (p *sshImportPlanner) importHost(ctx context.Context, plan *sshImportPlan, item *sshconfig.HostPreview, credIDByKeyFP map[string]string, result *sshImportApplyDTO) (string, error) {
	authKind, credID, keyPath, warnings := p.hostAuth(plan, item, credIDByKeyFP)
	result.Warnings = append(result.Warnings, warnings...)
	input := store.AssetInput{
		Kind:        session.KindSSH,
		Name:        item.Alias,
		Host:        &item.Hostname,
		Username:    optionalString(item.Username),
		AuthKind:    optionalString(authKind),
		KeyPath:     optionalString(keyPath),
		CredID:      optionalString(credID),
		OptionsJSON: "{}",
	}
	if item.Port > 0 {
		port := int32(item.Port)
		input.Port = &port
	}
	row, err := p.database.AssetCreate(ctx, input)
	if err != nil {
		return "", err
	}
	return row.ID, nil
}

func (p *sshImportPlanner) overwriteHost(ctx context.Context, plan *sshImportPlan, item *sshconfig.HostPreview, id string, credIDByKeyFP map[string]string, result *sshImportApplyDTO) error {
	authKind, credID, keyPath, warnings := p.hostAuth(plan, item, credIDByKeyFP)
	result.Warnings = append(result.Warnings, warnings...)
	options := map[string]any{}
	if row, err := p.database.AssetGet(ctx, id); err == nil {
		if err := json.Unmarshal(store.ParseJSONOr(row.OptionsJSON), &options); err != nil {
			options = map[string]any{}
		}
	}
	delete(options, "jumpAssetId")
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return err
	}
	patch := store.AssetPatch{
		Host:        store.Value(item.Hostname),
		Username:    optionalPatch(item.Username),
		AuthKind:    optionalPatch(authKind),
		KeyPath:     optionalPatch(keyPath),
		CredID:      optionalPatch(credID),
		OptionsJSON: sshImportStringPointer(string(optionsJSON)),
	}
	if item.Port > 0 {
		patch.Port = store.Value(int32(item.Port))
	}
	_, err = p.database.AssetUpdate(ctx, id, patch)
	return err
}

func (p *sshImportPlanner) mapProxyJump(ctx context.Context, item *sshconfig.HostPreview, assetID string, plan *sshImportPlan, assetIDByAlias map[string]string, result *sshImportApplyDTO) error {
	hops := sshImportJumpHops(item.ProxyJump)
	if len(hops) != 1 {
		return nil
	}
	target := strings.ToLower(hops[0])
	jumpID, ok := plan.existing.assetIDByName[target]
	if !ok {
		jumpID, ok = assetIDByAlias[target]
	}
	if !ok {
		result.Warnings = append(result.Warnings, fmt.Sprintf("主机 %q 的跳板 %q 未能解析，已导入但未设置跳板", item.Alias, hops[0]))
		return nil
	}
	options := map[string]any{}
	if row, err := p.database.AssetGet(ctx, assetID); err == nil {
		if err := json.Unmarshal(store.ParseJSONOr(row.OptionsJSON), &options); err != nil {
			options = map[string]any{}
		}
	}
	options["jumpAssetId"] = jumpID
	raw, err := json.Marshal(options)
	if err != nil {
		return err
	}
	value := string(raw)
	_, err = p.database.AssetUpdate(ctx, assetID, store.AssetPatch{OptionsJSON: &value})
	return err
}

func sshImportJumpHops(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "none") {
		return nil
	}
	fields := strings.Split(raw, ",")
	hops := make([]string, 0, len(fields))
	for _, field := range fields {
		hop := strings.TrimSpace(field)
		if at := strings.LastIndex(hop, "@"); at >= 0 {
			hop = hop[at+1:]
		}
		if strings.HasPrefix(hop, "[") {
			if end := strings.Index(hop, "]"); end > 0 {
				hop = hop[1:end]
			}
		} else if colon := strings.LastIndex(hop, ":"); colon > 0 {
			hop = hop[:colon]
		}
		if hop != "" {
			hops = append(hops, hop)
		}
	}
	return hops
}

func (p *sshImportPlanner) generateKey(ctx context.Context, input vaultGenerateKeyRequest) (generatedKeyDTO, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return generatedKeyDTO{}, ipc.BadParam(fmt.Errorf("密钥名称不能为空"))
	}
	rows, err := p.database.CredentialList(ctx)
	if err != nil {
		return generatedKeyDTO{}, err
	}
	for _, row := range rows {
		if strings.EqualFold(row.Name, name) {
			return generatedKeyDTO{}, ipc.BadParam(fmt.Errorf("已存在同名凭据 %q", row.Name))
		}
	}
	algorithm := keys.Algorithm(strings.TrimSpace(input.Algorithm))
	passphrase := normalizedPassphrase(&input.Passphrase)
	passphraseValue := ""
	if passphrase != nil {
		passphraseValue = *passphrase
	}
	pair, err := keys.Generate(keys.Options{Algorithm: algorithm, Passphrase: passphraseValue})
	if err != nil {
		return generatedKeyDTO{}, err
	}
	payload := vault.InlinePrivateKey(string(pair.PrivateKeyPEM), passphrase)
	id, err := putProductionCredential(ctx, p.vault, p.database, "", name, vault.KindPrivateKey, payload.Encode())
	if err != nil {
		return generatedKeyDTO{}, err
	}
	return generatedKeyDTO{
		ID:          id,
		Name:        name,
		Algorithm:   string(pair.Algorithm),
		Fingerprint: pair.Fingerprint,
		PublicKey:   pair.PublicKeyLine,
	}, nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func sshImportStringPointer(value string) *string { return &value }

func optionalPatch(value string) store.Optional[string] {
	if value == "" {
		return store.Null[string]()
	}
	return store.Value(value)
}
