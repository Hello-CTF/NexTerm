package account

import (
	"context"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

func TestCheckSessionDoesNotSlideTheSession(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "checker", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	token, session, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	read := func() (touched, expires int64) {
		row, err := query.Use(a.db).UserSession.WithContext(ctx).Where(query.Use(a.db).UserSession.ID.Eq(session.ID)).First()
		if err != nil {
			t.Fatal(err)
		}
		return row.TouchedAt, row.ExpiresAt
	}
	touched, expires := read()
	// Past the slide threshold ValidateSession would write; CheckSession must not.
	*now += sessionSlidePersist.Milliseconds() + 1000
	identity, err := a.CheckSession(ctx, token)
	if err != nil || identity.SessionID != session.ID || identity.UserID != user.ID {
		t.Fatalf("CheckSession = %+v, %v", identity, err)
	}
	if gotTouched, gotExpires := read(); gotTouched != touched || gotExpires != expires {
		t.Fatalf("CheckSession wrote: touched %d->%d expires %d->%d", touched, gotTouched, expires, gotExpires)
	}
	if _, err := a.ValidateSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if gotTouched, _ := read(); gotTouched == touched {
		t.Fatal("ValidateSession should still slide the session")
	}
}

func TestCheckSessionMatchesValidateSessionRejections(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		mutate func(t *testing.T, a *Accounts, now *int64, userID string) (token string)
	}{
		{"unknown_token", func(t *testing.T, a *Accounts, now *int64, userID string) string { return "not-a-token" }},
		{"revoked", func(t *testing.T, a *Accounts, now *int64, userID string) string {
			token, session, err := a.IssueSession(ctx, userID, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := a.RevokeSession(ctx, session.ID); err != nil {
				t.Fatal(err)
			}
			return token
		}},
		{"disabled_user", func(t *testing.T, a *Accounts, now *int64, userID string) string {
			token, _, err := a.IssueSession(ctx, userID, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := a.SetUserDisabled(ctx, userID, true); err != nil {
				t.Fatal(err)
			}
			return token
		}},
		{"sliding_expired", func(t *testing.T, a *Accounts, now *int64, userID string) string {
			token, _, err := a.IssueSession(ctx, userID, "")
			if err != nil {
				t.Fatal(err)
			}
			*now += SessionSlidingTTL.Milliseconds() + 1
			return token
		}},
		{"device_revoked", func(t *testing.T, a *Accounts, now *int64, userID string) string {
			device, err := a.RegisterDevice(ctx, userID, "laptop", "desktop")
			if err != nil {
				t.Fatal(err)
			}
			token, _, err := a.IssueSession(ctx, userID, device.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.RevokeDevice(ctx, userID, device.ID); err != nil {
				t.Fatal(err)
			}
			return token
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, now := testAccounts(t)
			user, err := a.CreateUser(ctx, "subject", "", "synthetic-password-1")
			if err != nil {
				t.Fatal(err)
			}
			token := tc.mutate(t, a, now, user.ID)
			_, validateErr := a.ValidateSession(ctx, token)
			_, checkErr := a.CheckSession(ctx, token)
			requireCode(t, validateErr, ipc.CodeForbidden)
			requireCode(t, checkErr, ipc.CodeForbidden)
			if ipc.NormalizeError(validateErr).Message != ipc.NormalizeError(checkErr).Message {
				t.Fatalf("messages differ: %q vs %q", validateErr, checkErr)
			}
		})
	}
}

func TestCheckSessionConcurrentWithValidate(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "racer", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = a.CheckSession(ctx, token)
			} else {
				_, err = a.ValidateSession(ctx, token)
			}
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
}
