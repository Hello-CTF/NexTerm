package production

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
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
}

type groupInputRequest struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parentId"`
	Sort     int64   `json:"sort"`
}

type groupPatchRequest struct {
	ID       string                   `json:"id"`
	Name     *string                  `json:"name"`
	ParentID optionalIPCValue[string] `json:"parentId"`
	Sort     *int64                   `json:"sort"`
}

type snippetDTO struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Body    string  `json:"body"`
	GroupID *string `json:"groupId"`
	Sort    int64   `json:"sort"`
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

type knownHostRequest struct {
	Host        string `json:"host"`
	Port        int32  `json:"port"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
}

type keyFileContentRequest struct {
	Content string `json:"content"`
}

func registerStoreCommands(dispatcher *ipc.Dispatcher, database *store.Store, hostKeys *productionHostKeyStore, dataDir string) error {
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
					GroupID: input.GroupID.storeValue(), Name: input.Name, Host: input.Host.storeValue(), Port: input.Port.storeValue(),
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
			return ipc.Register(dispatcher, "asset_save_key_file", func(_ context.Context, _ *ipc.Call, input keyFileContentRequest) (map[string]string, error) {
				if dataDir == "" {
					return nil, ipc.NewError(ipc.CodeUnsupported, "密钥存储不可用")
				}
				if len(input.Content) > 64<<10 {
					return nil, ipc.BadParam(fmt.Errorf("私钥内容超过 64 KiB 上限"))
				}
				directory := filepath.Join(dataDir, "keys")
				if err := os.MkdirAll(directory, 0o700); err != nil {
					return nil, err
				}
				path := filepath.Join(directory, ids.New()+".key")
				if err := os.WriteFile(path, []byte(input.Content), 0o600); err != nil {
					return nil, err
				}
				return map[string]string{"path": path}, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "asset_read_key_file", func(_ context.Context, _ *ipc.Call, input idRequest) (string, error) {
				file, err := os.Open(input.Path)
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
				row, err := database.GroupCreate(ctx, store.GroupInput{ParentID: input.ParentID, Name: input.Name, Sort: input.Sort})
				return productionGroupDTO(row), err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "group_update", func(ctx context.Context, _ *ipc.Call, input groupPatchRequest) (groupDTO, error) {
				row, err := database.GroupUpdate(ctx, input.ID, store.GroupPatch{Name: input.Name, ParentID: input.ParentID.storeValue(), Sort: input.Sort})
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
					result[index] = snippetDTO{ID: row.ID, Name: row.Name, Body: row.Body, GroupID: row.GroupID, Sort: row.Sort}
				}
				return result, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "snippet_create", func(ctx context.Context, _ *ipc.Call, input idRequest) (map[string]string, error) {
				name, err := validatedSnippetName(input.Name)
				if err != nil {
					return nil, err
				}
				if err := validateSnippetBody(input.Body); err != nil {
					return nil, err
				}
				row, err := database.SnippetCreate(ctx, name, input.Body, nil, 0)
				return map[string]string{"id": row.ID}, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "snippet_update", func(ctx context.Context, _ *ipc.Call, input idRequest) (any, error) {
				name, err := validatedSnippetName(input.Name)
				if err != nil {
					return nil, err
				}
				if err := validateSnippetBody(input.Body); err != nil {
					return nil, err
				}
				return nil, database.SnippetUpdate(ctx, input.ID, name, input.Body)
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
			return ipc.Register(dispatcher, "known_host_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) (any, error) {
				return database.KnownHostList(ctx)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "known_host_accept", func(ctx context.Context, _ *ipc.Call, input knownHostRequest) (any, error) {
				return nil, database.KnownHostAccept(ctx, input.Host, input.Port, input.KeyType, input.Fingerprint)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "known_host_remove", func(ctx context.Context, _ *ipc.Call, input idRequest) (any, error) {
				if hostKeys != nil {
					return nil, hostKeys.Remove(ctx, input.ID)
				}
				return nil, database.KnownHostRemove(ctx, input.ID)
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

func productionAssetDTO(row store.AssetRow) assetDTO {
	return assetDTO{
		ID: row.ID, GroupID: row.GroupID, Kind: row.Kind, Name: row.Name, Host: row.Host, Port: row.Port,
		Username: row.Username, AuthKind: row.AuthKind, KeyPath: row.KeyPath, CredID: row.CredID,
		Options: store.ParseJSONOr(row.OptionsJSON), Tags: row.Tags, Note: row.Note, Sort: row.Sort,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt, Builtin: row.Builtin,
	}
}

func productionGroupDTO(row store.AssetGroupRow) groupDTO {
	return groupDTO{ID: row.ID, ParentID: row.ParentID, Name: row.Name, Sort: row.Sort, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
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

func validateSnippetBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return ipc.BadParam(fmt.Errorf("片段内容不能为空"))
	}
	return nil
}
