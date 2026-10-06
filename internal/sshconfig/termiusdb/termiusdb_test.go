package termiusdb

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"math"
	"strings"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/ssh"
)

func encodeTestRecordKey(dbID, storeID, recordID int) []byte {
	first := byte(0)
	dbBytes := encodeMinLE(int64(dbID))
	storeBytes := encodeMinLE(int64(storeID))
	indexBytes := encodeMinLE(1)
	first = byte(len(dbBytes)-1)<<5 | byte(len(storeBytes)-1)<<2 | byte(len(indexBytes)-1)
	key := []byte{first}
	key = append(key, dbBytes...)
	key = append(key, storeBytes...)
	key = append(key, indexBytes...)
	key = append(key, 3)
	var bits [8]byte
	binary.LittleEndian.PutUint64(bits[:], math.Float64bits(float64(recordID)))
	return append(key, bits[:]...)
}

func encodeMinLE(value int64) []byte {
	var out []byte
	for {
		out = append(out, byte(value))
		value >>= 8
		if value == 0 {
			break
		}
	}
	return out
}

func TestDecodeRecordID(t *testing.T) {
	if got := decodeRecordID(encodeTestRecordKey(1, 1, 5)); got != 5 {
		t.Errorf("decodeRecordID = %d, want 5", got)
	}
	if got := decodeRecordID(encodeTestRecordKey(300, 7, 9)); got != 9 {
		t.Errorf("decodeRecordID multi-byte ids = %d, want 9", got)
	}
	stringKey := []byte{0x00, 0x01, 0x01, 0x01, 0x01, 0x03, 'f', 'o', 'o'}
	if got := decodeRecordID(stringKey); got != 0 {
		t.Errorf("decodeRecordID string key = %d, want 0", got)
	}
	if got := decodeRecordID([]byte{0x00, 0x01}); got != 0 {
		t.Errorf("decodeRecordID short key = %d, want 0", got)
	}
}

func TestExportWrongKey(t *testing.T) {
	dir := buildDB(t, testKey(t), map[int]string{
		1: `{"host": "h.example.com", "user_name": "root", "port": 22, "title": "h"}`,
	})
	wrong := make([]byte, 32)
	if _, err := rand.Read(wrong); err != nil {
		t.Fatalf("key: %v", err)
	}
	if _, _, err := Export(dir, func() ([]byte, error) { return wrong, nil }); err == nil ||
		!strings.Contains(err.Error(), "no blobs could be decrypted") {
		t.Fatalf("err = %v, want decryption failure", err)
	}
}

func TestExportPreservesRecordIDs(t *testing.T) {
	key := testKey(t)
	pemA, _ := testKeyPEM(t)
	pemB, _ := testKeyPEM(t)
	dir := buildDB(t, key, map[int]string{
		3:  `{"private_key": ` + jsonString(t, pemA) + `, "label": "key-a"}`,
		10: `{"private_key": ` + jsonString(t, pemB) + `, "label": "key-b"}`,
		1:  `{"host": "c1.example.com", "user_name": "root", "port": 22, "title": "c1", "key_id": 10}`,
		2:  `{"host": "c2.example.com", "user_name": "root", "port": 22, "title": "c2", "key_id": 3}`,
	})
	hosts, keys, err := Export(dir, func() ([]byte, error) { return key, nil })
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(keys) != 2 || keys[0].ID != 3 || keys[1].ID != 10 {
		t.Errorf("record IDs not preserved: %+v", keys)
	}
	names := map[string]string{}
	for _, host := range hosts {
		names[host.Aliases[0]] = host.KeyName
	}
	if names["c1"] != "key-b" || names["c2"] != "key-a" {
		t.Errorf("key_id references misassigned: %v", names)
	}
}

func TestExportResolvesKeyReferenceToRecord(t *testing.T) {
	key := testKey(t)
	pemA, _ := testKeyPEM(t)
	pemB, _ := testKeyPEM(t)
	dir := buildDB(t, key, map[int]string{
		3:  `{"private_key": ` + jsonString(t, pemA) + `, "label": "shared"}`,
		10: `{"private_key": ` + jsonString(t, pemB) + `, "label": "shared"}`,
		1:  `{"host": "one.example.com", "user_name": "root", "port": 22, "title": "h1", "key_id": 10}`,
		2:  `{"host": "two.example.com", "user_name": "root", "port": 22, "title": "h2", "key_id": 3}`,
	})
	hosts, _, err := Export(dir, func() ([]byte, error) { return key, nil })
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	byTitle := map[string]string{}
	for _, host := range hosts {
		byTitle[host.Aliases[0]] = host.KeyPK
	}
	if byTitle["h1"] != pemB || byTitle["h2"] != pemA {
		t.Errorf("key references resolved to wrong records: %v", byTitle)
	}

	dir = buildDB(t, key, map[int]string{
		1: `{"private_key": ` + jsonString(t, pemA) + `, "label": "shared", "id": "k1"}`,
		2: `{"private_key": ` + jsonString(t, pemB) + `, "label": "shared", "id": "k2"}`,
		3: `{"host": "one.example.com", "user_name": "root", "port": 22, "title": "h1", "key_id": "k2"}`,
	})
	hosts, _, err = Export(dir, func() ([]byte, error) { return key, nil })
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(hosts) != 1 || hosts[0].KeyPK != pemB {
		t.Errorf("string key_id resolved to wrong record: %+v", hosts)
	}
}

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("key: %v", err)
	}
	return key
}

func testKeyPEM(t *testing.T) (string, string) {
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

func buildDB(t *testing.T, key []byte, records map[int]string) string {
	t.Helper()
	dir := t.TempDir()
	db, err := leveldb.OpenFile(dir, &opt.Options{Comparer: idbComparer{}})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for id, payload := range records {
		blob := encryptBlob(t, key, payload)
		if err := db.Put(encodeTestRecordKey(1, 1, id), []byte(blob), nil); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	return dir
}

func encryptBlob(t *testing.T, key []byte, plaintext string) string {
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

func jsonString(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}
