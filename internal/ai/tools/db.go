package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (r *Registry) database(scope Scope) (Database, error) {
	if r.deps.Database == nil {
		return nil, fmt.Errorf("数据库服务未配置")
	}
	if scope.ConnID == "" {
		return nil, fmt.Errorf("当前 AI 作用域没有 connId")
	}
	return r.deps.Database, nil
}

func (r *Registry) dbListTables(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Schema string `json:"schema"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	database, err := r.database(scope)
	if err != nil {
		return Fail(err)
	}
	tables, err := database.Tables(ctx, scope.ConnID, args.Schema)
	if err != nil {
		return Fail(err)
	}
	text := strings.Join(tables, "\n")
	if text == "" {
		text = "无表"
	}
	return OK(text)
}

func (r *Registry) dbDescribe(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Schema string `json:"schema"`
		Table  string `json:"table"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if strings.TrimSpace(args.Table) == "" {
		return Fail(invalid("table 不能为空"))
	}
	database, err := r.database(scope)
	if err != nil {
		return Fail(err)
	}
	description, err := database.Describe(ctx, scope.ConnID, args.Schema, args.Table)
	if err != nil {
		return Fail(err)
	}
	encoded, err := json.MarshalIndent(description, "", "  ")
	if err != nil {
		return Fail(err)
	}
	return OK(string(encoded))
}

func (r *Registry) dbQuery(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		SQL string `json:"sql"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if strings.TrimSpace(args.SQL) == "" {
		return Fail(invalid("sql 不能为空"))
	}
	database, err := r.database(scope)
	if err != nil {
		return Fail(err)
	}
	result, err := database.Query(ctx, scope.ConnID, args.SQL, 5000, 30*time.Second)
	if err != nil {
		return Fail(err)
	}
	if result.Error != nil {
		return Output{Text: *result.Error, ExitCode: 1}
	}
	var output strings.Builder
	if len(result.Columns) != 0 {
		output.WriteString(strings.Join(result.Columns, " | "))
		output.WriteByte('\n')
	}
	displayed := len(result.Rows)
	truncated := result.Truncated
	if displayed > 50 {
		displayed = 50
		truncated = true
	}
	for _, row := range result.Rows[:displayed] {
		cells := make([]string, len(row))
		for i, cell := range row {
			encoded, err := json.Marshal(cell)
			if err != nil {
				cells[i] = fmt.Sprint(cell)
			} else {
				cells[i] = string(encoded)
			}
		}
		output.WriteString(strings.Join(cells, " | "))
		output.WriteByte('\n')
	}
	if result.RowsAffected != 0 {
		fmt.Fprintf(&output, "rowsAffected=%d\n", result.RowsAffected)
	}
	if truncated {
		fmt.Fprintf(&output, "仅显示前 %d 行（查询返回 %d 行）\n", displayed, len(result.Rows))
	}
	text, cut := capText(output.String())
	return Output{OK: true, Text: text, Truncated: truncated || cut}
}

func (r *Registry) redisScan(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Pattern string `json:"pattern"`
		Count   uint64 `json:"count"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if args.Pattern == "" {
		args.Pattern = "*"
	}
	if args.Count == 0 {
		args.Count = 100
	}
	if args.Count > 1000 {
		return Fail(invalid("count 不能超过 1000"))
	}
	database, err := r.database(scope)
	if err != nil {
		return Fail(err)
	}
	result, err := database.RedisScan(ctx, scope.ConnID, 0, args.Pattern, args.Count)
	if err != nil {
		return Fail(err)
	}
	var output strings.Builder
	fmt.Fprintf(&output, "cursor=%d keys:\n", result.Cursor)
	for _, key := range result.Keys {
		output.WriteString(key)
		output.WriteByte('\n')
	}
	text, truncated := capText(output.String())
	return Output{OK: true, Text: text, Truncated: truncated}
}
