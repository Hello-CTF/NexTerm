package tools

import (
	"context"
	"fmt"
	"strings"
)

func (r *Registry) listAssets(ctx context.Context) Output {
	if r.deps.ListAssets == nil {
		return Fail(fmt.Errorf("资产服务未配置"))
	}
	assets, err := r.deps.ListAssets(ctx)
	if err != nil {
		return Fail(err)
	}
	if len(assets) == 0 {
		return OK("没有资产")
	}
	var output strings.Builder
	for _, asset := range assets {
		fmt.Fprintf(&output, "%s [%s] %s %s\n", asset.Name, asset.Kind, asset.Host, asset.Username)
	}
	text, truncated := capText(output.String())
	return Output{OK: true, Text: text, Truncated: truncated}
}

func askUser(raw []byte) Output {
	var args Question
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	args.Question = strings.TrimSpace(args.Question)
	if args.Question == "" {
		return Fail(invalid("question 不能为空"))
	}
	for i := range args.Options {
		args.Options[i] = strings.TrimSpace(args.Options[i])
		if args.Options[i] == "" {
			return Fail(invalid("选项不能为空"))
		}
	}
	return Output{OK: true, Question: &args}
}

func (r *Registry) todoWrite(jobID string, raw []byte) Output {
	var args struct {
		Todos []TodoItem `json:"todos"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if len(args.Todos) > 50 {
		return Fail(invalid("todos 最多 50 项"))
	}
	var output strings.Builder
	for i := range args.Todos {
		args.Todos[i].Content = strings.TrimSpace(args.Todos[i].Content)
		if args.Todos[i].Content == "" {
			return Fail(invalid("待办内容不能为空"))
		}
		if args.Todos[i].Status == "" {
			args.Todos[i].Status = "pending"
		}
		mark := "[ ]"
		switch args.Todos[i].Status {
		case "pending":
		case "in_progress":
			mark = "[~]"
		case "completed":
			mark = "[x]"
		default:
			return Fail(invalid("未知待办状态 %s", args.Todos[i].Status))
		}
		fmt.Fprintf(&output, "%s %s\n", mark, args.Todos[i].Content)
	}
	r.state(jobID).setTodos(args.Todos)
	return Output{OK: true, Text: output.String(), Todos: append([]TodoItem{}, args.Todos...)}
}

func (r *Registry) exitPlanMode(jobID string, raw []byte) Output {
	var args struct {
		Plan string `json:"plan"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	args.Plan = strings.TrimSpace(args.Plan)
	if args.Plan == "" {
		return Fail(invalid("plan 不能为空"))
	}
	r.state(jobID).setPlan(args.Plan)
	return Output{OK: true, Text: "计划已提交，请等待用户审核；不会自动执行。", Plan: args.Plan}
}
