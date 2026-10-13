package server

import (
	"encoding/json"
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// preferenceUpdateRequest uses set/clear to express overwrite writes and "clear to inherit the default".
type preferenceUpdateRequest struct {
	Set   map[string]json.RawMessage `json:"set"`
	Clear []string                   `json:"clear"`
}

type preferenceScopeView struct {
	Defaults  map[string]json.RawMessage `json:"defaults"`
	Overrides map[string]json.RawMessage `json:"overrides"`
	Effective map[string]json.RawMessage `json:"effective"`
}

// mountPreferenceRoutes mounts account-level preference routes; like the account routes they are fully disabled under --auth=off,
// introducing no implicit user or implicit superadmin, and shared workspaces keep using per-end local preferences.
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

// servePreferencesGet returns the session user's three preference layers: defaults + overrides + effective.
func (s *Server) servePreferencesGet(w http.ResponseWriter, r *http.Request) {
	if s.preferences == nil {
		s.preferencesUnavailable(w)
		return
	}
	s.writePreferenceScope(w, r, accountIdentityFrom(r.Context()).UserID)
}

// servePreferencesPut writes only the current session user's own overrides; the user identity comes from the server session only.
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
