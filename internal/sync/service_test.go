package sync

import (
	"context"
	"errors"
	"testing"
)

func TestRecordProbeLeavesLegacyPlaintextUntouched(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	plaintext := `{"url":"https://sync.example.com","username":"u","password":"p"}`
	if err := instance.db.SettingSet(ctx, settingLink, plaintext); err != nil {
		t.Fatal(err)
	}

	instance.service.recordProbe(ctx, errors.New("probe exploded"))

	stored, found, err := instance.db.SettingGet(ctx, settingLink)
	if err != nil || !found || stored != plaintext {
		t.Fatalf("legacy link changed after probe: %q %v %v", stored, found, err)
	}
}
