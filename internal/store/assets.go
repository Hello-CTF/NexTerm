package store

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

func (s *Store) AssetList(ctx context.Context, includeDeleted bool) ([]AssetRow, error) {
	asset := query.Use(s.db).AssetRow
	q := asset.WithContext(ctx).Where(ownerConditions(ctx, asset.TableName())...)
	if !includeDeleted {
		q = q.Where(asset.DeletedAt.IsNull())
	}
	result := []AssetRow{}
	if err := q.Order(asset.Sort, asset.Name).Scan(&result); err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) AssetGet(ctx context.Context, id string) (AssetRow, error) {
	return s.assetGet(ctx, s.db, id)
}

func (s *Store) AssetGetLive(ctx context.Context, id string) (AssetRow, error) {
	asset := query.Use(s.db).AssetRow
	row, err := asset.WithContext(ctx).
		Where(asset.ID.Eq(id)).
		Where(asset.DeletedAt.IsNull()).
		Where(ownerConditions(ctx, asset.TableName())...).
		First()
	if isNoRows(err) {
		return AssetRow{}, notFound("资产")
	}
	if err != nil {
		return AssetRow{}, dbError(err)
	}
	return *row, nil
}

func (s *Store) AssetGetTx(ctx context.Context, tx *gorm.DB, id string) (AssetRow, error) {
	return s.assetGet(ctx, tx, id)
}

func (s *Store) assetGet(ctx context.Context, db *gorm.DB, id string) (AssetRow, error) {
	asset := query.Use(db).AssetRow
	row, err := asset.WithContext(ctx).
		Where(asset.ID.Eq(id)).
		Where(ownerConditions(ctx, asset.TableName())...).
		First()
	if isNoRows(err) {
		return AssetRow{}, notFound("资产")
	}
	if err != nil {
		return AssetRow{}, dbError(err)
	}
	return *row, nil
}

func (s *Store) AssetOwnerID(ctx context.Context, id string) (string, error) {
	asset := query.Use(s.db).AssetRow
	row, err := asset.WithContext(ctx).Where(asset.ID.Eq(id)).First()
	if isNoRows(err) {
		return "", notFound("资产")
	}
	if err != nil {
		return "", dbError(err)
	}
	return row.OwnerID, nil
}

func (s *Store) AssetCreate(ctx context.Context, input AssetInput) (AssetRow, error) {
	if err := validateInventoryCreateOptions(input.OptionsJSON); err != nil {
		return AssetRow{}, err
	}
	return s.assetCreate(ctx, s.db, input)
}

func (s *Store) AssetCreateTx(ctx context.Context, tx *gorm.DB, input AssetInput) (AssetRow, error) {
	if err := validateInventoryCreateOptions(input.OptionsJSON); err != nil {
		return AssetRow{}, err
	}
	return s.assetCreate(ctx, tx, input)
}

func (s *Store) assetCreate(ctx context.Context, db *gorm.DB, input AssetInput) (AssetRow, error) {
	if strings.TrimSpace(input.Name) == "" {
		return AssetRow{}, badParam(errString("资产名称不能为空"))
	}
	if input.GroupID != nil {
		if _, err := s.groupReference(ctx, db, *input.GroupID); err != nil {
			return AssetRow{}, err
		}
	}
	id := ids.New()
	now := ids.NowMS()
	row := AssetRow{
		ID: id, GroupID: input.GroupID, Kind: input.Kind, Name: strings.TrimSpace(input.Name), Host: input.Host, Port: input.Port,
		Username: input.Username, AuthKind: input.AuthKind, KeyPath: input.KeyPath, CredID: input.CredID,
		OptionsJSON: input.OptionsJSON, Tags: NormalizeAssetTags(input.Tags), Note: input.Note, Sort: input.Sort, CreatedAt: now, UpdatedAt: now, OwnerID: ipc.OwnerID(ctx),
	}
	if err := query.Use(db).AssetRow.WithContext(ctx).Create(&row); err != nil {
		return AssetRow{}, dbError(err)
	}
	return s.assetGet(ctx, db, id)
}

