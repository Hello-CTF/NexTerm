package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/sshconfig"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type optionalIPCValue[T any] struct {
	Set   bool
	Value *T
}

func (v *optionalIPCValue[T]) UnmarshalJSON(data []byte) error {
	v.Set = true
	if string(data) == "null" {
		v.Value = nil
		return nil
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	v.Value = &value
	return nil
}

func (v optionalIPCValue[T]) storeValue() store.Optional[T] {
	return store.Optional[T]{Set: v.Set, Value: v.Value}
}

type layoutDTO struct {
	Revision  int64           `json:"revision"`
	UpdatedAt int64           `json:"updatedAt"`
	Data      json.RawMessage `json:"data"`
}

type layoutPutRequest struct {
	Data     string `json:"data"`
	Revision int64  `json:"revision"`
}

type layoutSaveDTO struct {
	Saved    bool  `json:"saved"`
	Revision int64 `json:"revision"`
	Conflict bool  `json:"conflict"`
}

type assetDTO struct {
	ID        string          `json:"id"`
	GroupID   *string         `json:"groupId"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Host      *string         `json:"host"`
	Port      *int32          `json:"port"`
	Username  *string         `json:"username"`
	AuthKind  *string         `json:"authKind"`
	KeyPath   *string         `json:"keyPath"`
	CredID    *string         `json:"credId"`
	Options   json.RawMessage `json:"options"`
	Tags      string          `json:"tags"`
	Note      string          `json:"note"`
	Sort      int64           `json:"sort"`
	CreatedAt int64           `json:"createdAt"`
	UpdatedAt int64           `json:"updatedAt"`
	DeletedAt *int64          `json:"deletedAt"`
	Builtin   bool            `json:"builtin"`
}

type assetSearchDTO struct {
	Assets    []assetDTO `json:"assets"`
	Total     int        `json:"total"`
	Truncated bool       `json:"truncated"`
}

type assetInputRequest struct {
	GroupID  *string         `json:"groupId"`
	Kind     string          `json:"kind"`
	Name     string          `json:"name"`
	Host     *string         `json:"host"`
	Port     *int32          `json:"port"`
	Username *string         `json:"username"`
	AuthKind *string         `json:"authKind"`
	KeyPath  *string         `json:"keyPath"`
	CredID   *string         `json:"credId"`
	Options  json.RawMessage `json:"options"`
	Tags     string          `json:"tags"`
	Note     string          `json:"note"`
	Sort     int64           `json:"sort"`
}

type assetPatchRequest struct {
	ID       string                   `json:"id"`
	Kind     *string                  `json:"kind"`
	GroupID  optionalIPCValue[string] `json:"groupId"`
	Name     *string                  `json:"name"`
	Host     optionalIPCValue[string] `json:"host"`
	Port     optionalIPCValue[int32]  `json:"port"`
	Username optionalIPCValue[string] `json:"username"`
	AuthKind optionalIPCValue[string] `json:"authKind"`
	KeyPath  optionalIPCValue[string] `json:"keyPath"`
	CredID   optionalIPCValue[string] `json:"credId"`
	Options  json.RawMessage          `json:"options"`
	Tags     *string                  `json:"tags"`
	Note     *string                  `json:"note"`
	Sort     *int64                   `json:"sort"`
}

type idRequest struct {
	ID   string `json:"id"`
	Q    string `json:"q"`
	Name string `json:"name"`
	Body string `json:"body"`
	Path string `json:"path"`
}

type groupDTO struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Name      string  `json:"name"`
	Sort      int64   `json:"sort"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
	// Connection defaults the group's assets inherit; references only, never a secret.
	DefaultUsername    *string `json:"defaultUsername"`
	DefaultAuthKind    *string `json:"defaultAuthKind"`
	DefaultKeyPath     *string `json:"defaultKeyPath"`
	DefaultCredID      *string `json:"defaultCredId"`
	DefaultJumpAssetID *string `json:"defaultJumpAssetId"`
}

// groupDefaultsRequest replaces all of a group's connection defaults; an absent or blank field clears it.
type groupDefaultsRequest struct {
	Username    *string `json:"username,omitempty"`
	AuthKind    *string `json:"authKind,omitempty"`
	KeyPath     *string `json:"keyPath,omitempty"`
	CredID      *string `json:"credId,omitempty"`
	JumpAssetID *string `json:"jumpAssetId,omitempty"`
}

func (r *groupDefaultsRequest) storeValue() *store.GroupDefaults {
	if r == nil {
		return nil
	}
	text := func(value *string) string {
		if value == nil {
			return ""
		}
		return strings.TrimSpace(*value)
	}
	return &store.GroupDefaults{
		Username: text(r.Username), AuthKind: text(r.AuthKind), KeyPath: text(r.KeyPath), CredID: text(r.CredID), JumpAssetID: text(r.JumpAssetID),
	}
}

type groupInputRequest struct {
	Name     string                `json:"name"`
	ParentID *string               `json:"parentId"`
	Sort     int64                 `json:"sort"`
	Defaults *groupDefaultsRequest `json:"defaults,omitempty"`
}

type groupPatchRequest struct {
	ID       string                   `json:"id"`
	Name     *string                  `json:"name"`
	ParentID optionalIPCValue[string] `json:"parentId"`
	Sort     *int64                   `json:"sort"`
	Defaults *groupDefaultsRequest    `json:"defaults,omitempty"`
}

// assetInheritanceRequest describes an asset as the editor currently shows it, saved or not.
type assetInheritanceRequest struct {
	AssetID     *string `json:"assetId,omitempty"`
	Kind        string  `json:"kind"`
	GroupID     *string `json:"groupId,omitempty"`
	Username    *string `json:"username,omitempty"`
	AuthKind    *string `json:"authKind,omitempty"`
	KeyPath     *string `json:"keyPath,omitempty"`
	CredID      *string `json:"credId,omitempty"`
	JumpAssetID *string `json:"jumpAssetId,omitempty"`
	HasSecret   bool    `json:"hasSecret,omitempty"`
	// PromptPassword marks an rdp or vnc asset that asks for its password at connect time.
	PromptPassword bool `json:"promptPassword,omitempty"`
}

type assetCredentialRequiredDTO struct {
	Required bool `json:"required"`
}

type inheritedValueDTO struct {
	Field     string `json:"field"`
	Applied   bool   `json:"applied"`
	Value     string `json:"value"`
	GroupID   string `json:"groupId"`
	GroupName string `json:"groupName"`
	Reason    string `json:"reason"`
}

// assetInheritanceDTO lists what the empty fields of the described asset would inherit. Error is set when a
// group default cannot be used (for example it points at another owner's credential); it names the group.
type assetInheritanceDTO struct {
	Fields []inheritedValueDTO `json:"fields"`
	Error  string              `json:"error"`
}

type snippetDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Body      string  `json:"body"`
	GroupID   *string `json:"groupId"`
	Sort      int64   `json:"sort"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
	// Description and Steps describe a multi-step snippet; Steps is an empty array for a plain snippet.
	Description string              `json:"description"`
	Steps       []store.SnippetStep `json:"steps"`
}

// snippetMigrationStatusDTO reports the legacy runbook templates the startup migration could not turn into
// snippets; they stay in the old table and are not usable.
type snippetMigrationStatusDTO struct {
	UnmigratedRunbookTemplates int64 `json:"unmigratedRunbookTemplates"`
}

// snippetInputRequest creates a snippet. With steps the body is derived on the server and any given body is ignored.
type snippetInputRequest struct {
	Name        string              `json:"name"`
	Body        string              `json:"body"`
	GroupID     *string             `json:"groupId"`
	Sort        int64               `json:"sort"`
	Description string              `json:"description,omitempty"`
	Steps       []store.SnippetStep `json:"steps,omitempty"`
}

// snippetPatchRequest replaces a snippet's content; omitted steps turn it into a single-step snippet.
type snippetPatchRequest struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Body        string                   `json:"body"`
	GroupID     optionalIPCValue[string] `json:"groupId"`
	Sort        *int64                   `json:"sort"`
	Description string                   `json:"description,omitempty"`
	Steps       []store.SnippetStep      `json:"steps,omitempty"`
}

type auditCountDTO struct {
	Total int64 `json:"total"`
}

type auditQueryRequest struct {
	SessionID *string `json:"sessionId"`
	AssetID   *string `json:"assetId"`
	Source    *string `json:"source"`
	Kind      *string `json:"kind"`
	Limit     int64   `json:"limit"`
	Offset    int64   `json:"offset"`
}

type auditDTO struct {
	ID         int64           `json:"id"`
	TS         int64           `json:"ts"`
	SessionID  *string         `json:"sessionId"`
	AssetID    *string         `json:"assetId"`
	Source     string          `json:"source"`
	Kind       string          `json:"kind"`
	Payload    json.RawMessage `json:"payload"`
	ExitCode   *int32          `json:"exitCode"`
	DurationMS *int64          `json:"durationMs"`
}

type commandLogQueryRequest struct {
	SessionID *string `json:"sessionId"`
	AssetID   *string `json:"assetId"`
	UserID    *string `json:"userId"`
	Limit     int64   `json:"limit"`
	Offset    int64   `json:"offset"`
}

type commandLogDTO struct {
	ID         int64   `json:"id"`
	SessionID  string  `json:"sessionId"`
	TabID      string  `json:"tabId"`
	AssetID    string  `json:"assetId"`
	UserID     *string `json:"userId"`
	Command    string  `json:"command"`
	Source     string  `json:"source"`
	ExitCode   *int32  `json:"exitCode"`
	StartedAt  int64   `json:"startedAt"`
	FinishedAt int64   `json:"finishedAt"`
}

type knownHostRequest struct {
	Host        string `json:"host"`
	Port        int32  `json:"port"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	Key         string `json:"key"`
}

