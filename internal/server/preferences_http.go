package server

import (
	"encoding/json"
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// preferenceUpdateRequest 用 set/clear 表达覆盖写入与「清除以继承默认」。
type preferenceUpdateRequest struct {
	Set   map[string]json.RawMessage `json:"set"`
	Clear []string                   `json:"clear"`
}

type preferenceScopeView struct {
	Defaults  map[string]json.RawMessage `json:"defaults"`
	Overrides map[string]json.RawMessage `json:"overrides"`
	Effective map[string]json.RawMessage `json:"effective"`
}

// mountPreferenceRoutes 挂载账号级偏好路由; 与账号路由一样在 --auth=off 下整体关闭,
// 不引入隐式用户或隐式超管, 共享工作区继续走各端本地偏好。
func (s *Server) mountPreferenceRoutes(mux *http.ServeMux) {
	mux.Handle("GET /auth/preferences", s.accountGuard(s.requireAccountSession(http.HandlerFunc(s.servePreferencesGet))))
	mux.Handle("PUT /auth/preferences", s.accountGuard(s.requireAccountSession(s.requireAccountCSRF(http.HandlerFunc(s.servePreferencesPut)))))
	mux.Handle("GET /admin/preferences", s.accountGuard(s.requireAccountSession(s.requireSuperadmin(http.HandlerFunc(s.serveAdminPreferencesGet)))))
	mux.Handle("PUT /admin/preferences", s.accountGuard(s.requireAccountSession(s.requireAccountCSRF(s.requireSuperadmin(http.HandlerFunc(s.serveAdminPreferencesPut))))))
}

func (s *Server) preferencesUnavailable(w http.ResponseWriter) {
	writeAccountError(w, http.StatusInternalServerError, ipc.NewError(ipc.CodeInternal, "设置存储不可用"))
}

func (s *Server) writePreferenceScope(w http.ResponseWriter, r *http.Request, userID string) {
	defaults, err := s.preferences.Defaults(r.Context())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	overrides, err := s.preferences.UserOverrides(r.Context(), userID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, preferenceScopeView{
		Defaults:  defaults,
		Overrides: overrides,
		Effective: account.MergePreferences(defaults, overrides),
	})
}

// servePreferencesGet 返回会话用户的 默认+覆盖+生效 三层偏好。
func (s *Server) servePreferencesGet(w http.ResponseWriter, r *http.Request) {
	if s.preferences == nil {
		s.preferencesUnavailable(w)
		return
	}
	s.writePreferenceScope(w, r, accountIdentityFrom(r.Context()).UserID)
}

// servePreferencesPut 只写当前会话用户自己的覆盖, 用户标识只取自服务端会话。
func (s *Server) servePreferencesPut(w http.ResponseWriter, r *http.Request) {
	if s.preferences == nil {
		s.preferencesUnavailable(w)
		return
	}
	var request preferenceUpdateRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	identity := accountIdentityFrom(r.Context())
	if _, err := s.preferences.UpdateUserOverrides(r.Context(), identity.UserID, request.Set, request.Clear); err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.writePreferenceScope(w, r, identity.UserID)
}

type preferenceDefaultsView struct {
	Defaults map[string]json.RawMessage `json:"defaults"`
}

func (s *Server) serveAdminPreferencesGet(w http.ResponseWriter, r *http.Request) {
	if s.preferences == nil {
		s.preferencesUnavailable(w)
		return
	}
	defaults, err := s.preferences.Defaults(r.Context())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, preferenceDefaultsView{Defaults: defaults})
}

func (s *Server) serveAdminPreferencesPut(w http.ResponseWriter, r *http.Request) {
	if s.preferences == nil {
		s.preferencesUnavailable(w)
		return
	}
	var request preferenceUpdateRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	defaults, err := s.preferences.UpdateDefaults(r.Context(), request.Set, request.Clear)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, preferenceDefaultsView{Defaults: defaults})
}