var ErrWriteConflict = errors.New("store: write conflict")

// maxWriteRetries bounds the expected-revision retry loop of local writes. Concurrent
// local edits on the same row serialize: each conflict re-reads the fresh revision and
// advances strictly past it, so equal revision always means "unchanged" for CAS consumers.
const maxWriteRetries = 8

func nextWriteVersion(expected int64) int64 {
	now := ids.NowMS()
	if now <= expected {
		return expected + 1
	}
	return now
}

func (s *Store) AssetUpdate(ctx context.Context, id string, patch AssetPatch) (AssetRow, error) {
	return s.assetUpdate(ctx, s.db, id, patch)
}

func (s *Store) AssetUpdateTx(ctx context.Context, tx *gorm.DB, id string, patch AssetPatch) (AssetRow, error) {
	return s.assetUpdate(ctx, tx, id, patch)
}

func (s *Store) assetUpdateValues(ctx context.Context, db *gorm.DB, id string, patch AssetPatch, updatedAt int64) (map[string]any, error) {
	values := map[string]any{}
	if patch.GroupID.Set {
		if patch.GroupID.Value != nil {
			if _, err := s.groupReference(ctx, db, *patch.GroupID.Value); err != nil {
				return nil, err
			}
		}
		values["group_id"] = patch.GroupID.Value
	}
	if patch.Host.Set {
		values["host"] = patch.Host.Value
	}
	if patch.Port.Set {
		values["port"] = patch.Port.Value
	}
	if patch.Username.Set {
		values["username"] = patch.Username.Value
	}
	if patch.AuthKind.Set {
		values["auth_kind"] = patch.AuthKind.Value
	}
	if patch.KeyPath.Set {
		values["key_path"] = patch.KeyPath.Value
	}
	if patch.CredID.Set {
		values["cred_id"] = patch.CredID.Value
	}
	if patch.Name != nil {
		values["name"] = strings.TrimSpace(*patch.Name)
	}
	if patch.OptionsJSON != nil {
		values["options_json"] = *patch.OptionsJSON
	}
	if patch.Tags != nil {
		values["tags"] = NormalizeAssetTags(*patch.Tags)
	}
	if patch.Note != nil {
		values["note"] = *patch.Note
	}
	if patch.Sort != nil {
		values["sort"] = *patch.Sort
	}
	values["updated_at"] = updatedAt
	return values, nil
}

// assetUpdate advances the revision atomically: the write lands only when the row still
// matches the revision the patch was computed from, so two concurrent local edits cannot
// both land on the same revision with different content (same-version ABA). A conflict
// re-reads the fresh row and retries; exhausting the bounded retries reports ErrWriteConflict.
func (s *Store) assetUpdate(ctx context.Context, db *gorm.DB, id string, patch AssetPatch) (AssetRow, error) {
	for attempt := 0; attempt < maxWriteRetries; attempt++ {
		current, err := s.assetGet(ctx, db, id)
		if err != nil {
			return AssetRow{}, err
		}
		if err := validateInventoryPatch(current, patch); err != nil {
			return AssetRow{}, err
		}
		values, err := s.assetUpdateValues(ctx, db, id, patch, nextWriteVersion(current.UpdatedAt))
		if err != nil {
			return AssetRow{}, err
		}
		asset := query.Use(db).AssetRow
		statement := asset.WithContext(ctx).
			Where(asset.ID.Eq(id), asset.UpdatedAt.Eq(current.UpdatedAt)).
			Where(ownerConditions(ctx, asset.TableName())...)
		if current.DeletedAt == nil {
			statement = statement.Where(asset.DeletedAt.IsNull())
		} else {
			statement = statement.Where(asset.DeletedAt.Eq(*current.DeletedAt))
		}
		info, err := statement.UpdateColumns(values)
		if err != nil {
			return AssetRow{}, dbError(err)
		}
		if info.RowsAffected == 1 {
			return s.assetGet(ctx, db, id)
		}
	}
	return AssetRow{}, ErrWriteConflict
}

