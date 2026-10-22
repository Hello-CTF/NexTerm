package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type totpBindingBodyGate struct {
	entered  chan struct{}
	released chan struct{}
	body     *strings.Reader
	once     sync.Once
}

func (g *totpBindingBodyGate) Read(p []byte) (int, error) {
	g.once.Do(func() {
		close(g.entered)
		<-g.released
	})
	return g.body.Read(p)
}

func (*totpBindingBodyGate) Close() error { return nil }

func TestTOTPBindingRejectsSessionRevokedDuringBodyRead(t *testing.T) {
	for _, operation := range []string{"setup", "confirm"} {
		t.Run(operation, func(t *testing.T) {
			fixture := newAccountFixture(t, AuthOn)
			fixture.createUser(t, "root", "password-a1")
			first := fixture.login(t, "root", "password-a1")
			second := fixture.login(t, "root", "password-a1")
			userID := first.user["id"].(string)
			firstSessionID := currentAccountSessionID(t, fixture, first)
			gate := &totpBindingBodyGate{
				entered: make(chan struct{}), released: make(chan struct{}), body: strings.NewReader(`{}`),
			}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate.released) }) }
			defer release()
			request := httptest.NewRequest(http.MethodPost, "/auth/totp/"+operation, gate)
			request.AddCookie(first.cookie)
			request.Header.Set(csrfHeaderName, first.csrf)
			request.Header.Set("Content-Type", "application/json")
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				recorder := httptest.NewRecorder()
				fixture.server.Handler().ServeHTTP(recorder, request)
				done <- recorder
			}()
			select {
			case <-gate.entered:
			case recorder := <-done:
				t.Fatalf("request did not reach body read: %d %s", recorder.Code, recorder.Body.String())
			case <-time.After(5 * time.Second):
				t.Fatal("request did not reach body read")
			}
			revoke := fixture.call(t, http.MethodDelete, "/auth/sessions/"+firstSessionID, nil, second, second.csrf, nil)
			if revoke.status != http.StatusOK {
				t.Fatalf("revoke status = %d body = %v", revoke.status, revoke.body)
			}
			if operation == "confirm" {
				// A new setup under the surviving session belongs to the current
				// epoch. The old request must still fail with its revoked session.
				setup := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, second, second.csrf, nil)
				if setup.status != http.StatusOK {
					t.Fatalf("fresh setup status = %d body = %v", setup.status, setup.body)
				}
				secret, _ := setup.body["secret"].(string)
				encoded, err := json.Marshal(map[string]string{"code": testTOTPCode(t, secret, time.Now().Unix())})
				if err != nil {
					t.Fatal(err)
				}
				gate.body = strings.NewReader(string(encoded))
			}
			release()
			select {
			case recorder := <-done:
				if recorder.Code != http.StatusForbidden {
					t.Fatalf("revoked %s status = %d body = %s", operation, recorder.Code, recorder.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("request did not finish after body was released")
			}
			enabled, pending, recoveryLeft, err := fixture.accounts.TOTPStatus(context.Background(), userID)
			if err != nil || enabled || recoveryLeft != 0 || pending != (operation == "confirm") {
				t.Fatalf("TOTP after rejected %s: enabled=%t pending=%t recovery=%d err=%v", operation, enabled, pending, recoveryLeft, err)
			}
			if operation == "setup" {
				setup := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, second, second.csrf, nil)
				if setup.status != http.StatusOK {
					t.Fatalf("surviving session setup status = %d body = %v", setup.status, setup.body)
				}
			}
		})
	}
}
