package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

// Group connection defaults.
//
// An asset group may define a username, an authentication unit (authKind, keyPath, credId) and a jump route.
// Assets below it inherit what they leave empty. Resolution always reads the stored groups, so an edit applies
// to the next connection. This file is the only place that decides what is inherited; every consumer of those
// fields resolves through ResolveAsset (or the AssetGet*Resolved helpers).

// MaxGroupDepth bounds the ancestor walk so a corrupt parent chain cannot hang resolution.
const MaxGroupDepth = 32

// Names of the inheritable fields as reported to callers.
const (
	InheritUsername = "username"
	InheritAuth     = "auth"
	InheritJump     = "jump"
	InheritAll      = "all"
)

const (
	credentialKindPrivateKey = "private_key"

	assetKindSSH    = "ssh"
	assetKindDocker = "docker"
	assetKindWinRM  = "winrm"
	assetKindRDP    = "rdp"
	assetKindVNC    = "vnc"
)

// InheritedValue reports one inheritable field the asset leaves empty and a group provides.
// Applied is false when the default does not suit the asset (Reason says why); the asset then behaves as if
// the group had set nothing.
type InheritedValue struct {
	Field     string `json:"field"`
	Applied   bool   `json:"applied"`
	Value     string `json:"value"`
	GroupID   string `json:"groupId"`
	GroupName string `json:"groupName"`
	Reason    string `json:"reason,omitempty"`
}

// ResolvedAsset is an asset row with the group defaults applied. The embedded row is the effective row to
// connect with; it must not be written back to the store.
type ResolvedAsset struct {
	AssetRow
	Inherited []InheritedValue
}

// InheritedGroup returns the name of the group a field was inherited from, or "" when the asset's own value
// (or nothing) is used.
func (r ResolvedAsset) InheritedGroup(field string) string {
	for _, item := range r.Inherited {
		if item.Field == field && item.Applied {
			return item.GroupName
		}
	}
	return ""
}

// InheritanceDraft is what the resolver needs to know about an asset. An editor sends its unsaved values so
// the preview matches what a saved asset would resolve to.
type InheritanceDraft struct {
	AssetID  string
	Kind     string
	GroupID  *string
	OwnerID  string
	Username string
	AuthKind string
	KeyPath  string
	CredID   string
	// InlineSecret marks an asset that carries its own inline password; it counts as its own authentication.
	InlineSecret bool
	JumpAssetID  string
	// PromptPassword marks an rdp or vnc asset that asks for its password at connect time; that is its own choice
	// of authentication, so no group password is applied behind it.
	PromptPassword bool
}

// AuthKindNone is the stored authKind of a winrm, rdp or vnc asset that deliberately uses no password. It counts
// as the asset's own authentication, so a group default never applies to it.
const AuthKindNone = "none"

type inheritanceOutcome struct {
	username *string
	authKind *string
	keyPath  *string
	credID   *string
	jump     *string
	fields   []InheritedValue
}

// inheritsKind reports which inheritable fields a kind uses. Kinds that are absent (local, device, databases,
// anything unknown) never inherit.
func inheritsKind(kind string) (username, auth, jump bool) {
	switch kind {
	case assetKindSSH, assetKindDocker:
		return true, true, true
	case assetKindWinRM:
		return true, true, false
	case assetKindRDP, assetKindVNC:
		return true, true, true
	}
	return false, false, false
}

// KindInheritsAuth reports whether an asset of this kind reads the group authentication default. An empty
// authKind on such an asset means "inherit" and must survive copies of the row (bundle import, sync).
func KindInheritsAuth(kind string) bool {
	_, auth, _ := inheritsKind(kind)
	return auth
}