func (s *Store) AssetUpdateCAS(ctx context.Context, db *gorm.DB, id string, expectedUpdatedAt int64, patch AssetPatch) (AssetRow, error) {
	return s.assetUpdateCAS(ctx, db, id, expectedUpdatedAt, patch, false)
}

func (s *Store) assetUpdateCAS(ctx context.Context, db *gorm.DB, id string, expectedUpdatedAt int64, patch AssetPatch, sourceSync bool) (AssetRow, error) {
	current, err := s.assetGet(ctx, db, id)
	if err != nil {
		var appErr *ipc.Error
		if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
			return AssetRow{}, ErrWriteConflict
		}
		return AssetRow{}, err
	}
	if current.UpdatedAt != expectedUpdatedAt || current.DeletedAt != nil {
		return AssetRow{}, ErrWriteConflict
	}
	if !sourceSync {
		if err := validateInventoryPatch(current, patch); err != nil {
			return AssetRow{}, err
		}
	}
	values, err := s.assetUpdateValues(ctx, db, id, patch, nextWriteVersion(expectedUpdatedAt))
	if err != nil {
		return AssetRow{}, err
	}
	asset := query.Use(db).AssetRow
	info, err := asset.WithContext(ctx).
		Where(asset.ID.Eq(id), asset.UpdatedAt.Eq(expectedUpdatedAt), asset.DeletedAt.IsNull()).
		Where(ownerConditions(ctx, asset.TableName())...).
		UpdateColumns(values)
	if err != nil {
		return AssetRow{}, dbError(err)
	}
	if info.RowsAffected != 1 {
		return AssetRow{}, ErrWriteConflict
	}
	return s.assetGet(ctx, db, id)
}

func applyOptional[T any](current **T, patch Optional[T]) {
	if patch.Set {
		*current = patch.Value
	}
}

// AssetDelete soft-deletes with the same atomic revision advancement as assetUpdate: the
// delete lands only when the row still matches the revision it was computed from, so a
// concurrent edit or delete can never be flattened into the same revision.
func (s *Store) AssetDelete(ctx context.Context, id string) error {
	for attempt := 0; attempt < maxWriteRetries; attempt++ {
		row, err := s.AssetGet(ctx, id)
		if err != nil {
			return err
		}
		if row.Builtin {
			return badParam(errString("\"当前设备\"是内置资产，不能删除"))
		}
		asset := query.Use(s.db).AssetRow
		// The deletion advances the effective revision strictly past the last update
		// (same-millisecond deletes included) so LWW comparisons always observe it.
		revision := nextWriteVersion(row.UpdatedAt)
		info, err := asset.WithContext(ctx).
			Where(asset.ID.Eq(id), asset.UpdatedAt.Eq(row.UpdatedAt)).
			Where(ownerConditions(ctx, asset.TableName())...).
			UpdateColumns(map[string]any{"deleted_at": revision, "updated_at": revision})
		if err != nil {
			return dbError(err)
		}
		if info.RowsAffected == 1 {
			return nil
		}
	}
	return ErrWriteConflict
}

