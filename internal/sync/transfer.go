package sync

import (
	"errors"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func payloadFromAsset(row store.AssetRow) AssetPayload {
	return AssetPayload{
		ID: row.ID, GroupID: row.GroupID, Kind: row.Kind, Name: row.Name,
		Host: row.Host, Port: row.Port, Username: row.Username, AuthKind: row.AuthKind,
		KeyPath: row.KeyPath, CredID: row.CredID, OptionsJSON: row.OptionsJSON,
		Tags: row.Tags, Note: row.Note, Sort: row.Sort, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt,
	}
}

func (p AssetPayload) storeRow() store.AssetRow {
	return store.AssetRow{
		ID: p.ID, GroupID: p.GroupID, Kind: p.Kind, Name: p.Name,
		Host: p.Host, Port: p.Port, Username: p.Username, AuthKind: p.AuthKind,
		KeyPath: p.KeyPath, CredID: p.CredID, OptionsJSON: p.OptionsJSON,
		Tags: p.Tags, Note: p.Note, Sort: p.Sort, CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt, DeletedAt: p.DeletedAt,
	}
}

func hasCode(err error, code ipc.Code) bool {
	var appErr *ipc.Error
	return errors.As(err, &appErr) && appErr.Code == code
}

func isNotFound(err error) bool {
	return hasCode(err, ipc.CodeNotFound)
}