func trimmed(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func groupHasAuth(group AssetGroupRow) bool {
	return trimmed(group.DefaultAuthKind) != "" || trimmed(group.DefaultKeyPath) != "" || trimmed(group.DefaultCredID) != ""
}

// loadOwnerGroups reads every group of one owner, keyed by ID.
func (s *Store) loadOwnerGroups(ctx context.Context, ownerID string) (map[string]AssetGroupRow, error) {
	group := query.Use(s.db).AssetGroupRow
	rows := []AssetGroupRow{}
	if err := group.WithContext(ctx).Where(group.OwnerID.Eq(ownerID)).Scan(&rows); err != nil {
		return nil, dbError(err)
	}
	byID := make(map[string]AssetGroupRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	return byID, nil
}

// groupChain returns the group and its ancestors, nearest first. The walk is bounded and cycle safe.
func groupChain(byID map[string]AssetGroupRow, groupID string) ([]AssetGroupRow, error) {
	chain := []AssetGroupRow{}
	seen := map[string]bool{}
	cursor := groupID
	for cursor != "" {
		row, ok := byID[cursor]
		if !ok {
			break
		}
		if seen[cursor] {
			return nil, badParam(fmt.Errorf("分组「%s」的上级分组形成了循环，无法解析继承的连接默认值", row.Name))
		}
		if len(chain) >= MaxGroupDepth {
			return nil, badParam(fmt.Errorf("分组嵌套超过 %d 层，无法解析继承的连接默认值", MaxGroupDepth))
		}
		seen[cursor] = true
		chain = append(chain, row)
		if row.ParentID == nil {
			break
		}
		cursor = *row.ParentID
	}
	return chain, nil
}

func groupError(group AssetGroupRow, format string, args ...any) error {
	return badParam(fmt.Errorf("分组「%s」的%s", group.Name, fmt.Sprintf(format, args...)))
}

// resolveInheritance is the single decision procedure for group defaults.
func (s *Store) resolveInheritance(ctx context.Context, draft InheritanceDraft, groups map[string]AssetGroupRow) (inheritanceOutcome, error) {
	var outcome inheritanceOutcome
	if draft.GroupID == nil || strings.TrimSpace(*draft.GroupID) == "" {
		return outcome, nil
	}
	useUsername, useAuth, useJump := inheritsKind(draft.Kind)
	inherits := useUsername || useAuth || useJump
	if groups == nil {
		loaded, err := s.loadOwnerGroups(ctx, draft.OwnerID)
		if err != nil {
			return outcome, err
		}
		groups = loaded
	}
	chain, err := groupChain(groups, strings.TrimSpace(*draft.GroupID))
	if err != nil {
		if !inherits {
			// A broken group chain only matters to assets that would read it.
			return outcome, nil
		}
		return outcome, err
	}
	if len(chain) == 0 {
		return outcome, nil
	}
	if !inherits {
		for _, group := range chain {
			if trimmed(group.DefaultUsername) != "" || groupHasAuth(group) || trimmed(group.DefaultJumpAssetID) != "" {
				outcome.fields = append(outcome.fields, InheritedValue{
					Field: InheritAll, GroupID: group.ID, GroupName: group.Name,
					Reason: "该资产类型不继承分组连接默认值",
				})
				break
			}
		}
		return outcome, nil
	}

	if useUsername && strings.TrimSpace(draft.Username) == "" {
		for _, group := range chain {
			if name := trimmed(group.DefaultUsername); name != "" {
				outcome.username = &name
				outcome.fields = append(outcome.fields, InheritedValue{
					Field: InheritUsername, Applied: true, Value: name, GroupID: group.ID, GroupName: group.Name,
				})
				break
			}
		}
	}

	// SSH and docker assets carry the whole authentication unit. WinRM, RDP and VNC only ever use a password
	// credential (their authKind is a form default, not a choice), so their own authentication is a credential,
	// an inline password, the explicit "none" choice, or (rdp, vnc) a password asked for at connect time.
	ownAuth := strings.TrimSpace(draft.CredID) != "" || draft.InlineSecret
	if draft.Kind == assetKindSSH || draft.Kind == assetKindDocker {
		ownAuth = ownAuth || strings.TrimSpace(draft.AuthKind) != "" || strings.TrimSpace(draft.KeyPath) != ""
	} else {
		ownAuth = ownAuth || strings.TrimSpace(draft.AuthKind) == AuthKindNone ||
			(draft.PromptPassword && (draft.Kind == assetKindRDP || draft.Kind == assetKindVNC))
	}
	if useAuth && !ownAuth {
		for _, group := range chain {
			if !groupHasAuth(group) {
				continue
			}
			if err := s.applyGroupAuth(ctx, draft, group, &outcome); err != nil {
				return outcome, err
			}
			break
		}
	}

	if useJump && strings.TrimSpace(draft.JumpAssetID) == "" {
		for _, group := range chain {
			jumpID := trimmed(group.DefaultJumpAssetID)
			if jumpID == "" {
				continue
			}
			if err := s.applyGroupJump(ctx, draft, group, jumpID, &outcome); err != nil {
				return outcome, err
			}
			break
		}
	}
	return outcome, nil
}

func (s *Store) groupCredential(ctx context.Context, group AssetGroupRow, credID string) (CredentialRow, error) {
	credential := query.Use(s.db).CredentialRow
	row, err := credential.WithContext(ctx).Where(credential.ID.Eq(credID)).First()
	if isNoRows(err) {
		return CredentialRow{}, groupError(group, "默认凭据不存在，请在分组连接默认值中重新选择")
	}
	if err != nil {
		return CredentialRow{}, dbError(err)
	}
	if row.OwnerID != group.OwnerID {
		return CredentialRow{}, ipc.NewError(ipc.CodeForbidden, fmt.Sprintf("分组「%s」的默认凭据不属于该分组的所有者，已拒绝使用", group.Name))
	}
	return *row, nil
}

func (s *Store) applyGroupAuth(ctx context.Context, draft InheritanceDraft, group AssetGroupRow, outcome *inheritanceOutcome) error {
	authKind, keyPath, credID := trimmed(group.DefaultAuthKind), trimmed(group.DefaultKeyPath), trimmed(group.DefaultCredID)
	item := InheritedValue{Field: InheritAuth, GroupID: group.ID, GroupName: group.Name}
	var credential CredentialRow
	if credID != "" {
		row, err := s.groupCredential(ctx, group, credID)
		if err != nil {
			return err
		}
		credential = row
	}
	switch draft.Kind {
	case assetKindRDP, assetKindVNC, assetKindWinRM:
		protocol := strings.ToUpper(draft.Kind)
		switch {
		case authKind != "" && authKind != "password":
			item.Reason = fmt.Sprintf("%s 仅支持密码凭据，已跳过分组默认的%s认证", protocol, authKindLabel(authKind))
		case credID == "":
			item.Reason = fmt.Sprintf("分组默认认证没有密码凭据，%s 无法使用", protocol)
		case credential.Kind == credentialKindPrivateKey:
			item.Reason = fmt.Sprintf("%s 仅支持密码凭据，已跳过分组默认的私钥凭据", protocol)
		default:
			outcome.credID = &credID
			item.Applied = true
			item.Value = "密码 · " + credential.Name
		}
	default:
		switch authKind {
		case "password", "keyboard-interactive":
			if credID == "" || credential.Kind == credentialKindPrivateKey {
				item.Reason = "分组默认的密码认证没有可用的密码凭据"
			}
		case "key":
			if keyPath == "" && credID == "" {
				item.Reason = "分组默认的密钥认证没有密钥路径或私钥凭据"
			} else if keyPath == "" && credential.Kind != credentialKindPrivateKey {
				item.Reason = "分组默认的密钥认证所选凭据不是私钥"
			}
		case "agent":
		default:
			item.Reason = "分组默认认证方式无效"
		}
		if item.Reason == "" {
			outcome.authKind = &authKind
			if keyPath != "" {
				outcome.keyPath = &keyPath
			}
			if credID != "" {
				outcome.credID = &credID
			}
			item.Applied = true
			item.Value = authKindLabel(authKind)
			switch {
			case credID != "":
				item.Value += " · " + credential.Name
			case keyPath != "":
				item.Value += " · " + keyPath
			}
		}
	}
	outcome.fields = append(outcome.fields, item)
	return nil
}

func authKindLabel(kind string) string {
	switch kind {
	case "password":
		return "密码"
	case "key":
		return "密钥"
	case "agent":
		return "SSH Agent"
	case "keyboard-interactive":
		return "键盘交互"
	}
	return kind
}

func (s *Store) applyGroupJump(ctx context.Context, draft InheritanceDraft, group AssetGroupRow, jumpID string, outcome *inheritanceOutcome) error {
	item := InheritedValue{Field: InheritJump, GroupID: group.ID, GroupName: group.Name}
	asset := query.Use(s.db).AssetRow
	jump, err := asset.WithContext(ctx).Where(asset.ID.Eq(jumpID)).First()
	if isNoRows(err) {
		return groupError(group, "默认跳板机不存在，请在分组连接默认值中重新选择")
	}
	if err != nil {
		return dbError(err)
	}
	if jump.OwnerID != group.OwnerID {
		return ipc.NewError(ipc.CodeForbidden, fmt.Sprintf("分组「%s」的默认跳板机不属于该分组的所有者，已拒绝使用", group.Name))
	}
	if jump.DeletedAt != nil {
		return groupError(group, "默认跳板机「%s」已删除，请在分组连接默认值中重新选择", jump.Name)
	}
	if jump.Kind != assetKindSSH && jump.Kind != assetKindDocker {
		return groupError(group, "默认跳板机「%s」不是 SSH 资产", jump.Name)
	}
	item.Value = jump.Name
	switch {
	case jump.ID == draft.AssetID:
		item.Reason = "该资产本身就是分组的默认跳板机，不再经由自己跳转"
	case (draft.Kind == assetKindRDP || draft.Kind == assetKindVNC) && jump.Kind != assetKindSSH:
		item.Reason = "远程桌面只能经由 SSH 资产跳转，已跳过分组默认跳板机"
	default:
		outcome.jump = &jump.ID
		item.Applied = true
	}
	outcome.fields = append(outcome.fields, item)
	return nil
}

// InheritancePreview resolves a draft without touching any row. The asset editor uses it to show what an
// empty field would inherit.
func (s *Store) InheritancePreview(ctx context.Context, draft InheritanceDraft) ([]InheritedValue, error) {
	if draft.OwnerID == "" {
		draft.OwnerID = ipc.OwnerID(ctx)
	}
	outcome, err := s.resolveInheritance(ctx, draft, nil)
	if err != nil {
		return nil, err
	}
	return outcome.fields, nil
}

func jumpOptionKey(kind string) string {
	if kind == assetKindRDP || kind == assetKindVNC {
		return "viaAssetId"
	}
	return "jumpAssetId"
}

func rowDraft(row AssetRow) InheritanceDraft {
	draft := InheritanceDraft{
		AssetID: row.ID, Kind: row.Kind, GroupID: row.GroupID, OwnerID: row.OwnerID,
		Username: trimmed(row.Username), AuthKind: trimmed(row.AuthKind), KeyPath: trimmed(row.KeyPath), CredID: trimmed(row.CredID),
	}
	var options map[string]any
	if json.Unmarshal(ParseJSONOr(row.OptionsJSON), &options) == nil {
		if value, ok := options[jumpOptionKey(row.Kind)].(string); ok {
			draft.JumpAssetID = strings.TrimSpace(value)
		}
		if value, ok := options["password"].(string); ok && strings.TrimSpace(value) != "" {
			draft.InlineSecret = true
		}
		if value, ok := options["promptPassword"].(bool); ok && value {
			draft.PromptPassword = true
		}
	}
	return draft
}

// ResolveAsset applies the group defaults to a stored row. A row without a group, or whose groups define
// nothing it can use, is returned unchanged.
func (s *Store) ResolveAsset(ctx context.Context, row AssetRow) (ResolvedAsset, error) {
	return s.resolveAssetWith(ctx, row, nil)
}

// ResolveAssets resolves a listing with one group read for all rows. A row whose groups cannot be resolved is
// returned unresolved; listings are informational and the connect path reports the real error.
func (s *Store) ResolveAssets(ctx context.Context, rows []AssetRow) []ResolvedAsset {
	result := make([]ResolvedAsset, len(rows))
	var groups map[string]AssetGroupRow
	for index, row := range rows {
		result[index] = ResolvedAsset{AssetRow: row}
		if row.GroupID == nil || !KindInheritsAny(row.Kind) {
			continue
		}
		if groups == nil {
			loaded, err := s.loadOwnerGroups(ctx, row.OwnerID)
			if err != nil {
				continue
			}
			groups = loaded
		}
		if resolved, err := s.resolveAssetWith(ctx, row, groups); err == nil {
			result[index] = resolved
		}
	}
	return result
}

// KindInheritsAny reports whether a kind reads any group default.
func KindInheritsAny(kind string) bool {
	username, auth, jump := inheritsKind(kind)
	return username || auth || jump
}

func (s *Store) resolveAssetWith(ctx context.Context, row AssetRow, groups map[string]AssetGroupRow) (ResolvedAsset, error) {
	resolved := ResolvedAsset{AssetRow: row}
	// Checked before any group is read, so a broken group chain cannot affect local, device or database assets.
	if row.GroupID == nil || !KindInheritsAny(row.Kind) {
		return resolved, nil
	}
	outcome, err := s.resolveInheritance(ctx, rowDraft(row), groups)
	if err != nil {
		return ResolvedAsset{}, err
	}
	resolved.Inherited = outcome.fields
	if outcome.username != nil {
		resolved.Username = outcome.username
	}
	if outcome.authKind != nil {
		resolved.AuthKind = outcome.authKind
	}
	if outcome.keyPath != nil {
		resolved.KeyPath = outcome.keyPath
	}
	if outcome.credID != nil {
		resolved.CredID = outcome.credID
	}
	if outcome.jump != nil {
		options := map[string]any{}
		if err := json.Unmarshal(ParseJSONOr(row.OptionsJSON), &options); err != nil || options == nil {
			options = map[string]any{}
		}
		options[jumpOptionKey(row.Kind)] = *outcome.jump
		encoded, err := json.Marshal(options)
		if err != nil {
			return ResolvedAsset{}, badParam(err)
		}
		resolved.OptionsJSON = string(encoded)
	}
	return resolved, nil
}

// AssetGetResolved is AssetGet with group defaults applied.
func (s *Store) AssetGetResolved(ctx context.Context, id string) (ResolvedAsset, error) {
	row, err := s.AssetGet(ctx, id)
	if err != nil {
		return ResolvedAsset{}, err
	}
	return s.ResolveAsset(ctx, row)
}

// AssetGetLiveResolved is AssetGetLive with group defaults applied.
func (s *Store) AssetGetLiveResolved(ctx context.Context, id string) (ResolvedAsset, error) {
	row, err := s.AssetGetLive(ctx, id)
	if err != nil {
		return ResolvedAsset{}, err
	}
	return s.ResolveAsset(ctx, row)
}

// GroupDefaults are the values a group editor saves. Empty strings clear a field.
type GroupDefaults struct {
	Username    string
	AuthKind    string
	KeyPath     string
	CredID      string
	JumpAssetID string
}

func (d GroupDefaults) empty() bool { return d == GroupDefaults{} }

func optionalText(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func groupDefaults(row AssetGroupRow) GroupDefaults {
	return GroupDefaults{
		Username: trimmed(row.DefaultUsername), AuthKind: trimmed(row.DefaultAuthKind), KeyPath: trimmed(row.DefaultKeyPath),
		CredID: trimmed(row.DefaultCredID), JumpAssetID: trimmed(row.DefaultJumpAssetID),
	}
}

func applyGroupDefaults(row *AssetGroupRow, defaults GroupDefaults) {
	row.DefaultUsername = optionalText(defaults.Username)
	row.DefaultAuthKind = optionalText(defaults.AuthKind)
	row.DefaultKeyPath = optionalText(defaults.KeyPath)
	row.DefaultCredID = optionalText(defaults.CredID)
	row.DefaultJumpAssetID = optionalText(defaults.JumpAssetID)
}

// validateGroupDefaults rejects a default that could never be used or that points at another owner's data.
// The check runs at save time; resolution repeats the ownership checks because data can change afterwards.
func (s *Store) validateGroupDefaults(ctx context.Context, ownerID string, defaults GroupDefaults) error {
	if defaults.empty() {
		return nil
	}
	switch defaults.AuthKind {
	case "", "password", "key", "agent", "keyboard-interactive":
	default:
		return badParam(fmt.Errorf("分组默认认证方式无效: %s", defaults.AuthKind))
	}
	var credential CredentialRow
	if defaults.CredID != "" {
		table := query.Use(s.db).CredentialRow
		row, err := table.WithContext(ctx).Where(table.ID.Eq(defaults.CredID)).First()
		if isNoRows(err) {
			return badParam(fmt.Errorf("分组默认凭据不存在"))
		}
		if err != nil {
			return dbError(err)
		}
		if row.OwnerID != ownerID {
			return ipc.NewError(ipc.CodeForbidden, "分组默认凭据不属于当前用户")
		}
		credential = *row
	}
	switch defaults.AuthKind {
	case "password", "keyboard-interactive":
		if defaults.CredID == "" {
			return badParam(fmt.Errorf("分组默认的密码认证需要选择凭据"))
		}
		if credential.Kind == credentialKindPrivateKey {
			return badParam(fmt.Errorf("分组默认的密码认证不能使用私钥凭据"))
		}
	case "key":
		if defaults.KeyPath == "" && defaults.CredID == "" {
			return badParam(fmt.Errorf("分组默认的密钥认证需要密钥路径或私钥凭据"))
		}
		if defaults.KeyPath == "" && credential.Kind != credentialKindPrivateKey {
			return badParam(fmt.Errorf("分组默认的密钥认证需要私钥凭据"))
		}
	case "agent":
	case "":
		if defaults.KeyPath != "" || defaults.CredID != "" {
			return badParam(fmt.Errorf("分组默认认证需要先选择认证方式"))
		}
	}
	if defaults.JumpAssetID != "" {
		table := query.Use(s.db).AssetRow
		jump, err := table.WithContext(ctx).Where(table.ID.Eq(defaults.JumpAssetID)).First()
		if isNoRows(err) {
			return badParam(fmt.Errorf("分组默认跳板机不存在"))
		}
		if err != nil {
			return dbError(err)
		}
		if jump.OwnerID != ownerID {
			return ipc.NewError(ipc.CodeForbidden, "分组默认跳板机不属于当前用户")
		}
		if jump.DeletedAt != nil {
			return badParam(fmt.Errorf("分组默认跳板机已删除"))
		}
		if jump.Kind != assetKindSSH && jump.Kind != assetKindDocker {
			return badParam(fmt.Errorf("分组默认跳板机必须是 SSH 资产"))
		}
	}
	return nil
}

// GroupSanitizeDefaults clears group default references that do not resolve for the group's owner (a missing or
// foreign credential, a missing, deleted or foreign jump asset) and returns one message per cleared reference.
// Bundle import calls it after the bundle's credentials and assets exist. The group revision is left alone: the
// cleared form is what the importing side can actually use.
func (s *Store) GroupSanitizeDefaults(ctx context.Context, id string) ([]string, error) {
	row, err := s.GroupGet(ctx, id)
	if err != nil {
		return nil, err
	}
	defaults := groupDefaults(row)
	var messages []string
	values := map[string]any{}
	if defaults.CredID != "" {
		table := query.Use(s.db).CredentialRow
		credential, err := table.WithContext(ctx).Where(table.ID.Eq(defaults.CredID)).First()
		if err != nil && !isNoRows(err) {
			return nil, dbError(err)
		}
		if err != nil || credential.OwnerID != row.OwnerID {
			messages = append(messages, fmt.Sprintf("分组「%s」的默认凭据不可用，已清除该默认认证", row.Name))
			values["default_cred_id"], values["default_auth_kind"], values["default_key_path"] = nil, nil, nil
		}
	}
	if defaults.JumpAssetID != "" {
		table := query.Use(s.db).AssetRow
		jump, err := table.WithContext(ctx).Where(table.ID.Eq(defaults.JumpAssetID)).First()
		if err != nil && !isNoRows(err) {
			return nil, dbError(err)
		}
		if err != nil || jump.OwnerID != row.OwnerID || jump.DeletedAt != nil {
			messages = append(messages, fmt.Sprintf("分组「%s」的默认跳板机不可用，已清除该默认值", row.Name))
			values["default_jump_asset_id"] = nil
		}
	}
	if len(values) == 0 {
		return nil, nil
	}
	group := query.Use(s.db).AssetGroupRow
	if _, err := group.WithContext(ctx).
		Where(group.ID.Eq(id)).
		Where(ownerConditions(ctx, group.TableName())...).
		UpdateColumns(values); err != nil {
		return nil, dbError(err)
	}
	return messages, nil
}

// AssetCredentialRequired reports whether connecting to the asset uses a vault credential once group defaults are
// applied. A group whose defaults cannot be resolved answers false: the connect reports the real error.
func (s *Store) AssetCredentialRequired(ctx context.Context, id string) (bool, error) {
	resolved, err := s.AssetGetLiveResolved(ctx, id)
	if err != nil {
		var appErr *ipc.Error
		if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
			return false, err
		}
		return false, nil
	}
	return resolved.CredID != nil && trimmed(resolved.CredID) != "", nil
}