func (s *Store) AssetEnsureBuiltinLocal(ctx context.Context) (AssetRow, error) {
	row, err := s.AssetGet(ctx, BuiltinLocalAssetID)
	if err == nil {
		if row.DeletedAt == nil {
			return row, nil
		}
		asset := query.Use(s.db).AssetRow
		if _, err := asset.WithContext(ctx).
			Where(asset.ID.Eq(BuiltinLocalAssetID)).
			Where(ownerConditions(ctx, asset.TableName())...).
			UpdateColumns(map[string]any{"deleted_at": nil, "updated_at": ids.NowMS()}); err != nil {
			return AssetRow{}, dbError(err)
		}
		return s.AssetGet(ctx, BuiltinLocalAssetID)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNotFound {
		return AssetRow{}, err
	}
	if ipc.OwnerID(ctx) != "" {
		return AssetRow{}, notFound("当前设备不可用")
	}
	now := ids.NowMS()
	row = AssetRow{
		ID: BuiltinLocalAssetID, Kind: "local", Name: BuiltinLocalAssetName, OptionsJSON: "{}", Sort: -1,
		CreatedAt: now, UpdatedAt: now, Builtin: true,
	}
	if err := query.Use(s.db).AssetRow.WithContext(ctx).Create(&row); err != nil {
		return AssetRow{}, dbError(err)
	}
	return s.AssetGet(ctx, BuiltinLocalAssetID)
}

const AssetSearchLimit = 100

type AssetSearchResult struct {
	Assets    []AssetRow
	Total     int
	Truncated bool
}

func (s *Store) AssetSearch(ctx context.Context, text string) ([]AssetRow, error) {
	result, err := s.AssetSearchWithMeta(ctx, text)
	if err != nil {
		return nil, err
	}
	return result.Assets, nil
}

func (s *Store) assetSearchSnapshot(ctx context.Context) (assets []AssetRow, groups []AssetGroupRow, err error) {
	assets = []AssetRow{}
	groups = []AssetGroupRow{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		asset := query.Use(tx).AssetRow
		if err := asset.WithContext(ctx).
			Where(asset.DeletedAt.IsNull()).
			Where(ownerConditions(ctx, asset.TableName())...).
			Order(asset.Sort, asset.Name).
			Scan(&assets); err != nil {
			return dbError(err)
		}
		group := query.Use(tx).AssetGroupRow
		if err := group.WithContext(ctx).
			Where(ownerConditions(ctx, group.TableName())...).
			Order(group.Sort, group.Name).
			Scan(&groups); err != nil {
			return dbError(err)
		}
		return nil
	})
	return assets, groups, err
}

func (s *Store) AssetSearchWithMeta(ctx context.Context, text string) (AssetSearchResult, error) {
	matcher, exactTag, err := newAssetSearchMatcher(text)
	if err != nil {
		return AssetSearchResult{}, err
	}
	assets, groups, err := s.assetSearchSnapshot(ctx)
	if err != nil {
		return AssetSearchResult{}, err
	}
	groupPaths := assetGroupSearchPaths(groups)
	matches := make([]AssetRow, 0)
	for _, asset := range assets {
		if exactTag != nil {
			if assetHasExactTag(asset.Tags, *exactTag) {
				matches = append(matches, asset)
			}
			continue
		}
		fields := []string{asset.Name, assetSearchString(asset.Host), assetSearchString(asset.Username), asset.Note, asset.Tags}
		if asset.GroupID != nil {
			fields = append(fields, groupPaths[*asset.GroupID])
		}
		if matcher != nil {
			for _, value := range fields {
				if matcher.MatchString(value) {
					matches = append(matches, asset)
					break
				}
			}
		}
	}
	result := AssetSearchResult{Total: len(matches), Truncated: len(matches) > AssetSearchLimit}
	if len(matches) > AssetSearchLimit {
		matches = matches[:AssetSearchLimit]
	}
	result.Assets = append([]AssetRow{}, matches...)
	return result, nil
}

func assetGroupSearchPaths(groups []AssetGroupRow) map[string]string {
	byID := make(map[string]AssetGroupRow, len(groups))
	for _, group := range groups {
		byID[group.ID] = group
	}
	paths := make(map[string]string, len(groups))
	for _, group := range groups {
		parts := []string{}
		seen := map[string]bool{}
		current := group
		for {
			if seen[current.ID] {
				break
			}
			seen[current.ID] = true
			parts = append([]string{current.Name}, parts...)
			if current.ParentID == nil {
				break
			}
			parent, ok := byID[*current.ParentID]
			if !ok {
				break
			}
			current = parent
		}
		paths[group.ID] = strings.Join(parts, " / ")
	}
	return paths
}

