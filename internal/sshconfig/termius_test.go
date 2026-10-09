package sshconfig

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/sshconfig/termiusdb"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/ssh"
)

type fixtureComparer struct{}

func (fixtureComparer) Compare(a, b []byte) int { return bytes.Compare(a, b) }
func (fixtureComparer) Name() string            { return "idb_cmp1" }
func (fixtureComparer) Separator(dst, a, _ []byte) []byte {
	return append(dst, a...)
}
func (fixtureComparer) Successor(dst, b []byte) []byte {
	return append(dst, b...)
}

func idbRecordKey(recordID int) []byte {
	key := []byte{0x00, 0x01, 0x01, 0x01, 0x03}
	var bits [8]byte
	binary.LittleEndian.PutUint64(bits[:], math.Float64bits(float64(recordID)))
	return append(key, bits[:]...)
}

func encryptFixtureBlob(t *testing.T, key []byte, plaintext string) string {
	t.Helper()
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatalf("nonce: %v", err)
	}
	box := secretbox.Seal(nil, []byte(plaintext), &nonce, (*[32]byte)(key))
	raw := append([]byte{4, 0}, nonce[:]...)
	raw = append(raw, box...)
	return base64.StdEncoding.EncodeToString(raw)
}

func buildTermiusFixture(t *testing.T, key []byte, records map[int]string) string {
	t.Helper()
	dir := t.TempDir()
	db, err := leveldb.OpenFile(dir, &opt.Options{Comparer: fixtureComparer{}})
	if err != nil {
		t.Fatalf("open fixture leveldb: %v", err)
	}
	for id, payload := range records {
		if err := db.Put(idbRecordKey(id), []byte(encryptFixtureBlob(t, key, payload)), nil); err != nil {
			t.Fatalf("put fixture record: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture leveldb: %v", err)
	}
	return dir
}

func fixtureKeyPEM(t *testing.T) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), ssh.FingerprintSHA256(sshPub)
}

func fixtureKeySource(key []byte) termiusdb.KeySource {
	return func() ([]byte, error) { return key, nil }
}

func TestPreviewTermiusRequiresConfirmation(t *testing.T) {
	_, err := PreviewTermius(TermiusOptions{DBPath: t.TempDir()}, nil, nil, DefaultLimits)
	if !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("err = %v, want ErrConfirmationRequired", err)
	}
}

func TestPreviewTermiusPlatformGuard(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("platform guard only applies off macOS")
	}
	_, err := PreviewTermius(TermiusOptions{DBPath: t.TempDir(), Confirmed: true}, nil, nil, DefaultLimits)
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v, want unsupported platform guard", err)
	}
}

func TestPreviewTermiusMissingDB(t *testing.T) {
	key := make([]byte, 32)
	_, err := PreviewTermius(TermiusOptions{
		DBPath:    filepath.Join(t.TempDir(), "absent"),
		Confirmed: true,
		KeySource: fixtureKeySource(key),
	}, nil, nil, DefaultLimits)
	if err == nil {
		t.Fatal("expected error for missing termius DB")
	}
}

func TestPreviewTermiusEndToEnd(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("key: %v", err)
	}
	keyPEM, fingerprint := fixtureKeyPEM(t)
	records := map[int]string{
		1: `{"private_key": ` + mustJSON(t, keyPEM) + `, "label": "key-one"}`,
		2: `{"host": "prod.example.com", "user_name": "root", "port": 22, "title": "prod", "key_id": 1}`,
		3: `{"username": "root", "password": "s3cret"}`,
	}
	dir := buildTermiusFixture(t, key, records)
	preview, err := PreviewTermius(TermiusOptions{DBPath: dir, Confirmed: true, KeySource: fixtureKeySource(key)}, nil, nil, DefaultLimits)
	if err != nil {
		t.Fatalf("PreviewTermius: %v", err)
	}
	if len(preview.Hosts) != 1 || len(preview.Keys) != 1 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	host := preview.Hosts[0]
	if host.Alias != "prod" || host.Hostname != "prod.example.com" || host.Port != 22 || host.Username != "root" {
		t.Errorf("unexpected host: %+v", host)
	}
	if host.KeyName != "key-one" || host.AuthMethod != "key" {
		t.Errorf("key reference not resolved: %+v", host)
	}
	if len(host.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", host.Warnings)
	}
	if preview.Keys[0].Fingerprint != fingerprint || preview.Keys[0].Action != PlanAdd {
		t.Errorf("unexpected key: %+v", preview.Keys[0])
	}
	dump := fmt.Sprintf("%+v", *preview)
	if strings.Contains(dump, "s3cret") || strings.Contains(dump, "PRIVATE KEY") {
		t.Error("secret leaked into preview")
	}
}

