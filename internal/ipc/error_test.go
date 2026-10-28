package ipc

import (
	"errors"
	"strings"
	"testing"
)

func TestWrapErrorMovesRawDatabaseTextToDetail(t *testing.T) {
	cause := errors.New("SQLITE_BUSY: database is locked")
	e := WrapError(CodeDB, "数据库错误: "+cause.Error(), cause)
	if e.Code != CodeDB {
		t.Fatalf("code changed: %q", e.Code)
	}
	if strings.Contains(e.Message, "SQLITE") {
		t.Fatalf("raw text leaked into the user message: %q", e.Message)
	}
	if raw, _ := e.Detail.(string); !strings.Contains(raw, "SQLITE_BUSY") {
		t.Fatalf("raw text missing from detail: %v", e.Detail)
	}
	if !errors.Is(e, cause) {
		t.Fatal("cause chain lost")
	}
	if !strings.Contains(e.Error(), "SQLITE_BUSY") {
		t.Fatalf("Go error text must keep the raw cause for logs: %q", e.Error())
	}
}

func TestWrapErrorKeepsClearMessages(t *testing.T) {
	e := WrapError(CodeBadParam, "请选择主机", nil)
	if e.Message != "请选择主机" || e.Detail != nil {
		t.Fatalf("unexpected rewrite: %+v", e)
	}
}
