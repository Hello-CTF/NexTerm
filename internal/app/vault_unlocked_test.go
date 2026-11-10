package app_test

import (
	"testing"

	app "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

func TestVaultUnlockedReadsVaultStatus(t *testing.T) {
	var nilStatus *vault.Status
	cases := []struct {
		name   string
		status any
		want   bool
	}{
		{"unlocked value", vault.Status{Unlocked: true}, true},
		{"unlocked pointer", &vault.Status{Unlocked: true}, true},
		{"locked value", vault.Status{}, false},
		{"nil pointer", nilStatus, false},
		{"missing status", nil, false},
		{"unrelated value", struct{ Unlocked bool }{true}, false},
	}
	for _, tc := range cases {
		if got := app.VaultUnlocked(tc.status); got != tc.want {
			t.Errorf("%s: VaultUnlocked = %v, want %v", tc.name, got, tc.want)
		}
	}
}