func TestPreviewTermiusMultiKeyIDs(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("key: %v", err)
	}
	pemRare, fpRare := fixtureKeyPEM(t)
	pemPopular, fpPopular := fixtureKeyPEM(t)
	records := map[int]string{
		3:  `{"private_key": ` + mustJSON(t, pemRare) + `, "label": "key-rare"}`,
		10: `{"private_key": ` + mustJSON(t, pemPopular) + `, "label": "key-popular"}`,
		1:  `{"host": "c1.example.com", "user_name": "root", "port": 22, "title": "c1", "key_id": 3}`,
		2:  `{"host": "c2.example.com", "user_name": "root", "port": 22, "title": "c2", "key_id": 10}`,
		4:  `{"host": "c3.example.com", "user_name": "root", "port": 22, "title": "c3", "key_id": 10}`,
	}
	dir := buildTermiusFixture(t, key, records)
	preview, err := PreviewTermius(TermiusOptions{DBPath: dir, Confirmed: true, KeySource: fixtureKeySource(key)}, nil, nil, DefaultLimits)
	if err != nil {
		t.Fatalf("PreviewTermius: %v", err)
	}
	if len(preview.Keys) != 2 {
		t.Fatalf("got %d keys, want 2: %+v", len(preview.Keys), preview.Keys)
	}
	fps := map[string]string{}
	for _, k := range preview.Keys {
		fps[k.Aliases[0]] = k.Fingerprint
	}
	if fps["key-rare"] != fpRare || fps["key-popular"] != fpPopular {
		t.Errorf("key labels misassigned: %v", fps)
	}
	keyNames := map[string]string{}
	for _, host := range preview.Hosts {
		keyNames[host.Alias] = host.KeyName
	}
	if keyNames["c1"] != "key-rare" || keyNames["c2"] != "key-popular" || keyNames["c3"] != "key-popular" {
		t.Errorf("host key references misassigned: %v", keyNames)
	}
}

func TestBuildTermiusPreviewBatchAliasConflict(t *testing.T) {
	hosts := []termiusdb.HostRecord{
		{Aliases: []string{"dup"}, Host: "a.example.com", Port: 22, Username: "root"},
		{Aliases: []string{"dup"}, Host: "b.example.com", Port: 22, Username: "root"},
	}
	preview := buildTermiusPreview(hosts, nil, nil, nil, DefaultLimits)
	if len(preview.Hosts) != 2 {
		t.Fatalf("got %d hosts, want 2", len(preview.Hosts))
	}
	if preview.Hosts[0].Action != PlanAdd {
		t.Errorf("first dup action = %q, want add", preview.Hosts[0].Action)
	}
	if preview.Hosts[1].Action != PlanConflictAlias {
		t.Errorf("second dup action = %q, want conflict-alias: %+v", preview.Hosts[1].Action, preview.Hosts[1].Warnings)
	}
}

func TestBuildTermiusPreviewBatchPairConflict(t *testing.T) {
	hosts := []termiusdb.HostRecord{
		{Aliases: []string{"a"}, Host: "e1.example.com", Port: 22, Username: "root"},
		{Aliases: []string{"b"}, Host: "e2.example.com", Port: 22, Username: "root"},
		{Aliases: []string{"a"}, Host: "e2.example.com", Port: 22, Username: "root"},
		{Aliases: []string{"c"}, Host: "e3.example.com", Port: 22, Username: "root"},
		{Aliases: []string{"c"}, Host: "e3.example.com", Port: 22, Username: "root"},
	}
	preview := buildTermiusPreview(hosts, nil, nil, nil, DefaultLimits)
	if len(preview.Hosts) != 5 {
		t.Fatalf("got %d hosts, want 5", len(preview.Hosts))
	}
	if preview.Hosts[2].Action != PlanConflictAlias {
		t.Errorf("alias a with new endpoint action = %q, want conflict-alias (not skip-duplicate): %v",
			preview.Hosts[2].Action, preview.Hosts[2].Warnings)
	}
	if preview.Hosts[4].Action != PlanSkipDuplicate {
		t.Errorf("identical alias+endpoint action = %q, want skip-duplicate", preview.Hosts[4].Action)
	}
}

