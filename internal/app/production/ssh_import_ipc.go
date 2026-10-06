package production

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/keys"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/sshconfig"
	"github.com/ProbiusOfficial/NexTerm/internal/sshconfig/termiusdb"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

type sshImportPreviewRequest struct {
	Source    string `json:"source"`
	Path      string `json:"path"`
	Confirmed bool   `json:"confirmed"`
}

type sshImportItemAction struct {
	Alias  string `json:"alias"`
	Action string `json:"action"`
}

type sshImportKeyAction struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

type sshImportApplyRequest struct {
	Source    string                `json:"source"`
	Path      string                `json:"path"`
	Confirmed bool                  `json:"confirmed"`
	Hosts     []sshImportItemAction `json:"hosts"`
	Keys      []sshImportKeyAction  `json:"keys"`
}

type sshImportHostDTO struct {
	Alias         string   `json:"alias"`
	Hostname      string   `json:"hostname"`
	Port          int      `json:"port"`
	Username      string   `json:"username"`
	IdentityFiles []string `json:"identityFiles"`
	KeyName       string   `json:"keyName"`
	ProxyJump     string   `json:"proxyJump"`
	AuthMethod    string   `json:"authMethod"`
	Source        string   `json:"source"`
	Action        string   `json:"action"`
	Warnings      []string `json:"warnings"`
}

