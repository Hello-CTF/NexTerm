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
	metadataCtx, cancel := context.WithTimeout(ctx, dbMetadataTimeout)
	defer cancel()
	tables, err := database.Tables(metadataCtx, scope.ConnID, args.Schema)
	if err != nil {
		if failure, timedOut := toolTimeout(ctx, metadataCtx, "db_list_tables", dbMetadataTimeout); timedOut {
			return failure
		}
		return Fail(err)
	}
	text := strings.Join(tables, "\n")
	output := OK(text)
	if text == "" {
		output.Text = "无表"
		output.ModelText = "no tables"
	}
	return output
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
	metadataCtx, cancel := context.WithTimeout(ctx, dbMetadataTimeout)
	defer cancel()
	description, err := database.Describe(metadataCtx, scope.ConnID, args.Schema, args.Table)
	if err != nil {
		if failure, timedOut := toolTimeout(ctx, metadataCtx, "db_describe", dbMetadataTimeout); timedOut {
			return failure
		}
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
		return Output{Text: *result.Error, ModelText: *result.Error, ExitCode: 1}
	}
	var output, modelOutput strings.Builder
	writeShared := func(text string) {
		output.WriteString(text)
		modelOutput.WriteString(text)
	}
	if len(result.Columns) != 0 {
		writeShared(strings.Join(result.Columns, " | ") + "\n")
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
		writeShared(strings.Join(cells, " | ") + "\n")
	}
	if result.RowsAffected != 0 {
		writeShared(fmt.Sprintf("rowsAffected=%d\n", result.RowsAffected))
	}
	if truncated {
		fmt.Fprintf(&output, "仅显示前 %d 行（查询返回 %d 行）\n", displayed, len(result.Rows))
		fmt.Fprintf(&modelOutput, "showing only the first %d rows (query returned %d rows)\n", displayed, len(result.Rows))
	}
	text, cut := capText(output.String())
	modelText, modelCut := capText(modelOutput.String())
	if modelCut {
		modelText = strings.TrimSuffix(modelText, "\n[输出已截断]") + "\n[output truncated]"
	}
	return Output{OK: true, Text: text, ModelText: modelText, Truncated: truncated || cut}
}

// dbMetadataTimeout bounds db_list_tables and db_describe, so a database that stops answering fails the call
// instead of holding it until the run is canceled.
var dbMetadataTimeout = 30 * time.Second

// redis_scan budgets: one call keeps issuing SCAN until the iteration completes or one of these is reached.
const (
	redisScanKeyBudget  = 300
	redisScanTimeBudget = 5 * time.Second
	// redisScanTimeout bounds the call including a SCAN still in flight when the time budget runs out.
	redisScanTimeout = 15 * time.Second
)

func (r *Registry) redisScan(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Pattern string `json:"pattern"`
		Count   uint64 `json:"count"`
		Cursor  uint64 `json:"cursor"`
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
	scanCtx, cancel := context.WithTimeout(ctx, redisScanTimeout)
	defer cancel()
	started := time.Now()
	cursor := args.Cursor
	seen := make(map[string]struct{})
	var keys []string
	scans := 0
	for {
		result, err := database.RedisScan(scanCtx, scope.ConnID, cursor, args.Pattern, args.Count)
		if err != nil {
			if failure, timedOut := toolTimeout(ctx, scanCtx, "redis_scan", redisScanTimeout); timedOut {
				return failure
			}
			return Fail(err)
		}
		scans++
		for _, key := range result.Keys {
			// SCAN may return a key more than once during one iteration.
			if _, duplicate := seen[key]; !duplicate {
				seen[key] = struct{}{}
				keys = append(keys, key)
			}
		}
		// A cursor that does not advance would repeat the same page forever.
		stalled := result.Cursor == cursor
		cursor = result.Cursor
		if cursor == 0 || stalled || len(keys) >= redisScanKeyBudget || time.Since(started) >= redisScanTimeBudget {
			break
		}
	}
	var output strings.Builder
	fmt.Fprintf(&output, "cursor=%d keys:\n", cursor)
	for _, key := range keys {
		output.WriteString(key)
		output.WriteByte('\n')
	}
	text, truncated := capText(output.String())
	modelText := text
	if truncated {
		modelText = strings.TrimSuffix(text, "\n[输出已截断]") + "\n[output truncated]"
	}
	if cursor != 0 {
		text += fmt.Sprintf("\n[cursor≠0：可能还有更多匹配的键（本次执行 %d 次 SCAN）；继续扫描请传 cursor=%d]", scans, cursor)
		modelText += fmt.Sprintf("\n[cursor≠0: more keys may match (this call ran %d SCAN iterations); to continue, call redis_scan with cursor=%d]", scans, cursor)
	} else {
		text += fmt.Sprintf("\n[扫描已完成（本次执行 %d 次 SCAN）]", scans)
		modelText += fmt.Sprintf("\n[scan complete (this call ran %d SCAN iterations)]", scans)
	}
	return Output{OK: true, Text: text, ModelText: modelText, Truncated: truncated}
}