func newAssetSearchMatcher(text string) (*regexp.Regexp, *string, error) {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"tag:", "tag="} {
		if strings.HasPrefix(lower, prefix) {
			value := unescapeAssetSearchText(strings.TrimSpace(trimmed[len(prefix):]))
			return nil, &value, nil
		}
	}
	var pattern strings.Builder
	pattern.WriteString("(?is)^.*")
	escaped := false
	for _, r := range trimmed {
		if escaped {
			pattern.WriteString(regexp.QuoteMeta(string(r)))
			escaped = false
			continue
		}
		switch r {
		case '\\':
			escaped = true
		case '%':
			pattern.WriteString(".*")
		case '_':
			pattern.WriteString(".")
		default:
			pattern.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	if escaped {
		pattern.WriteString(regexp.QuoteMeta("\\"))
	}
	pattern.WriteString(".*$")
	matcher, err := regexp.Compile(pattern.String())
	if err != nil {
		return nil, nil, badParam(errString("搜索表达式无效"))
	}
	return matcher, nil, nil
}

func unescapeAssetSearchText(text string) string {
	var result strings.Builder
	escaped := false
	for _, r := range text {
		if escaped {
			result.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		result.WriteRune(r)
	}
	if escaped {
		result.WriteRune('\\')
	}
	return result.String()
}

func assetHasExactTag(raw, wanted string) bool {
	if wanted == "" {
		return false
	}
	for _, tag := range AssetTagValues(raw) {
		if strings.EqualFold(tag, wanted) {
			return true
		}
	}
	return false
}

func AssetTagValues(raw string) []string {
	raw = strings.ReplaceAll(raw, "，", ",")
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if tag := strings.TrimSpace(part); tag != "" {
			result = append(result, tag)
		}
	}
	return result
}

func NormalizeAssetTags(raw string) string {
	seen := map[string]bool{}
	result := make([]string, 0)
	for _, tag := range AssetTagValues(raw) {
		key := strings.ToLower(tag)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, tag)
	}
	return strings.Join(result, ", ")
}

func assetSearchString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *Store) AssetUpsert(ctx context.Context, row AssetRow) (bool, error) {
	return s.assetUpsert(ctx, s.db, row)
}

func (s *Store) AssetUpsertTx(ctx context.Context, tx *gorm.DB, row AssetRow) (bool, error) {
	return s.assetUpsert(ctx, tx, row)
}

func validateInventoryUpsert(current, incoming AssetRow) error {
	name := strings.TrimSpace(incoming.Name)
	patch := AssetPatch{
		GroupID: inventoryOptional(incoming.GroupID), Name: &name,
		Host: inventoryOptional(incoming.Host), Port: inventoryOptional(incoming.Port), Username: inventoryOptional(incoming.Username),
		AuthKind: inventoryOptional(incoming.AuthKind), KeyPath: inventoryOptional(incoming.KeyPath), CredID: inventoryOptional(incoming.CredID),
		OptionsJSON: &incoming.OptionsJSON,
	}
	if err := validateInventoryPatch(current, patch); err != nil {
		return err
	}
	options, err := decodeInventoryOptions(current.OptionsJSON)
	if err != nil {
		return badParam(errString("资产现有高级选项不是有效 JSON"))
	}
	meta, managed, err := inventoryMetaFromOptions(options)
	if err != nil {
		return badParam(errString("资产清单源标记无效"))
	}
	if managed && current.Kind != incoming.Kind {
		return inventoryReadOnly(meta, "类型")
	}
	return nil
}

