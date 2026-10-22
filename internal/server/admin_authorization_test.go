package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func adminAuthorizationRequest(route adminRoute, disableTargetID, resetTargetID string) (string, string, any) {
	method, path, _ := strings.Cut(route.pattern, " ")
	var body any
	switch route.pattern {
	case "POST /admin/users":
		body = map[string]any{"username": "matrix-created", "password": "matrix-password1"}
	case "POST /admin/users/{id}/disable":
		path = "/admin/users/" + disableTargetID + "/disable"
	case "POST /admin/users/{id}/reset":
		path = "/admin/users/" + resetTargetID + "/reset"
	case "PUT /admin/settings":
		body = map[string]any{"registration_open": true}
	case "PUT /admin/preferences":
		body = map[string]any{"set": map[string]any{
			"appearance.terminalTheme":      "dark",
			"appearance.terminalFontFamily": "cascadia",
			"appearance.terminalContrast":   "standard",
		}}
	}
	return method, path, body
}

func TestAdminRouteAuthorizationMatrix(t *testing.T) {
	fixture := newPreferenceFixture(t, AuthOn)
	admin, _ := fixture.initSuperadmin(t, "root", "root-password1")
	user := fixture.createUser(t, "ordinary-user", "user-password1")
	disableTarget, err := fixture.accounts.CreateUser(context.Background(), "disable-target", "", "disable-password1")
	if err != nil {
		t.Fatal(err)
	}
	resetTarget, err := fixture.accounts.CreateUser(context.Background(), "reset-target", "", "reset-password1")
	if err != nil {
		t.Fatal(err)
	}

	routes := fixture.server.adminRoutes()
	if got, want := len(routes), 8; got != want {
		t.Errorf("admin route count = %d, want %d; update the ratchet only for an intentional route surface change", got, want)
	}
	for _, route := range routes {
		method, path, body := adminAuthorizationRequest(route, disableTarget.ID, resetTarget.ID)
		t.Run(route.name, func(t *testing.T) {
			adminWithoutCSRFStatus := http.StatusOK
			if method != http.MethodGet && method != http.MethodHead {
				adminWithoutCSRFStatus = http.StatusForbidden
			}
			scenarios := []struct {
				name    string
				session *accountTestSession
				csrf    string
				want    int
			}{
				{name: "anonymous", want: http.StatusUnauthorized},
				{name: "ordinary_user", session: user, csrf: user.csrf, want: http.StatusForbidden},
				{name: "admin_without_csrf", session: admin, want: adminWithoutCSRFStatus},
				{name: "admin_with_csrf", session: admin, csrf: admin.csrf, want: http.StatusOK},
			}
			for _, scenario := range scenarios {
				t.Run(scenario.name, func(t *testing.T) {
					call := fixture.call(t, method, path, body, scenario.session, scenario.csrf, nil)
					if call.status != scenario.want {
						t.Fatalf("%s %s status=%d body=%v, want %d", method, path, call.status, call.body, scenario.want)
					}
				})
			}
		})
	}
}
