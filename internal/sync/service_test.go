package sync

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRecordProbeWarnsWhenSaveFails(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	plaintext := `{"url":"https://sync.example.com","username":"u","password":"p"}`
	if err := instance.db.SettingSet(ctx, settingLink, plaintext); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	instance.service.engine.logger = slog.New(slog.NewTextHandler(&logs, nil))

	instance.service.recordProbe(ctx, errors.New("probe exploded"))

	logged := logs.String()
	if !strings.Contains(logged, "level=WARN") || !strings.Contains(logged, "sync probe result not recorded") {
		t.Fatalf("recordProbe log = %q, want warn about unrecorded probe result", logged)
	}
	stored, found, err := instance.db.SettingGet(ctx, settingLink)
	if err != nil || !found || stored != plaintext {
		t.Fatalf("link setting changed after swallowed save error: %q %v %v", stored, found, err)
	}
}
