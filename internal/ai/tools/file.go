package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
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
	if change.version.Exists {
		current, err := files.ReadFile(ctx, change.path, 2<<20)
		if err != nil || versionOf(current) != change.version {
			return Fail(ErrFileChanged)
		}
	} else {
		exists, err := files.Exists(ctx, change.path)
		if err != nil {
			return Fail(err)
		}
		if exists {
			return Fail(ErrFileChanged)
		}
	}
	if err := ctx.Err(); err != nil {
		return Fail(err)
	}
	if err := files.WriteFile(ctx, change.path, []byte(change.after), true); err != nil {
		return Fail(err)
	}
	r.state(jobID).rememberRead(change.key, versionOf([]byte(change.after)))
	result := OK(fmt.Sprintf("已写入 %s（%d 字节，已备份原文件）", change.path, len(change.after)))
	kind := "write_file"
	payload := map[string]any{"path": change.path, "bytes": len(change.after)}
	if call.Name == "edit_file" {
		result = OK(fmt.Sprintf("已编辑 %s（替换 %d 处，已备份原文件）", change.path, change.replacements))
		kind = "edit_file"
		payload = map[string]any{"path": change.path, "replacements": change.replacements}
	}
	after, readErr := files.ReadFile(ctx, change.path, MaxDiffBytes)
	if readErr == nil && utf8.Valid(after) && change.before.known {
		actual := string(after)
		if actual != change.before.content && len(change.before.content)+len(actual) <= MaxPreviewSize {
			result.Change = &Change{ID: call.ID, Path: change.path, Before: change.before.content, After: actual}
		}
		r.state(jobID).rememberRead(change.key, versionOf(after))
	}
	if r.deps.Audit != nil {
		_ = r.deps.Audit(context.WithoutCancel(ctx), AuditEntry{SessionID: scope.SessionID, AssetID: scope.AssetID, Kind: kind, Payload: payload})
	}
	return result
}
