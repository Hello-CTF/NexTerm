package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func payloadFromSnippet(row store.SnippetRow) SnippetPayload {
	return SnippetPayload{
		ID: row.ID, GroupID: row.GroupID, Name: row.Name, Body: row.Body,
		Sort: row.Sort, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (p SnippetPayload) storeRow() store.SnippetRow {
	return store.SnippetRow{
		ID: p.ID, GroupID: p.GroupID, Name: p.Name, Body: p.Body,
		Sort: p.Sort, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func snippetContentEqual(local store.SnippetRow, snippet SnippetPayload) bool {
	return reflect.DeepEqual(payloadFromSnippet(local), snippet)
}

func (s *Service) importSnippets(ctx context.Context, snippets []SnippetPayload, force bool, bundleOrigin, localOrigin string, report *ImportReport) {
	for _, snippet := range snippets {
		if store.EnsureID(snippet.ID) != nil || strings.TrimSpace(snippet.Name) == "" {
			report.Refused++
			report.Warnings = append(report.Warnings, fmt.Sprintf("片段 %s 的 ID 或名称不合法，已拒绝导入", snippet.ID))
			continue
		}
		local, err := s.store.SnippetGet(ctx, snippet.ID)
		exists := err == nil
		if err != nil && !isNotFound(err) {
			report.Refused++
			report.Warnings = append(report.Warnings, fmt.Sprintf("无法检查片段 %s: %v", snippet.ID, err))
			continue
		}
		if exists && !force && s.skipSnippet(snippet, local, bundleOrigin, localOrigin, report) {
			continue
		}
		row := snippet.storeRow()
		if row.GroupID != nil {
			if strings.TrimSpace(*row.GroupID) == "" {
				row.GroupID = nil
			} else if _, err := s.store.GroupGet(ctx, *row.GroupID); isNotFound(err) {
				report.Warnings = append(report.Warnings, fmt.Sprintf("片段 %s 引用的分组 %s 不存在，已清除该引用", snippet.ID, *row.GroupID))
				row.GroupID = nil
			} else if err != nil {
				report.Refused++
				report.Warnings = append(report.Warnings, fmt.Sprintf("无法检查片段 %s 的分组: %v", snippet.ID, err))
				continue
			}
		}
		created, err := s.snippetUpsert(ctx, row)
		if err != nil {
			report.Refused++
			report.Warnings = append(report.Warnings, fmt.Sprintf("片段 %s 导入失败: %v", snippet.ID, err))
			continue
		}
		if created {
			report.SnippetsCreated++
		} else {
			report.SnippetsUpdated++
		}
	}
}

// snippetUpsert 与 store.GroupUpsert 同款语义：按 ID 幂等写入并保留对端修订号，返回是否新建。
// store 层暂无片段 upsert 方法，这里经 Store.DB() 执行同款 SQL。
func (s *Service) snippetUpsert(ctx context.Context, row store.SnippetRow) (bool, error) {
	var ignored string
	err := s.store.DB().QueryRowContext(ctx, "SELECT id FROM snippet WHERE id = ?", row.ID).Scan(&ignored)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	_, err = s.store.DB().ExecContext(ctx, `INSERT INTO snippet(id, group_id, name, body, sort, created_at, updated_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET group_id=excluded.group_id, name=excluded.name, body=excluded.body,
sort=excluded.sort, updated_at=excluded.updated_at`,
		row.ID, row.GroupID, strings.TrimSpace(row.Name), row.Body, row.Sort, row.CreatedAt, row.UpdatedAt)
	if err != nil {
		return false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return !exists, nil
}

func (s *Service) skipSnippet(snippet SnippetPayload, local store.SnippetRow, bundleOrigin, localOrigin string, report *ImportReport) bool {
	switch {
	case local.UpdatedAt > snippet.UpdatedAt:
		report.SkippedNewer++
		report.SkippedNewerDetails = append(report.SkippedNewerDetails, SkippedNewerEntry{
			Kind: "snippet", ID: snippet.ID, Name: snippet.Name,
			LocalRevision: local.UpdatedAt, RemoteRevision: snippet.UpdatedAt,
		})
		report.Warnings = append(report.Warnings, fmt.Sprintf("片段 %s 的本机版本较新，已跳过；如需覆盖请使用强制同步", snippet.ID))
		return true
	case local.UpdatedAt == snippet.UpdatedAt && bundleOrigin != localOrigin && !snippetContentEqual(local, snippet):
		if bundleOrigin > localOrigin {
			report.Warnings = append(report.Warnings, fmt.Sprintf("片段 %s 本机与远端修订号相同（%d），按 Origin 字典序接受远端版本（%s > %s）",
				snippet.ID, local.UpdatedAt, bundleOrigin, localOrigin))
			return false
		}
		report.SkippedNewer++
		report.SkippedNewerDetails = append(report.SkippedNewerDetails, SkippedNewerEntry{
			Kind: "snippet", ID: snippet.ID, Name: snippet.Name,
			LocalRevision: local.UpdatedAt, RemoteRevision: snippet.UpdatedAt, EqualRevision: true,
		})
		report.Warnings = append(report.Warnings, fmt.Sprintf("片段 %s 本机与远端修订号相同（%d），按 Origin 字典序保留本机版本（%s < %s）",
			snippet.ID, local.UpdatedAt, localOrigin, bundleOrigin))
		return true
	}
	return false
}
