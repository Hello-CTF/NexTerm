package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func (r *Registry) prepareFile(ctx context.Context, jobID string, scope Scope, call Call) (*preparedChange, *Preview, error) {
	var args struct {
		Path       string `json:"path"`
		Content    string `json:"content"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := decode(call.Args, &args); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(args.Path) == "" {
		return nil, nil, invalid("path 不能为空")
	}
	files, err := r.fileSystem(ctx, scope)
	if err != nil {
		return nil, nil, err
	}
	key := targetKey(scope, args.Path)
	registered, ok := r.state(jobID).readVersion(key)
	if !ok {
		return nil, nil, ErrReadRequired
	}
	change := &preparedChange{path: args.Path, key: key, version: registered}
	if !registered.Exists {
		exists, err := files.Exists(ctx, args.Path)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			return nil, nil, ErrFileChanged
		}
		if call.Name == "edit_file" {
			return nil, nil, errors.New("不能编辑不存在的文件")
		}
		change.before = fileState{known: true, missing: true}
		change.after = args.Content
	} else {
		content, err := files.ReadFile(ctx, args.Path, 2<<20)
		if err != nil {
			return nil, nil, err
		}
		if versionOf(content) != registered {
			return nil, nil, ErrFileChanged
		}
		if !utf8.Valid(content) {
			return nil, nil, errors.New("不能覆盖非 UTF-8 或二进制文件")
		}
		before := string(content)
		change.before = fileState{known: true, content: before}
		switch call.Name {
		case "write_file":
			change.after = args.Content
		case "edit_file":
			if args.OldString == "" || args.OldString == args.NewString {
				return nil, nil, invalid("old_string 必须非空且与 new_string 不同")
			}
			count := strings.Count(before, args.OldString)
			if count == 0 || count > 1 && !args.ReplaceAll {
				return nil, nil, fmt.Errorf("old_string 匹配 %d 处；需要恰好 1 处或启用 replace_all", count)
			}
			change.replacements = count
			if args.ReplaceAll {
				change.after = strings.ReplaceAll(before, args.OldString, args.NewString)
			} else {
				change.after = strings.Replace(before, args.OldString, args.NewString, 1)
			}
		}
	}
	var preview *Preview
	if change.before.known && change.after != change.before.content && len(change.before.content) <= MaxDiffBytes && len(change.after) <= MaxDiffBytes && len(change.before.content)+len(change.after) <= MaxPreviewSize {
		kind := "modify"
		if change.before.missing {
			kind = "create"
		}
		preview = &Preview{Path: change.path, Before: change.before.content, After: change.after, Kind: kind}
	}
	return change, preview, nil
}

const (
	writeCreateSemantics  = "atomic_no_clobber_create"
	writeReplaceSemantics = "non_cas_overwrite"
)

func writeSemanticsForChange(change *preparedChange) string {
	if change.create {
		return writeCreateSemantics
	}
	return writeReplaceSemantics
}

func (r *Registry) writePrepared(ctx context.Context, jobID string, scope Scope, call Call, preparation *Preparation) Output {
	if preparation == nil || preparation.change == nil {
		if stored, ok := r.state(jobID).preparation(call.ID); ok {
			preparation = &Preparation{change: stored}
		}
	}
	if preparation == nil || preparation.change == nil {
		return Fail(errors.New("缺少写入前差异准备，请重新调用工具"))
	}
	change := preparation.change
	files, err := r.fileSystem(ctx, scope)
	if err != nil {
		return Fail(err)
	}
	if _, ok := r.state(jobID).readVersion(change.key); !ok {
		return Fail(ErrReadRequired)
	}
	exists, err := files.Exists(ctx, change.path)
	if err != nil {
		return Fail(err)
	}
	change.create = !change.version.Exists || !exists
	var writeErr error
	if change.create {
		if !change.version.Exists && exists {
			return Fail(ErrFileChanged)
		}
		writer, ok := any(files).(conditional.Writer)
		if !ok {
			return Fail(fmt.Errorf("当前文件服务不支持安全条件创建: %w", base.ErrUnsupported))
		}
		if err := ctx.Err(); err != nil {
			return Fail(err)
		}
		writeErr = writer.WriteFileVersion(ctx, change.path, []byte(change.after), false, conditional.Absent())
	} else {
		if err := ctx.Err(); err != nil {
			return Fail(err)
		}
		writeErr = files.WriteFile(ctx, change.path, []byte(change.after), true)
	}
	if writeErr != nil {
		if errors.Is(writeErr, conditional.ErrVersionMismatch) {
			return Fail(ErrFileChanged)
		}
		if errors.Is(writeErr, conditional.ErrCommitIndeterminate) {
			return r.reconcileIndeterminateWrite(ctx, jobID, scope, call, change, writeErr)
		}
		return Fail(writeErr)
	}
	return r.completeWrite(ctx, jobID, scope, call, change, "committed")
}

func (r *Registry) reconcileIndeterminateWrite(ctx context.Context, jobID string, scope Scope, call Call, change *preparedChange, writeErr error) Output {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	files, err := r.fileSystem(readCtx, scope)
	if err != nil {
		return Fail(fmt.Errorf("%w；解析文件服务以确认结果失败: %v", writeErr, err))
	}
	after, err := files.ReadFile(readCtx, change.path, 2<<20)
	if err != nil {
		return Fail(fmt.Errorf("%w；读取目标以确认结果失败: %v", writeErr, err))
	}
	if !utf8.Valid(after) || string(after) != change.after {
		return Fail(fmt.Errorf("%w；目标内容与本次写入不一致，未重试", writeErr))
	}
	return r.completeWrite(ctx, jobID, scope, call, change, "indeterminate_content_confirmed")
}

func (r *Registry) completeWrite(ctx context.Context, jobID string, scope Scope, call Call, change *preparedChange, outcome string) Output {
	semantics := writeSemanticsForChange(change)
	r.state(jobID).rememberRead(change.key, versionOf([]byte(change.after)))
	var result Output
	if change.create {
		result = OK(fmt.Sprintf("已创建 %s（%d 字节，原子 no-clobber）", change.path, len(change.after)))
	} else if call.Name == "edit_file" {
		result = OK(fmt.Sprintf("已编辑 %s（替换 %d 处，已备份原文件；非事务覆盖，可能覆盖确认后的外部修改）", change.path, change.replacements))
	} else {
		result = OK(fmt.Sprintf("已写入 %s（%d 字节，已备份原文件；非事务覆盖，可能覆盖确认后的外部修改）", change.path, len(change.after)))
	}
	if outcome == "indeterminate_content_confirmed" {
		result.Text += "（原始写入结果不确定，未重试）"
	}
	payload := map[string]any{"path": change.path, "writeSemantics": semantics, "outcome": outcome}
	if call.Name == "edit_file" {
		payload["replacements"] = change.replacements
	} else {
		payload["bytes"] = len(change.after)
	}
	afterCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	files, err := r.fileSystem(afterCtx, scope)
	if err == nil {
		after, readErr := files.ReadFile(ctx, change.path, MaxDiffBytes)
		if readErr == nil && utf8.Valid(after) && change.before.known {
			actual := string(after)
			if actual != change.before.content && len(change.before.content)+len(actual) <= MaxPreviewSize {
				result.Change = &Change{ID: call.ID, Path: change.path, Before: change.before.content, After: actual}
			}
			r.state(jobID).rememberRead(change.key, versionOf(after))
		}
	}
	if r.deps.Audit != nil {
		_ = r.deps.Audit(context.WithoutCancel(ctx), AuditEntry{SessionID: scope.SessionID, AssetID: scope.AssetID, Kind: call.Name, Payload: payload})
	}
	return result
}