func TestBuildTermiusPreviewKeyReconciliation(t *testing.T) {
	keyPEM, fingerprint := fixtureKeyPEM(t)
	keys := []termiusdb.KeyRecord{
		{Aliases: []string{"orig"}, PrivateKey: keyPEM},
		{Aliases: []string{"copy"}, PrivateKey: keyPEM},
	}
	hosts := []termiusdb.HostRecord{
		{Aliases: []string{"h1"}, Host: "h1.example.com", Port: 22, Username: "root", KeyName: "copy"},
		{Aliases: []string{"h2"}, Host: "h2.example.com", Port: 22, Username: "root", KeyName: "ghost"},
		{Aliases: []string{"h3"}, Host: "h3.example.com", Port: 22, Username: "root", KeyID: 42},
	}
	preview := buildTermiusPreview(hosts, keys, nil, nil, DefaultLimits)
	if len(preview.Hosts) != 3 {
		t.Fatalf("got %d hosts, want 3", len(preview.Hosts))
	}
	if preview.Hosts[0].KeyName != "orig" {
		t.Errorf("h1 key not canonicalized: %q", preview.Hosts[0].KeyName)
	}
	if !hasWarning(preview.Hosts[1].Warnings, "not part of the import") {
		t.Errorf("h2 dangling key reference not marked: %v", preview.Hosts[1].Warnings)
	}
	if preview.Hosts[2].AuthMethod != "key" || !hasWarning(preview.Hosts[2].Warnings, "could not be resolved") {
		t.Errorf("h3 unresolved key id not marked: %+v", preview.Hosts[2])
	}

	existing := []ExistingKey{{Name: "stored", Fingerprint: fingerprint}}
	preview = buildTermiusPreview(hosts[:1], keys, nil, existing, DefaultLimits)
	if preview.Hosts[0].KeyName != "stored" {
		t.Errorf("h1 key not mapped to existing key: %q", preview.Hosts[0].KeyName)
	}
	if !hasWarning(preview.Hosts[0].Warnings, "already exists") {
		t.Errorf("h1 missing existing-key warning: %v", preview.Hosts[0].Warnings)
	}
}

func TestBuildTermiusPreviewKeyFingerprintPreserved(t *testing.T) {
	pemA, fpA := fixtureKeyPEM(t)
	pemB, fpB := fixtureKeyPEM(t)
	keys := []termiusdb.KeyRecord{
		{Aliases: []string{"shared"}, PrivateKey: pemA, ID: 1},
		{Aliases: []string{"shared"}, PrivateKey: pemB, ID: 2},
	}
	hosts := []termiusdb.HostRecord{
		{Aliases: []string{"h1"}, Host: "h1.example.com", Port: 22, Username: "root", KeyName: "shared", KeyID: 1, KeyPK: pemA},
		{Aliases: []string{"h2"}, Host: "h2.example.com", Port: 22, Username: "root", KeyName: "shared", KeyID: 2, KeyPK: pemB},
	}
	preview := buildTermiusPreview(hosts, keys, nil, nil, DefaultLimits)
	if len(preview.Hosts) != 2 || len(preview.Keys) != 2 {
		t.Fatalf("preview = %+v", preview)
	}
	if preview.Hosts[0].KeyFingerprint != fpA {
		t.Errorf("h1 KeyFingerprint = %q, want %q", preview.Hosts[0].KeyFingerprint, fpA)
	}
	if preview.Hosts[1].KeyFingerprint != fpB {
		t.Errorf("h2 KeyFingerprint = %q, want %q (second same-name key must keep its own fingerprint)", preview.Hosts[1].KeyFingerprint, fpB)
	}
	unreferenced := termiusdb.HostRecord{
		Aliases: []string{"h3"}, Host: "h3.example.com", Port: 22, Username: "root", KeyName: "shared",
	}
	preview = buildTermiusPreview([]termiusdb.HostRecord{unreferenced}, keys, nil, nil, DefaultLimits)
	if preview.Hosts[0].KeyFingerprint != "" {
		t.Errorf("host without key reference got KeyFingerprint %q", preview.Hosts[0].KeyFingerprint)
	}
}

func TestBuildTermiusPreviewExistingKey(t *testing.T) {
	keyPEM, fingerprint := fixtureKeyPEM(t)
	keys := []termiusdb.KeyRecord{{Aliases: []string{"termius_key"}, PrivateKey: keyPEM}}

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
	hosts := []termiusdb.HostRecord{
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
	hosts := []termiusdb.HostRecord{
		{Aliases: []string{"bad\x1b[31mname"}, Host: "host.example.com", Port: 22, Username: "root"},
	}
	preview := buildTermiusPreview(hosts, nil, nil, nil, DefaultLimits)
	if len(preview.Hosts) != 1 || preview.Hosts[0].Alias != "bad[31mname" {
		t.Errorf("alias not sanitized: %+v", preview.Hosts)
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(data)
}
