package ipc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRPCHandlerUsesHTTP200ForCommandErrors(t *testing.T) {
	dispatcher := NewDispatcher()
	if err := Register(dispatcher, "fail", func(context.Context, *Call, struct{}) (any, error) {
		return nil, NewError(CodeForbidden, "操作被拒绝: no")
	}); err != nil {
		t.Fatal(err)
	}
	handler := NewRPCHandler(dispatcher, Environment{})
	request := httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(`{"cmd":"fail","args":null}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got, want := response.Body.String(), "{\"ok\":false,\"error\":{\"code\":\"forbidden\",\"message\":\"操作被拒绝: no\"}}\n"; got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestRPCHandlerRejectsMalformedAndNonPostRequests(t *testing.T) {
	handler := NewRPCHandler(NewDispatcher(), Environment{})
	request := httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(`{"cmd":`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"code":"bad_param"`) {
		t.Fatalf("malformed response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/rpc", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", response.Code)
	}
}
