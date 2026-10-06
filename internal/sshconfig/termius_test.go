package sshconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huangzheng2016/termius_exporter/pkg/parser"
)

func TestPreviewTermiusRequiresConfirmation(t *testing.T) {
	_, err := PreviewTermius(TermiusOptions{DBPath: t.TempDir()}, nil, nil, DefaultLimits)
	if !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("err = %v, want ErrConfirmationRequired", err)
	}
}

func TestPreviewTermiusMissingDB(t *testing.T) {
	_, err := PreviewTermius(TermiusOptions{DBPath: filepath.Join(t.TempDir(), "absent"), Confirmed: true}, nil, nil, DefaultLimits)
	if err == nil {
		t.Fatal("expected error for missing termius DB")
	}
}

func TestBuildTermiusPreview(t *testing.T) {
	dir := t.TempDir()
	keyPath, fingerprint := writeTestKey(t, dir, "termius_key", false)
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	hosts := []parser.HostRecord{
		{Aliases: []string{"prod", "prod-alias"}, Host: "prod.example.com", Port: 0, Username: "root", Password: "s3cret-pass"},
		{Aliases: []string{"keyhost"}, Host: "keyhost.example.com", Port: 2222, Username: "deploy", KeyName: "termius_key"},
		{Aliases: []string{"agenthost"}, Host: "agenthost.example.com", Port: 22, Username: "root"},
		{Aliases: []string{"conflicted"}, Host: "conflicted.example.com", Port: 22, Username: "root"},
	}
	keys := []parser.KeyRecord{
		{Aliases: []string{"termius_key"}, PrivateKey: string(keyPEM)},
		{Aliases: []string{"termius_key_copy"}, PrivateKey: string(keyPEM)},
		{Aliases: []string{"broken"}, PrivateKey: "not-a-key"},
	}
	existing := []ExistingAsset{{Name: "conflicted", Host: "old.example.com", Port: 22, Username: "old"}}
	preview := buildTermiusPreview(hosts, keys, existing, nil, DefaultLimits)

	if len(preview.Hosts) != 4 {
		t.Fatalf("got %d hosts, want 4", len(preview.Hosts))
	}
	prod := preview.Hosts[0]
	if prod.Alias != "prod" || prod.Port != 22 || prod.AuthMethod != "password" {
		t.Errorf("unexpected prod host: %+v", prod)
	}
	if preview.Hosts[1].AuthMethod != "key" || preview.Hosts[1].KeyName != "termius_key" {
		t.Errorf("unexpected keyhost: %+v", preview.Hosts[1])
	}
	if preview.Hosts[2].AuthMethod != "agent" {
		t.Errorf("unexpected agenthost: %+v", preview.Hosts[2])
	}
	if preview.Hosts[3].Action != PlanConflictAlias {
		t.Errorf("conflicted action = %q, want conflict-alias", preview.Hosts[3].Action)
	}

	if len(preview.Keys) != 2 {
		t.Fatalf("got %d keys, want 2: %+v", len(preview.Keys), preview.Keys)
	}
	if preview.Keys[0].Fingerprint != fingerprint || preview.Keys[0].Action != PlanAdd {
		t.Errorf("unexpected first key: %+v", preview.Keys[0])
	}
	if preview.Keys[1].Action != PlanSkipDuplicate {
		t.Errorf("duplicate fingerprint not deduped: %+v", preview.Keys[1])
	}
	if !hasDiagnostic(preview.Diagnostics, "termius-key-unparsed") {
		t.Errorf("missing termius-key-unparsed diagnostic: %+v", preview.Diagnostics)
	}

	dump := fmt.Sprintf("%+v", *preview)
	if strings.Contains(dump, "s3cret-pass") {
		t.Error("password leaked into preview")
	}
	if strings.Contains(dump, "PRIVATE KEY") {
		t.Error("private key material leaked into preview")
	}
}

func TestBuildTermiusPreviewExistingKey(t *testing.T) {
	dir := t.TempDir()
	keyPath, fingerprint := writeTestKey(t, dir, "termius_key", false)
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	keys := []parser.KeyRecord{{Aliases: []string{"termius_key"}, PrivateKey: string(keyPEM)}}

	preview := buildTermiusPreview(nil, keys, nil, []ExistingKey{{Name: "stored", Fingerprint: fingerprint}}, DefaultLimits)
	if len(preview.Keys) != 1 || preview.Keys[0].Action != PlanSkipDuplicate {
		t.Errorf("existing fingerprint not deduped: %+v", preview.Keys)
	}

	preview = buildTermiusPreview(nil, keys, nil, []ExistingKey{{Name: "termius_key", Fingerprint: "SHA256:other"}}, DefaultLimits)
	if len(preview.Keys) != 1 || preview.Keys[0].Action != PlanConflictAlias {
		t.Errorf("name conflict not planned: %+v", preview.Keys)
	}
}

func TestBuildTermiusPreviewLimits(t *testing.T) {
	hosts := []parser.HostRecord{
		{Aliases: []string{"h1"}, Host: "h1.example.com", Port: 22, Username: "root"},
		{Aliases: []string{"h2"}, Host: "h2.example.com", Port: 22, Username: "root"},
	}
	preview := buildTermiusPreview(hosts, nil, nil, nil, Limits{MaxHosts: 1})
	if len(preview.Hosts) != 1 || !preview.Truncated {
		t.Errorf("got %d hosts truncated=%v, want 1 true", len(preview.Hosts), preview.Truncated)
	}
	if !hasDiagnostic(preview.Diagnostics, "limit-truncated") {
		t.Errorf("missing limit-truncated diagnostic: %+v", preview.Diagnostics)
	}
}

func TestBuildTermiusPreviewSanitizes(t *testing.T) {
	hosts := []parser.HostRecord{
		{Aliases: []string{"bad\x1b[31mname"}, Host: "host.example.com", Port: 22, Username: "root"},
	}
	preview := buildTermiusPreview(hosts, nil, nil, nil, DefaultLimits)
	if len(preview.Hosts) != 1 || preview.Hosts[0].Alias != "bad[31mname" {
		t.Errorf("alias not sanitized: %+v", preview.Hosts)
	}
}