func (s *Store) assetUpsert(ctx context.Context, db *gorm.DB, row AssetRow) (bool, error) {
	if row.ID == BuiltinLocalAssetID {
		return false, badParam(errString("内置资产\"当前设备\"不接受同步写入（各设备身份相互独立）"))
	}
	if err := EnsureID(row.ID); err != nil {
		return false, err
	}
	if strings.TrimSpace(row.Name) == "" {
		return false, badParam(errString("资产名称不能为空"))
	}
	if row.GroupID != nil {
		if _, err := s.groupReference(ctx, db, *row.GroupID); err != nil {
			return false, err
		}
	}
	asset := query.Use(db).AssetRow
	current, err := asset.WithContext(ctx).
		Where(asset.ID.Eq(row.ID)).
		Where(ownerConditions(ctx, asset.TableName())...).
		First()
	exists := err == nil
	if err != nil && !isNoRows(err) {
		return false, dbError(err)
	}
	if exists {
		if err := validateInventoryUpsert(*current, row); err != nil {
			return false, err
		}
	} else if err := validateInventoryCreateOptions(row.OptionsJSON); err != nil {
		return false, err
	}
	row.OwnerID = ipc.OwnerID(ctx)
	row.Name = strings.TrimSpace(row.Name)
	row.Tags = NormalizeAssetTags(row.Tags)
	row.Builtin = false
	row.OptionsJSON = normalizeAssetOptionsJSON(row.OptionsJSON)
	if err := s.upsertWithDB(ctx, db, &row, []string{
		"group_id", "kind", "name", "host", "port", "username", "auth_kind", "key_path", "cred_id",
		"options_json", "tags", "note", "sort", "updated_at", "deleted_at",
	}); err != nil {
		return false, err
	}
	return !exists, nil
}

// normalizeAssetOptionsJSON keeps stored options valid JSON and consistent with the
// database default: an empty payload never overwrites the column default "{}" with an
// invalid empty string.
func normalizeAssetOptionsJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}

// AssetUpsertGuarded is the expected-revision variant of AssetUpsert: the write lands only
// when the stored row still matches the base snapshot the caller's decision was made
// against (nil base means "must not exist yet"). Sync apply and bundle import decide on a
// snapshot and write later; without this guard a concurrent reconciler or sync write in
// between is silently overwritten. A stale snapshot or a cross-owner collision surfaces as
// ErrWriteConflict and the row is left untouched.
func (s *Store) AssetUpsertGuarded(ctx context.Context, row AssetRow, base *AssetRow) (bool, error) {
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		created, err = s.AssetUpsertGuardedTx(ctx, tx, row, base)
		return err
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

// AssetUpsertGuardedTx is the in-transaction version of AssetUpsertGuarded.
func (s *Store) AssetUpsertGuardedTx(ctx context.Context, tx *gorm.DB, row AssetRow, base *AssetRow) (bool, error) {
	if row.ID == BuiltinLocalAssetID {
		return false, badParam(errString("内置资产\"当前设备\"不接受同步写入（各设备身份相互独立）"))
	}
	if err := EnsureID(row.ID); err != nil {
		return false, err
	}
	if strings.TrimSpace(row.Name) == "" {
		return false, badParam(errString("资产名称不能为空"))
	}
	if row.GroupID != nil {
		if _, err := s.groupReference(ctx, tx, *row.GroupID); err != nil {
			return false, err
		}
	}
	row.OwnerID = ipc.OwnerID(ctx)
	row.Name = strings.TrimSpace(row.Name)
	row.Tags = NormalizeAssetTags(row.Tags)
	row.Builtin = false
	row.OptionsJSON = normalizeAssetOptionsJSON(row.OptionsJSON)
	if base == nil {
		if err := validateInventoryCreateOptions(row.OptionsJSON); err != nil {
			return false, err
		}
		if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
			if IsUniqueErr(err) {
				return false, ErrWriteConflict
			}
			return false, dbError(err)
		}
		return true, nil
	}
	if err := validateInventoryUpsert(*base, row); err != nil {
		return false, err
	}
	values := map[string]any{
		"group_id": row.GroupID, "kind": row.Kind, "name": row.Name,
		"host": row.Host, "port": row.Port, "username": row.Username, "auth_kind": row.AuthKind,
		"key_path": row.KeyPath, "cred_id": row.CredID, "options_json": row.OptionsJSON,
		"tags": row.Tags, "note": row.Note, "sort": row.Sort,
		"updated_at": row.UpdatedAt, "deleted_at": row.DeletedAt,
	}
	asset := query.Use(tx).AssetRow
	// The guarded UPDATE binds every sync-visible column of the base snapshot in the same
	// statement: the write lands only when the row still equals the base content, so a
	// same-revision different content write can never be overwritten by the old decision.
	statement := asset.WithContext(ctx).
		Where(asset.ID.Eq(row.ID), asset.UpdatedAt.Eq(base.UpdatedAt)).
		Where(ownerConditions(ctx, asset.TableName())...)
	if base.DeletedAt == nil {
		statement = statement.Where(asset.DeletedAt.IsNull())
	} else {
		statement = statement.Where(asset.DeletedAt.Eq(*base.DeletedAt))
	}
	statement = statement.Where(asset.Kind.Eq(base.Kind), asset.Name.Eq(base.Name)).
		Where(asset.OptionsJSON.Eq(base.OptionsJSON), asset.Tags.Eq(base.Tags)).
		Where(asset.Note.Eq(base.Note), asset.Sort.Eq(base.Sort), asset.CreatedAt.Eq(base.CreatedAt))
	if base.GroupID == nil {
		statement = statement.Where(asset.GroupID.IsNull())
	} else {
		statement = statement.Where(asset.GroupID.Eq(*base.GroupID))
	}
	if base.Host == nil {
		statement = statement.Where(asset.Host.IsNull())
	} else {
		statement = statement.Where(asset.Host.Eq(*base.Host))
	}
	if base.Port == nil {
		statement = statement.Where(asset.Port.IsNull())
	} else {
		statement = statement.Where(asset.Port.Eq(*base.Port))
	}
	if base.Username == nil {
		statement = statement.Where(asset.Username.IsNull())
	} else {
		statement = statement.Where(asset.Username.Eq(*base.Username))
	}
	if base.AuthKind == nil {
		statement = statement.Where(asset.AuthKind.IsNull())
	} else {
		statement = statement.Where(asset.AuthKind.Eq(*base.AuthKind))
	}
	if base.KeyPath == nil {
		statement = statement.Where(asset.KeyPath.IsNull())
	} else {
		statement = statement.Where(asset.KeyPath.Eq(*base.KeyPath))
	}
	if base.CredID == nil {
		statement = statement.Where(asset.CredID.IsNull())
	} else {
		statement = statement.Where(asset.CredID.Eq(*base.CredID))
	}
	info, err := statement.UpdateColumns(values)
	if err != nil {
		return false, dbError(err)
	}
	if info.RowsAffected == 1 {
		return false, nil
	}
	// MySQL/MariaDB report 0 affected rows when the guarded UPDATE sets identical
	// values: accept the write only when the stored row already equals the desired
	// state; anything else means the snapshot is stale.
	matches, err := s.assetRowMatches(ctx, tx, row)
	if err != nil {
		return false, err
	}
	if matches {
		return false, nil
	}
	return false, ErrWriteConflict
}