type sshImportKeyDTO struct {
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
	database *store.Store
	vault    *vault.Vault
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

func registerSSHImportCommands(dispatcher *ipc.Dispatcher, database *store.Store, credentialVault *vault.Vault) error {
	planner := &sshImportPlanner{database: database, vault: credentialVault}
	registrations := []func() error{
		func() error {
			return ipc.RegisterNested(dispatcher, "ssh_import_preview", func(ctx context.Context, _ *ipc.Call, input sshImportPreviewRequest) (sshImportPreviewDTO, error) {
				plan, err := planner.derive(ctx, input.Source, input.Path, input.Confirmed, false)
				if err != nil {
					return sshImportPreviewDTO{}, err
				}
				return sshImportPreviewDTOFromPlan(plan), nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "ssh_import_apply", func(ctx context.Context, _ *ipc.Call, input sshImportApplyRequest) (sshImportApplyDTO, error) {
				return planner.apply(ctx, input)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "vault_generate_key", func(ctx context.Context, _ *ipc.Call, input vaultGenerateKeyRequest) (generatedKeyDTO, error) {
				return planner.generateKey(ctx, input)
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
	dto := sshImportPreviewDTO{
		Source:      plan.preview.Source,
		Path:        plan.path,
		Hosts:       make([]sshImportHostDTO, 0, len(plan.preview.Hosts)),
		Keys:        make([]sshImportKeyDTO, 0, len(plan.preview.Keys)),
		Diagnostics: make([]sshImportDiagnosticDTO, 0, len(plan.preview.Diagnostics)),
		Truncated:   plan.preview.Truncated,
	}
	for _, host := range plan.preview.Hosts {
		dto.Hosts = append(dto.Hosts, sshImportHostDTO{
			Alias:         host.Alias,
			Hostname:      host.Hostname,
			Port:          host.Port,
			Username:      host.Username,
			IdentityFiles: host.IdentityFiles,
			KeyName:       host.KeyName,
			ProxyJump:     host.ProxyJump,
			AuthMethod:    host.AuthMethod,
			Source:        host.Source,
			Action:        string(host.Action),
			Warnings:      host.Warnings,
		})
	}
	for _, key := range plan.preview.Keys {
		dto.Keys = append(dto.Keys, sshImportKeyDTO{
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
		result, err := sshconfig.Parse(effective, sshconfig.Limits{})
		if err != nil {
			if os.IsNotExist(err) {
				return nil, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("找不到 SSH 配置文件 %s", effective))
			}
			return nil, ipc.NewError(ipc.CodeBadParam, "解析 SSH 配置失败: "+err.Error())
		}
		plan.preview = sshconfig.PreviewSSHConfig(result, existing.assets, existing.keys, sshconfig.Limits{})
		plan.path = effective
		return plan, nil
	case "termius":
		if !confirmed {
			return nil, ipc.NewError(ipc.CodeBadParam, "读取本机 Termius 数据需要显式确认")
		}
		platformSource, err := termiusdb.PlatformKeySource()
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
		dbPath := strings.TrimSpace(path)
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
		plan.path = dbPath
		if plan.path == "" {
			plan.path = termiusdb.DefaultDBPath()
		}
		return plan, nil
	default:
		return nil, ipc.BadParam(fmt.Errorf("未知的导入来源 %q", source))
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
		fingerprint, _, err := sshconfig.FingerprintPrivateKey([]byte(*payload.Key))
		if err == nil {
			return fingerprint
		}
	}
	if payload.File != nil && *payload.File != "" {
		if info, err := sshconfig.InspectKeyFile(*payload.File); err == nil {
			return info.Fingerprint
		}
	}
	return ""
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

	keyByName := map[string]*sshconfig.KeyPreview{}
	for i := range preview.Keys {
		item := &preview.Keys[i]
		if len(item.Aliases) > 0 {
			keyByName[strings.ToLower(item.Aliases[0])] = item
		}
	}
	credIDByKeyName := map[string]string{}
	type keyOutcome struct {
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
		Action      string `json:"action"`
	}
	keyOutcomes := make([]keyOutcome, 0, len(input.Keys))
	for _, selection := range input.Keys {
		lower := strings.ToLower(selection.Name)
		item := keyByName[lower]
		if item == nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("密钥 %q 不在最新预览中，已跳过", selection.Name))
			result.Skipped++
			continue
		}
		outcome := keyOutcome{Name: item.Aliases[0], Fingerprint: item.Fingerprint}
		switch {
		case selection.Action == "import" && item.Action == sshconfig.PlanAdd:
			payload, err := p.importKeyPayload(plan, item)
			if err != nil {
				return result, err
			}
			id, err := putProductionCredential(ctx, p.vault, p.database, "", item.Aliases[0], vault.KindPrivateKey, payload)
			if err != nil {
				return result, err
			}
			credIDByKeyName[lower] = id
			result.CredentialsCreated++
			outcome.Action = "created"
		case selection.Action == "overwrite" && item.Action == sshconfig.PlanConflictAlias:
			id, ok := plan.existing.credIDByName[lower]
			if !ok {
				result.Warnings = append(result.Warnings, fmt.Sprintf("密钥 %q 的冲突对象已不存在，已跳过", item.Aliases[0]))
				result.Skipped++
				outcome.Action = "skipped"
				break
			}
			payload, err := p.importKeyPayload(plan, item)
			if err != nil {
				return result, err
			}
			if _, err := putProductionCredential(ctx, p.vault, p.database, id, item.Aliases[0], vault.KindPrivateKey, payload); err != nil {
				return result, err
			}
			credIDByKeyName[lower] = id
			result.CredentialsUpdated++
			outcome.Action = "updated"
		default:
			result.Skipped++
			outcome.Action = "skipped"
		}
		keyOutcomes = append(keyOutcomes, outcome)
	}

	hostByAlias := map[string]*sshconfig.HostPreview{}
	for i := range preview.Hosts {
		hostByAlias[preview.Hosts[i].Alias] = &preview.Hosts[i]
	}
	assetIDByAlias := map[string]string{}
	type hostOutcome struct {
		Alias  string `json:"alias"`
		Action string `json:"action"`
	}
	hostOutcomes := make([]hostOutcome, 0, len(input.Hosts))
	for _, selection := range input.Hosts {
		item := hostByAlias[selection.Alias]
		if item == nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("主机 %q 不在最新预览中，已跳过", selection.Alias))
			result.Skipped++
			continue
		}
		outcome := hostOutcome{Alias: item.Alias}
		switch {
		case selection.Action == "import" && item.Action == sshconfig.PlanAdd:
			id, err := p.importHost(ctx, plan, item, credIDByKeyName, &result)
			if err != nil {
				return result, err
			}
			assetIDByAlias[strings.ToLower(item.Alias)] = id
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
			if err := p.overwriteHost(ctx, plan, item, id, credIDByKeyName, &result); err != nil {
				return result, err
			}
			assetIDByAlias[strings.ToLower(item.Alias)] = id
			result.AssetsUpdated++
			outcome.Action = "updated"
		default:
			result.Skipped++
			outcome.Action = "skipped"
		}
		hostOutcomes = append(hostOutcomes, outcome)
	}

	for _, selection := range input.Hosts {
		item := hostByAlias[selection.Alias]
		if item == nil || item.ProxyJump == "" {
			continue
		}
		id, ok := assetIDByAlias[strings.ToLower(item.Alias)]
		if !ok {
			continue
		}
		if err := p.mapProxyJump(ctx, item, id, plan, assetIDByAlias, &result); err != nil {
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

func (p *sshImportPlanner) hostAuth(plan *sshImportPlan, item *sshconfig.HostPreview, credIDByKeyName map[string]string) (authKind string, credID string, warning string) {
	if item.KeyName != "" {
		lower := strings.ToLower(item.KeyName)
		if id, ok := credIDByKeyName[lower]; ok {
			return "key", id, ""
		}
		if id, ok := plan.existing.credIDByName[lower]; ok {
			return "key", id, ""
		}
		if fingerprint := firstKeyFingerprint(plan, item.KeyName); fingerprint != "" {
			if id, ok := plan.existing.credIDByFP[fingerprint]; ok {
				return "key", id, ""
			}
		}
		return "", "", fmt.Sprintf("主机 %q 引用的密钥 %q 未能入库，请手动绑定凭据", item.Alias, item.KeyName)
	}
	for _, path := range item.IdentityFiles {
		for i := range plan.preview.Keys {
			key := &plan.preview.Keys[i]
			if key.Path != path {
				continue
			}
			if len(key.Aliases) == 0 {
				continue
			}
			lower := strings.ToLower(key.Aliases[0])
			if id, ok := credIDByKeyName[lower]; ok {
				return "key", id, ""
			}
			if id, ok := plan.existing.credIDByFP[key.Fingerprint]; ok && key.Fingerprint != "" {
				return "key", id, ""
			}
			if id, ok := plan.existing.credIDByName[lower]; ok {
				return "key", id, ""
			}
		}
	}
	if len(item.IdentityFiles) > 0 {
		return "", "", fmt.Sprintf("主机 %q 的 IdentityFile 未能入库，请手动绑定凭据", item.Alias)
	}
	if item.AuthMethod == "password" {
		return "", "", fmt.Sprintf("主机 %q 使用密码认证，密码不会导入，请手动绑定凭据", item.Alias)
	}
	return "", "", ""
}

func firstKeyFingerprint(plan *sshImportPlan, name string) string {
	for i := range plan.preview.Keys {
		key := &plan.preview.Keys[i]
		if len(key.Aliases) > 0 && strings.EqualFold(key.Aliases[0], name) {
			return key.Fingerprint
		}
	}
	return ""
}

func (p *sshImportPlanner) importHost(ctx context.Context, plan *sshImportPlan, item *sshconfig.HostPreview, credIDByKeyName map[string]string, result *sshImportApplyDTO) (string, error) {
	authKind, credID, warning := p.hostAuth(plan, item, credIDByKeyName)
	if warning != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	input := store.AssetInput{
		Kind:        session.KindSSH,
		Name:        item.Alias,
		Host:        &item.Hostname,
		Username:    optionalString(item.Username),
		AuthKind:    optionalString(authKind),
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

func (p *sshImportPlanner) overwriteHost(ctx context.Context, plan *sshImportPlan, item *sshconfig.HostPreview, id string, credIDByKeyName map[string]string, result *sshImportApplyDTO) error {
	authKind, credID, warning := p.hostAuth(plan, item, credIDByKeyName)
	if warning != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	patch := store.AssetPatch{
		Host:     store.Value(item.Hostname),
		Username: optionalPatch(item.Username),
		AuthKind: optionalPatch(authKind),
		CredID:   optionalPatch(credID),
	}
	if item.Port > 0 {
		patch.Port = store.Value(int32(item.Port))
	}
	_, err := p.database.AssetUpdate(ctx, id, patch)
	return err
}

func (p *sshImportPlanner) mapProxyJump(ctx context.Context, item *sshconfig.HostPreview, assetID string, plan *sshImportPlan, assetIDByAlias map[string]string, result *sshImportApplyDTO) error {
	hops := sshImportJumpHops(item.ProxyJump)
	if len(hops) == 0 {
		return nil
	}
	if len(hops) > 1 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("主机 %q 的 ProxyJump 多跳链暂不支持，已导入但未设置跳板", item.Alias))
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

func optionalPatch(value string) store.Optional[string] {
	if value == "" {
		return store.Null[string]()
	}
	return store.Value(value)
}
