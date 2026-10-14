package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestNativeGenerateHonorsOverrideToolsFallbackAndMetadata(t *testing.T) {
	provider := &fakeProvider{steps: []fakeStep{errorStep(http.StatusTooManyRequests), blockStep("served-backup", "ok")}}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 0, nil, nil)
	base, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	chatModel, err := base.WithTools([]*schema.ToolInfo{{Name: "write", Desc: "write a file"}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := chatModel.Generate(context.Background(), []*schema.Message{{Role: schema.RoleType("user"), Content: "hi"}}, model.WithModel("override"))
	if err != nil {
		t.Fatal(err)
	}
	requests := provider.Requests()
	if !reflect.DeepEqual(requestModels(requests), []string{"override", "backup"}) || requests[1].toolCount != 1 {
		t.Fatalf("native requests = %+v", requests)
	}
	if message.Content != "ok" {
		t.Fatalf("native message = %+v", message)
	}
	if extraString(message.Extra, MessageExtraRunID) == "" || extraString(message.Extra, MessageExtraCallID) == "" ||
		extraString(message.Extra, MessageExtraServingModel) != "served-backup" || extraUint64(message.Extra, MessageExtraContextWindow) != 1000 {
		t.Fatalf("native metadata = %+v", message.Extra)
	}
}

func TestNativeStreamHonorsOverrideToolsFallbackAndMetadata(t *testing.T) {
	provider := &fakeProvider{steps: []fakeStep{errorStep(http.StatusInternalServerError), streamStep("served-backup", "stream-ok")}}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup", Stream: true}, 0, nil, nil)
	base, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	chatModel, err := base.WithTools([]*schema.ToolInfo{{Name: "write"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := chatModel.Stream(context.Background(), []*schema.Message{{Role: schema.RoleType("user"), Content: "hi"}}, model.WithModel("override"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var messages []*schema.Message
	for {
		message, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	if len(messages) == 0 || messages[0].Content != "stream-ok" {
		t.Fatalf("native stream messages = %+v", messages)
	}
	runID := extraString(messages[0].Extra, MessageExtraRunID)
	callID := extraString(messages[0].Extra, MessageExtraCallID)
	servingModel := extraString(messages[0].Extra, MessageExtraServingModel)
	contextWindow := extraUint64(messages[0].Extra, MessageExtraContextWindow)
	for _, message := range messages[1:] {
		if extraString(message.Extra, MessageExtraRunID) != runID || extraString(message.Extra, MessageExtraCallID) != callID ||
			extraString(message.Extra, MessageExtraServingModel) != servingModel || extraUint64(message.Extra, MessageExtraContextWindow) != contextWindow {
			t.Fatalf("frame metadata = %+v, want %s/%s/%s/%d", message.Extra, runID, callID, servingModel, contextWindow)
		}
	}
	if runID == "" || callID == "" || servingModel != "served-backup" || contextWindow != 1000 {
		t.Fatalf("stream metadata = %s/%s/%s/%d", runID, callID, servingModel, contextWindow)
	}
	requests := provider.Requests()
	if !reflect.DeepEqual(requestModels(requests), []string{"override", "backup"}) || !requests[1].stream || requests[1].toolCount != 1 {
		t.Fatalf("native stream requests = %+v", requests)
	}
}

func TestNativeStreamDoesNotReplayAfterFirstFrame(t *testing.T) {
	provider := &fakeProvider{steps: []fakeStep{{
		status: http.StatusOK,
		body:   "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: invalid\n\n",
	}, streamStep("backup", "must-not-run")}}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup", Stream: true}, 3, nil, nil)
	chatModel, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := chatModel.Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var contents []string
	for {
		message, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			t.Fatal("stream ended without the provider error")
		}
		if recvErr != nil {
			break
		}
		contents = append(contents, message.Content)
	}
	if !reflect.DeepEqual(contents, []string{"partial"}) {
		t.Fatalf("replayed native frames = %v", contents)
	}
	if got := requestModels(provider.Requests()); !reflect.DeepEqual(got, []string{"primary"}) {
		t.Fatalf("post-frame native requests = %v", got)
	}
}

func nativeProjectionInput() []*schema.Message {
	content := `{"ok":true,"text":"无表","modelText":"no tables","exitCode":0,"truncated":false,"custom":{"label":"保留"}}`
	return []*schema.Message{
		schema.UserMessage("用户数据保持中文"),
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "db_list_tables", Arguments: `{}`}}}),
		schema.ToolMessage(content, "call-1"),
		schema.AssistantMessage("模型中文回答保持不变", nil),
	}
}

func requireNativeProjectionRequests(t *testing.T, requests []recordedRequest) {
	t.Helper()
	if len(requests) == 0 {
		t.Fatal("no native requests captured")
	}
	for index, request := range requests {
		if len(request.messages) != 4 {
			t.Fatalf("request %d messages = %+v", index, request.messages)
		}
		if got := string(request.messages[0]["content"]); got != `"用户数据保持中文"` {
			t.Errorf("request %d user content = %s", index, got)
		}
		if got := string(request.messages[3]["content"]); got != `"模型中文回答保持不变"` {
			t.Errorf("request %d assistant content = %s", index, got)
		}
		var content string
		if err := json.Unmarshal(request.messages[2]["content"], &content); err != nil {
			t.Fatalf("request %d tool content: %v", index, err)
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(content), &envelope); err != nil {
			t.Fatalf("request %d tool envelope: %v", index, err)
		}
		if envelope["text"] != "no tables" {
			t.Errorf("request %d model text = %v", index, envelope["text"])
		}
		if _, exists := envelope["modelText"]; exists {
			t.Errorf("request %d leaked modelText: %+v", index, envelope)
		}
		custom, _ := envelope["custom"].(map[string]any)
		if custom["label"] != "保留" {
			t.Errorf("request %d unknown metadata = %+v", index, envelope["custom"])
		}
	}
}

func TestNativeGenerateProjectsBeforeRetryAndFallback(t *testing.T) {
	provider := &fakeProvider{steps: []fakeStep{
		errorStep(http.StatusInternalServerError), errorStep(http.StatusTooManyRequests),
		errorStep(http.StatusInternalServerError), blockStep("backup", "ok"),
	}}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 1, nil, nil)
	chatModel, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	input := nativeProjectionInput()
	original := input[2].Content
	if _, err := chatModel.Generate(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	requests := provider.Requests()
	if len(requests) != 4 {
		t.Fatalf("native requests = %d, want retry and fallback total 4", len(requests))
	}
	requireNativeProjectionRequests(t, requests)
	if input[2].Content != original {
		t.Fatalf("runNative mutated its input: %s", input[2].Content)
	}
}

func TestNativeStreamProjectsBeforeFallback(t *testing.T) {
	provider := &fakeProvider{steps: []fakeStep{errorStep(http.StatusInternalServerError), streamStep("backup", "ok")}}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup", Stream: true}, 0, nil, nil)
	chatModel, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	input := nativeProjectionInput()
	original := input[2].Content
	reader, err := chatModel.Stream(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for {
		_, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	requireNativeProjectionRequests(t, provider.Requests())
	if input[2].Content != original {
		t.Fatalf("runNative mutated its input: %s", input[2].Content)
	}
}

func TestProjectModelMessagesIsCloneAndIdempotent(t *testing.T) {
	input := nativeProjectionInput()
	original := input[2].Content
	first := ProjectModelMessages(input)
	second := ProjectModelMessages(first)
	if first[2] == input[2] || first[0] == input[0] {
		t.Fatal("projection reused input message pointers")
	}
	if input[2].Content != original {
		t.Fatalf("projection mutated source content: %s", input[2].Content)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("projection is not idempotent:\nfirst=%+v\nsecond=%+v", first, second)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(second[2].Content), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["text"] != "no tables" {
		t.Fatalf("projected text = %v", envelope["text"])
	}
	if _, exists := envelope["modelText"]; exists {
		t.Fatalf("modelText leaked: %+v", envelope)
	}
}

func TestModelTextCatalogStaticEntries(t *testing.T) {
	catalogs := []struct {
		name    string
		entries map[string]string
	}{
		{name: "primary", entries: modelStaticText},
		{name: "takeover cron and store", entries: modelAdditionalStaticText},
	}
	for _, catalog := range catalogs {
		for source, want := range catalog.entries {
			t.Run(fmt.Sprintf("%s/%x", catalog.name, len(source)+len(want)), func(t *testing.T) {
				if got := projectMachineText(source); got != want {
					t.Fatalf("projection = %q, want %q", got, want)
				}
			})
		}
	}
}

func formatCatalogExample(t *testing.T, format string, projected bool) string {
	t.Helper()
	tokens, err := parseModelTextFormat(format)
	if err != nil {
		t.Fatal(err)
	}
	maxArgument := -1
	for _, token := range tokens {
		if token.verb != 0 {
			maxArgument = max(maxArgument, token.argument)
		}
	}
	args := make([]any, maxArgument+1)
	for _, token := range tokens {
		if token.verb == 0 {
			continue
		}
		switch token.verb {
		case 'd':
			args[token.argument] = 7
		case 'v', 'w':
			args[token.argument] = "连接已断开：数据库服务已关闭，请重新连接"
			if projected {
				args[token.argument] = "disconnected: database service is closed; reconnect"
			}
		case 'e':
			args[token.argument] = "内部错误: Redis TYPE 失败: 没有资产"
			if projected {
				args[token.argument] = "internal error: Redis TYPE failed: 没有资产"
			}
		default:
			args[token.argument] = "用户「数据」"
		}
	}
	format = strings.NewReplacer("%w", "%v", "%e", "%v").Replace(format)
	return fmt.Sprintf(format, args...)
}

func TestModelTextCatalogTemplates(t *testing.T) {
	for index, spec := range modelTextTemplateSpecs {
		t.Run(fmt.Sprintf("template-%03d", index), func(t *testing.T) {
			input := formatCatalogExample(t, spec.source, false)
			want := formatCatalogExample(t, spec.target, true)
			if got := projectMachineText(input); got != want {
				t.Fatalf("projection = %q, want %q\nsource format = %q", got, want, spec.source)
			}
		})
	}
}

func TestModelProjectionPreservesFreeContentAndRealNames(t *testing.T) {
	tests := []struct {
		name string
		view modelResultView
		want string
	}{
		{name: "file content is never statically translated", view: modelResultView{Tool: "read_file", OK: true, Text: "没有资产\n用户中文"}, want: "没有资产\n用户中文"},
		{name: "legacy empty table text stays ambiguous", view: modelResultView{Tool: "db_list_tables", OK: true, Text: "无表"}, want: "无表"},
		{name: "legacy empty search text stays ambiguous", view: modelResultView{Tool: "search_files", OK: true, Text: "无匹配"}, want: "无匹配"},
		{name: "legacy docker log text stays ambiguous", view: modelResultView{Tool: "docker_logs", OK: true, Text: "日志为空"}, want: "日志为空"},
		{name: "legacy docker output text stays ambiguous", view: modelResultView{Tool: "docker_exec", OK: true, Text: "(无输出)"}, want: "(无输出)"},
		{name: "subagent answer stays Chinese", view: modelResultView{Tool: "done", OK: true, Text: "没有资产\n模型中文回答"}, want: "没有资产\n模型中文回答"},
		{name: "failed done summary stays Chinese", view: modelResultView{Tool: "done", OK: false, Text: "没有资产\n失败摘要"}, want: "没有资产\n失败摘要"},
		{name: "normal subagent answer stays Chinese", view: modelResultView{Tool: "spawn_subagent", OK: true, Text: "没有资产\n子代理中文答案"}, want: "没有资产\n子代理中文答案"},
		{name: "subagent panic translates only the outer error", view: modelResultView{Tool: "spawn_subagent", Text: "工具 spawn_subagent 执行时崩溃：没有资产", Panic: true}, want: "tool spawn_subagent panicked during execution: 没有资产"},
		{name: "legacy SQL alias is not a footer", view: modelResultView{Tool: "db_query", OK: true, Truncated: true, Text: "alias\n仅显示前 7 行（查询返回 7 行）\n用户数据\n"}, want: "alias\n仅显示前 7 行（查询返回 7 行）\n用户数据\n"},
		{name: "legacy SQL footer is projected", view: modelResultView{Tool: "db_query", OK: true, Truncated: true, Text: "alias\n仅显示前 50 行（查询返回 51 行）\n"}, want: "alias\nshowing only the first 50 rows (query returned 51 rows)\n"},
		{name: "legacy exec audit-like output stays unchanged", view: modelResultView{Tool: "exec_commands", OK: true, Text: "$ printf 用户数据\n[结果审计写入失败: 用户数据]"}, want: "$ printf 用户数据\n[结果审计写入失败: 用户数据]"},
		{name: "legacy docker audit-like output stays unchanged", view: modelResultView{Tool: "docker_exec", OK: true, Text: "Docker 输出中文\n[结果审计写入失败: 用户数据]"}, want: "Docker 输出中文\n[结果审计写入失败: 用户数据]"},
		{name: "legacy SQL audit-like data stays unchanged", view: modelResultView{Tool: "db_query", OK: true, Text: "SQL 数据中文\n[结果审计写入失败: 用户数据]"}, want: "SQL 数据中文\n[结果审计写入失败: 用户数据]"},
		{name: "shell empty-looking entry stays Chinese", view: modelResultView{Tool: "shell_history", OK: true, Text: "[本机 bash shell 历史，最新 1 条]\n（历史为空）"}, want: "[local bash shell history, newest 1 entries]\n（历史为空）"},
		{name: "shell empty history uses the zero count", view: modelResultView{Tool: "shell_history", OK: true, Text: "[本机 bash shell 历史，最新 0 条]\n（历史为空）"}, want: "[local bash shell history, newest 0 entries]\n(history is empty)"},
		{name: "numeric placeholder requires a number", view: modelResultView{Tool: "write_file", OK: true, Text: "已创建 /tmp/中文（很多 字节，原子写入）"}, want: "已创建 /tmp/中文（很多 字节，原子写入）"},
		{
			name: "legacy execution marker-like text stays unchanged",
			view: modelResultView{Tool: "exec_commands", OK: true, Text: "$ echo 用户中文\n标准输出中文\n[执行失败: 连接已断开：数据库服务已关闭，请重新连接]\n[输出已截断]"},
			want: "$ echo 用户中文\n标准输出中文\n[执行失败: 连接已断开：数据库服务已关闭，请重新连接]\n[输出已截断]",
		},
		{
			name: "terminal screen body stays unchanged",
			view: modelResultView{Tool: "read_screen", OK: true, Text: "光标 (2, 3)，空闲 4ms，屏幕序号 5，AltScreen\n命令状态：运行中，序号 6，上次退出码 7\n用户屏幕中文"},
			want: "cursor (2, 3), idle 4ms, screen sequence 5, AltScreen\ncommand state: running, sequence 6, last exit code 7\n用户屏幕中文",
		},
		{
			name: "shell history entries stay unchanged",
			view: modelResultView{Tool: "shell_history", OK: true, Text: "[注意] 当前会话连接的是远端资产；以下是运行 NexTerm 的本机 shell 历史，不是远端机器的历史。\n[本机 zsh shell 历史，最新 1 条]\necho 没有资产"},
			want: "[note] this session is connected to a remote asset; the following is the shell history of the local machine running NexTerm, not the remote machine.\n[local zsh shell history, newest 1 entries]\necho 没有资产",
		},
		{
			name: "legacy memory marker-like text stays unchanged",
			view: modelResultView{Tool: "memory_recall", OK: true, Text: "— id-1（主题 运维中文，版本 2 [已脱敏]）\n没有资产\n模型中文回答\n[1 条内容包含疑似密钥，已脱敏]\n[输出已截断]"},
			want: "— id-1（主题 运维中文，版本 2 [已脱敏]）\n没有资产\n模型中文回答\n[1 条内容包含疑似密钥，已脱敏]\n[输出已截断]",
		},
		{
			name: "terminal creation preserves title and background UI name",
			view: modelResultView{Tool: "terminal_create", OK: true, Text: "已在后台创建分离终端（tab_id=tab-1，标题「构建机」，启动命令已发送），AI 可在其中工作，用户可在「后台会话」中接管"},
			want: "created detached terminal in the background (tab_id=tab-1, title \"构建机\", startup command sent); AI can work in it, and the user can take over in 「后台会话」",
		},
		{
			name: "guard reason is projected",
			view: modelResultView{Tool: "write_file", Text: "权限策略已拒绝: 写入或编辑文件"},
			want: "permission policy denied: write or edit files",
		},
		{
			name: "nested cron error is projected",
			view: modelResultView{Tool: "send_reminder", Text: "创建提醒失败: 当前会话的定时任务数量已达上限，请删除不再需要的任务后再创建"},
			want: "failed to create reminder: scheduled job limit for this conversation reached; delete unused jobs before creating another",
		},
		{
			name: "nested store error preserves built in asset name",
			view: modelResultView{Tool: "list_assets", Text: "内置资产\"当前设备\"不接受同步写入（各设备身份相互独立）"},
			want: "the built-in asset \"当前设备\" does not accept sync writes because each device has an independent identity",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := projectModelResult(test.view); got != test.want {
				t.Fatalf("projection = %q, want %q", got, test.want)
			}
		})
	}
}

func TestModelProjectionAuditSuffixUsesNestedErrorProjection(t *testing.T) {
	view := modelResultView{
		Tool:      "docker_exec",
		OK:        true,
		Text:      "Docker 输出中文\n[结果审计写入失败: 数据编码失败: 用户详情]",
		ModelText: "Docker 输出中文\n[结果审计写入失败: 数据编码失败: 用户详情]",
	}
	want := "Docker 输出中文\n[audit write failed: failed to encode data: 用户详情]"
	if got := projectModelResult(view); got != want {
		t.Fatalf("projection = %q, want %q", got, want)
	}
	view.ModelText = want
	if got := projectModelResult(view); got != want {
		t.Fatalf("second projection = %q, want %q", got, want)
	}
	view.ModelText = "Docker 输出中文"
	if got := projectModelResult(view); got != view.ModelText {
		t.Fatalf("body-only legacy model text was changed: %q", got)
	}
}

func TestModelProjectionLegacyAuditSuffixUsesNestedErrorProjection(t *testing.T) {
	const uiSuffix = "\n[结果审计写入失败: 数据编码失败: 用户详情]"
	const modelSuffix = "\n[audit write failed: failed to encode data: 用户详情]"
	for _, test := range []struct {
		tool   string
		prefix string
	}{
		{tool: "exec_commands", prefix: "$ printf 用户数据\n标准输出中文"},
		{tool: "docker_exec", prefix: "Docker 输出中文\n没有资产"},
		{tool: "db_query", prefix: "SQL 数据中文\n没有资产"},
	} {
		t.Run(test.tool, func(t *testing.T) {
			content, err := json.Marshal(map[string]any{"ok": true, "text": test.prefix + uiSuffix, "exitCode": 0, "truncated": false})
			if err != nil {
				t.Fatal(err)
			}
			message := schema.ToolMessage(string(content), "legacy-"+test.tool)
			message.ToolName = test.tool
			original := message.Content
			first := ProjectModelMessages([]*schema.Message{message})
			if message.Content != original {
				t.Fatalf("UI record changed: %q", message.Content)
			}
			var envelope struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(first[0].Content), &envelope); err != nil {
				t.Fatal(err)
			}
			want := test.prefix + modelSuffix
			if envelope.Text != want {
				t.Fatalf("model text = %q, want %q", envelope.Text, want)
			}
			if strings.Count(envelope.Text, "[audit write failed:") != 1 {
				t.Fatalf("audit suffix was duplicated: %q", envelope.Text)
			}
			second := ProjectModelMessages(first)
			if second[0].Content != first[0].Content {
				t.Fatalf("second projection changed the record:\nfirst=%s\nsecond=%s", first[0].Content, second[0].Content)
			}
		})
	}
}

func TestProjectModelMessagesReadsSubagentPanicField(t *testing.T) {
	messages := []*schema.Message{
		schema.ToolMessage(`{"ok":false,"text":"工具 spawn_subagent 执行时崩溃：没有资产","exitCode":1,"truncated":false,"panic":true}`, "panic"),
		schema.ToolMessage(`{"ok":true,"text":"没有资产\n子代理中文答案","exitCode":0,"truncated":false,"panic":false}`, "normal"),
	}
	for _, message := range messages {
		message.ToolName = "spawn_subagent"
	}
	projected := ProjectModelMessages(messages)
	var panicEnvelope, normalEnvelope map[string]any
	if err := json.Unmarshal([]byte(projected[0].Content), &panicEnvelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(projected[1].Content), &normalEnvelope); err != nil {
		t.Fatal(err)
	}
	if panicEnvelope["text"] != "tool spawn_subagent panicked during execution: 没有资产" {
		t.Fatalf("panic projection = %q", panicEnvelope["text"])
	}
	if normalEnvelope["text"] != "没有资产\n子代理中文答案" {
		t.Fatalf("normal subagent answer changed: %q", normalEnvelope["text"])
	}
}

func TestModelProjectionTruncationIsIdempotent(t *testing.T) {
	view := modelResultView{Tool: "search_files", OK: true, Text: "用户数据", Truncated: true}
	if got := projectModelResult(view); got != "用户数据" {
		t.Fatalf("truncation without a marker invented output: %q", got)
	}
	view.Text = "用户数据\n[输出已截断]"
	first := projectModelResult(view)
	if first != "用户数据\n[output truncated]" {
		t.Fatalf("first projection = %q", first)
	}
	view.Text = first
	if second := projectModelResult(view); second != first {
		t.Fatalf("second projection = %q, want %q", second, first)
	}
}

func TestProjectModelMessagesRecoversToolNameFromCallID(t *testing.T) {
	input := []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Function: schema.FunctionCall{Name: "read_file", Arguments: `{}`}}}),
		schema.ToolMessage(`{"ok":true,"text":"没有资产","exitCode":0,"truncated":false}`, "call-1"),
	}
	projected := ProjectModelMessages(input)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(projected[1].Content), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["text"] != "没有资产" {
		t.Fatalf("tool name was not recovered from call ID: %+v", envelope)
	}
}

func TestProjectModelMessagesLeavesUnrecognizedJSONUntouched(t *testing.T) {
	const content = `{"text":"没有资产","custom":"用户中文"}`
	message := schema.ToolMessage(content, "call-1")
	projected := ProjectModelMessages([]*schema.Message{message})
	if projected[0].Content != content {
		t.Fatalf("unrecognized JSON changed: %s", projected[0].Content)
	}
}