// assetRowMatches reports whether the stored row already carries exactly the given
// sync-visible state (same revision, deletion state, and content).
func (s *Store) assetRowMatches(ctx context.Context, tx *gorm.DB, row AssetRow) (bool, error) {
	asset := query.Use(tx).AssetRow
	stored, err := asset.WithContext(ctx).
		Where(asset.ID.Eq(row.ID)).
		Where(ownerConditions(ctx, asset.TableName())...).
		First()
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	return stored.UpdatedAt == row.UpdatedAt &&
		optionalInt64Equal(stored.DeletedAt, row.DeletedAt) &&
		stored.GroupID == nil == (row.GroupID == nil) && (stored.GroupID == nil || *stored.GroupID == *row.GroupID) &&
		stored.Kind == row.Kind && stored.Name == row.Name &&
		optionalStringEqual(stored.Host, row.Host) &&
		optionalInt32Equal(stored.Port, row.Port) &&
		optionalStringEqual(stored.Username, row.Username) &&
		optionalStringEqual(stored.AuthKind, row.AuthKind) &&
		optionalStringEqual(stored.KeyPath, row.KeyPath) &&
		optionalStringEqual(stored.CredID, row.CredID) &&
		stored.OptionsJSON == row.OptionsJSON && stored.Tags == row.Tags &&
		stored.Note == row.Note && stored.Sort == row.Sort, nil
}

func optionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalInt32Equal(left, right *int32) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalInt64Equal(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