type desktopTrayDTO struct {
	Enabled bool `json:"enabled"`
}

type desktopTraySetRequest struct {
	Enabled bool `json:"enabled"`
}

// DesktopCloseToTraySettingKey is a device-level setting ("1"/"0", vault-style)
// controlling whether closing the desktop window hides it to the system tray.
const DesktopCloseToTraySettingKey = "desktop.close_to_tray"

// DesktopCloseToTray defaults to quitting when no valid preference is saved.
func DesktopCloseToTray(ctx context.Context, database *store.Store) (bool, error) {
	raw, found, err := database.SettingGet(ctx, DesktopCloseToTraySettingKey)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	return raw == "1", nil
}

func registerStoreCommands(dispatcher *ipc.Dispatcher, database *store.Store, hostKeys *productionHostKeyStore, dataDir string, desktop bool) error {
	return registerStoreCommandsWithHostKeyMode(dispatcher, database, hostKeys, dataDir, desktop, desktop)
}

func registerStoreCommandsWithHostKeyMode(dispatcher *ipc.Dispatcher, database *store.Store, hostKeys *productionHostKeyStore, dataDir string, desktop, hostKeysShared bool) error {
	assetFromRow := func(row store.AssetRow) assetDTO { return productionAssetDTO(row) }
	assetsFromRows := func(rows []store.AssetRow) []assetDTO {
		result := make([]assetDTO, len(rows))
		for index, row := range rows {
			result[index] = assetFromRow(row)
		}
		return result
	}
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "layout_get", func(ctx context.Context, _ *ipc.Call, _ struct{}) (layoutDTO, error) {
				revision, updatedAt, data, err := database.LayoutLoad(ctx)
				return layoutDTO{Revision: revision, UpdatedAt: updatedAt, Data: data}, err
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "layout_put", func(ctx context.Context, call *ipc.Call, input layoutPutRequest) (layoutSaveDTO, error) {
				saved, revision, err := database.LayoutSave(ctx, input.Revision, input.Data)
				if err == nil && saved {
					_ = ipc.Emit(ctx, call.Events, ipc.TopicLayoutChanged, ipc.LayoutChangedEvent{Revision: revision})
				}
				return layoutSaveDTO{Saved: saved, Revision: revision, Conflict: !saved}, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "asset_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]assetDTO, error) {
				rows, err := database.AssetList(ctx, false)
				return assetsFromRows(rows), err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "asset_get", func(ctx context.Context, _ *ipc.Call, input idRequest) (assetDTO, error) {
				row, err := database.AssetGet(ctx, input.ID)
				return assetFromRow(row), err
			})
		},
		func() error {
			// Whether connecting uses a vault credential once group defaults are applied; the client asks this to
			// decide on the unlock prompt instead of looking at the asset's own credential field.
			return ipc.Register(dispatcher, "asset_credential_required", func(ctx context.Context, _ *ipc.Call, input idRequest) (assetCredentialRequiredDTO, error) {
				required, err := database.AssetCredentialRequired(ctx, input.ID)
				return assetCredentialRequiredDTO{Required: required}, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "asset_inheritance_preview", func(ctx context.Context, _ *ipc.Call, input assetInheritanceRequest) (assetInheritanceDTO, error) {
				text := func(value *string) string {
					if value == nil {
						return ""
					}
					return strings.TrimSpace(*value)
				}
				draft := store.InheritanceDraft{
					AssetID: text(input.AssetID), Kind: input.Kind, GroupID: input.GroupID, Username: text(input.Username),
					AuthKind: text(input.AuthKind), KeyPath: text(input.KeyPath), CredID: text(input.CredID),
					InlineSecret: input.HasSecret, PromptPassword: input.PromptPassword, JumpAssetID: text(input.JumpAssetID), OwnerID: ipc.OwnerID(ctx),
				}
				if input.GroupID != nil {
					// A group the caller cannot see is not a source of defaults.
					if _, err := database.GroupGet(ctx, *input.GroupID); err != nil {
						return assetInheritanceDTO{Fields: []inheritedValueDTO{}}, nil
					}
				}
				fields, err := database.InheritancePreview(ctx, draft)
				result := assetInheritanceDTO{Fields: make([]inheritedValueDTO, 0, len(fields))}
				if err != nil {
					var appErr *ipc.Error
					if errors.As(err, &appErr) && (appErr.Code == ipc.CodeBadParam || appErr.Code == ipc.CodeForbidden) {
						result.Error = appErr.Message
						return result, nil
					}
					return assetInheritanceDTO{}, err
				}
				for _, field := range fields {
					result.Fields = append(result.Fields, inheritedValueDTO{
						Field: field.Field, Applied: field.Applied, Value: field.Value,
						GroupID: field.GroupID, GroupName: field.GroupName, Reason: field.Reason,
					})
				}
				return result, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "asset_create", func(ctx context.Context, _ *ipc.Call, input assetInputRequest) (assetDTO, error) {
				row, err := database.AssetCreate(ctx, store.AssetInput{
					GroupID: input.GroupID, Kind: input.Kind, Name: input.Name, Host: input.Host, Port: input.Port,
					Username: input.Username, AuthKind: input.AuthKind, KeyPath: input.KeyPath, CredID: input.CredID,
					OptionsJSON: productionJSONText(input.Options, "{}"), Tags: input.Tags, Note: input.Note, Sort: input.Sort,
				})
				return assetFromRow(row), err
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "asset_update", func(ctx context.Context, _ *ipc.Call, input assetPatchRequest) (assetDTO, error) {
				patch := store.AssetPatch{
					Kind: input.Kind, GroupID: input.GroupID.storeValue(), Name: input.Name, Host: input.Host.storeValue(), Port: input.Port.storeValue(),
					Username: input.Username.storeValue(), AuthKind: input.AuthKind.storeValue(), KeyPath: input.KeyPath.storeValue(), CredID: input.CredID.storeValue(),
					Tags: input.Tags, Note: input.Note, Sort: input.Sort,
				}
				if input.Options != nil {
					value := productionJSONText(input.Options, "{}")
					patch.OptionsJSON = &value
				}
				row, err := database.AssetUpdate(ctx, input.ID, patch)
				return assetFromRow(row), err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "asset_delete", func(ctx context.Context, _ *ipc.Call, input idRequest) (any, error) {
				return nil, database.AssetDelete(ctx, input.ID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "asset_search", func(ctx context.Context, _ *ipc.Call, input idRequest) ([]assetDTO, error) {
				rows, err := database.AssetSearch(ctx, input.Q)
				return assetsFromRows(rows), err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "asset_search_meta", func(ctx context.Context, _ *ipc.Call, input idRequest) (assetSearchDTO, error) {
				result, err := database.AssetSearchWithMeta(ctx, input.Q)
				if err != nil {
					return assetSearchDTO{}, err
				}
				return assetSearchDTO{Assets: assetsFromRows(result.Assets), Total: result.Total, Truncated: result.Truncated}, nil
			})
		},
		func() error {
			// Paths come from the desktop system dialog; when desktop is false (nexterm-server assembly), they are always refused,
			// so arbitrary path reads cannot reach through the server-side /rpc, and the web side gets content via browser file selection.
			return ipc.Register(dispatcher, "asset_read_key_file", func(_ context.Context, _ *ipc.Call, input idRequest) (string, error) {
				if !desktop {
					return "", ipc.NewError(ipc.CodeUnsupported, "读取本地密钥文件只在桌面端可用")
				}
				file, err := sshconfig.OpenSharedDelete(input.Path)
				if err != nil {
					return "", err
				}
				defer file.Close()
				data, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
				if err != nil {
					return "", err
				}
				if len(data) > 64<<10 {
					return "", ipc.BadParam(fmt.Errorf("私钥内容超过 64 KiB 上限"))
				}
				return string(data), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "group_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]groupDTO, error) {
				rows, err := database.GroupList(ctx)
				result := make([]groupDTO, len(rows))
				for index, row := range rows {
					result[index] = productionGroupDTO(row)
				}
				return result, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "group_create", func(ctx context.Context, _ *ipc.Call, input groupInputRequest) (groupDTO, error) {
				row, err := database.GroupCreate(ctx, store.GroupInput{ParentID: input.ParentID, Name: input.Name, Sort: input.Sort, Defaults: input.Defaults.storeValue()})
				return productionGroupDTO(row), err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "group_update", func(ctx context.Context, _ *ipc.Call, input groupPatchRequest) (groupDTO, error) {
				row, err := database.GroupUpdate(ctx, input.ID, store.GroupPatch{Name: input.Name, ParentID: input.ParentID.storeValue(), Sort: input.Sort, Defaults: input.Defaults.storeValue()})
				return productionGroupDTO(row), err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "group_delete", func(ctx context.Context, _ *ipc.Call, input idRequest) (any, error) {
				return nil, database.GroupDelete(ctx, input.ID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "snippet_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]snippetDTO, error) {
				rows, err := database.SnippetList(ctx)
				result := make([]snippetDTO, len(rows))
				for index, row := range rows {
					result[index] = productionSnippetDTO(row)
				}
				return result, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "snippet_create", func(ctx context.Context, _ *ipc.Call, input snippetInputRequest) (map[string]string, error) {
				name, err := validatedSnippetName(input.Name)
				if err != nil {
					return nil, err
				}
				if err := validateSnippetBody(input.Body, input.Steps); err != nil {
					return nil, err
				}
				row, err := database.SnippetCreateContent(ctx, name, store.SnippetContent{Description: input.Description, Body: input.Body, Steps: input.Steps}, input.GroupID, input.Sort)
				return map[string]string{"id": row.ID}, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "snippet_update", func(ctx context.Context, _ *ipc.Call, input snippetPatchRequest) (any, error) {
				name, err := validatedSnippetName(input.Name)
				if err != nil {
					return nil, err
				}
				if err := validateSnippetBody(input.Body, input.Steps); err != nil {
					return nil, err
				}
				return nil, database.SnippetUpdateFields(ctx, input.ID, store.SnippetPatch{
					Name: name, Body: input.Body, Description: input.Description, Steps: input.Steps, GroupID: input.GroupID.storeValue(), Sort: input.Sort,
				})
			})
		},
		func() error {
			return ipc.Register(dispatcher, "snippet_migration_status", func(ctx context.Context, _ *ipc.Call, _ struct{}) (snippetMigrationStatusDTO, error) {
				count, err := database.RunbookTemplateUnmigratedCount(ctx)
				return snippetMigrationStatusDTO{UnmigratedRunbookTemplates: count}, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "snippet_delete", func(ctx context.Context, _ *ipc.Call, input idRequest) (any, error) {
				return nil, database.SnippetDelete(ctx, input.ID)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "audit_query", func(ctx context.Context, _ *ipc.Call, input auditQueryRequest) ([]auditDTO, error) {
				rows, err := database.AuditQuery(ctx, store.AuditQuery{
					SessionID: input.SessionID, AssetID: input.AssetID, Source: input.Source, Kind: input.Kind, Limit: input.Limit, Offset: input.Offset,
				})
				result := make([]auditDTO, len(rows))
				for index, row := range rows {
					result[index] = auditDTO{
						ID: row.ID, TS: row.TS, SessionID: row.SessionID, AssetID: row.AssetID, Source: row.Source, Kind: row.Kind,
						Payload: store.ParseJSONOr(row.PayloadJSON), ExitCode: row.ExitCode, DurationMS: row.DurationMS,
					}
				}
				return result, err
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "audit_count", func(ctx context.Context, _ *ipc.Call, input auditQueryRequest) (auditCountDTO, error) {
				total, err := database.AuditCount(ctx, store.AuditQuery{
					SessionID: input.SessionID, AssetID: input.AssetID, Source: input.Source, Kind: input.Kind,
				})
				return auditCountDTO{Total: total}, err
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "command_query", func(ctx context.Context, _ *ipc.Call, input commandLogQueryRequest) ([]commandLogDTO, error) {
				rows, err := database.CommandLogQuery(ctx, store.CommandLogQuery{
					SessionID: input.SessionID, AssetID: input.AssetID, UserID: commandLogUserFilter(ctx, desktop, input.UserID), Limit: input.Limit, Offset: input.Offset,
				})
				result := make([]commandLogDTO, len(rows))
				for index, row := range rows {
					result[index] = commandLogDTO{
						ID: row.ID, SessionID: row.SessionID, TabID: row.TabID, AssetID: row.AssetID, UserID: row.UserID,
						Command: row.Command, Source: row.Source, ExitCode: row.ExitCode, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
					}
				}
				return result, err
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "command_count", func(ctx context.Context, _ *ipc.Call, input commandLogQueryRequest) (auditCountDTO, error) {
				total, err := database.CommandLogCount(ctx, store.CommandLogQuery{
					SessionID: input.SessionID, AssetID: input.AssetID, UserID: commandLogUserFilter(ctx, desktop, input.UserID),
				})
				return auditCountDTO{Total: total}, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "known_host_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) (any, error) {
				return productionKnownHostList(ctx, database, hostKeys, hostKeysShared)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "known_host_accept", func(ctx context.Context, _ *ipc.Call, input knownHostRequest) (any, error) {
				return nil, productionKnownHostAccept(ctx, database, hostKeys, hostKeysShared, input.Host, input.Port, input.KeyType, input.Fingerprint, input.Key)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "known_host_remove", func(ctx context.Context, _ *ipc.Call, input idRequest) (any, error) {
				return nil, productionKnownHostRemove(ctx, database, hostKeys, hostKeysShared, input.ID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "desktop_tray_get", func(ctx context.Context, _ *ipc.Call, _ struct{}) (desktopTrayDTO, error) {
				if !desktop {
					return desktopTrayDTO{}, ipc.NewError(ipc.CodeUnsupported, "托盘设置只在桌面端可用")
				}
				enabled, err := DesktopCloseToTray(ctx, database)
				return desktopTrayDTO{Enabled: enabled}, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "desktop_tray_set", func(ctx context.Context, _ *ipc.Call, input desktopTraySetRequest) (desktopTrayDTO, error) {
				if !desktop {
					return desktopTrayDTO{}, ipc.NewError(ipc.CodeUnsupported, "托盘设置只在桌面端可用")
				}
				value := "0"
				if input.Enabled {
					value = "1"
				}
				if err := database.SettingSet(ctx, DesktopCloseToTraySettingKey, value); err != nil {
					return desktopTrayDTO{}, err
				}
				return desktopTrayDTO{Enabled: input.Enabled}, nil
			})
		},
		func() error { return registerAuditTimeline(dispatcher, database, desktop) },
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func productionAssetDTO(row store.AssetRow) assetDTO {
	return assetDTO{
		ID: row.ID, GroupID: row.GroupID, Kind: row.Kind, Name: row.Name, Host: row.Host, Port: row.Port,
		Username: row.Username, AuthKind: row.AuthKind, KeyPath: row.KeyPath, CredID: row.CredID,
		Options: store.ParseJSONOr(row.OptionsJSON), Tags: row.Tags, Note: row.Note, Sort: row.Sort,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt, Builtin: row.Builtin,
	}
}

func productionGroupDTO(row store.AssetGroupRow) groupDTO {
	return groupDTO{
		ID: row.ID, ParentID: row.ParentID, Name: row.Name, Sort: row.Sort, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		DefaultUsername: row.DefaultUsername, DefaultAuthKind: row.DefaultAuthKind, DefaultKeyPath: row.DefaultKeyPath,
		DefaultCredID: row.DefaultCredID, DefaultJumpAssetID: row.DefaultJumpAssetID,
	}
}

func productionSnippetDTO(row store.SnippetRow) snippetDTO {
	// A step column that cannot be decoded degrades to a single-step view; the body still holds the commands.
	steps, err := store.DecodeSnippetSteps(row.Steps)
	if err != nil {
		steps = []store.SnippetStep{}
	}
	return snippetDTO{
		ID: row.ID, Name: row.Name, Body: row.Body, GroupID: row.GroupID, Sort: row.Sort,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Description: row.Description, Steps: steps,
	}
}

// commandLogUserFilter returns the userId filter in effect for command log queries: in the server-side assembly
// (desktop=false), a logged-in caller is always forced down to their own userId, and client-reported filters are not
// trusted; desktop and identity-less open deployments (auth=off / loopback login-free / gateway) keep the client-given filter.
func commandLogUserFilter(ctx context.Context, desktop bool, requested *string) *string {
	if desktop {
		return requested
	}
	userID, ok := ipc.UserIDFromContext(ctx)
	if !ok {
		return requested
	}
	return &userID
}

func productionJSONText(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 || string(raw) == "null" {
		return fallback
	}
	return string(raw)
}

func validatedSnippetName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", ipc.BadParam(fmt.Errorf("片段名称不能为空"))
	}
	return trimmed, nil
}

// validateSnippetBody requires a non-empty body for a plain snippet; a multi-step snippet derives its body from the steps.
func validateSnippetBody(body string, steps []store.SnippetStep) error {
	if len(steps) > 0 {
		return nil
	}
	if strings.TrimSpace(body) == "" {
		return ipc.BadParam(fmt.Errorf("片段内容不能为空"))
	}
	return nil
}
