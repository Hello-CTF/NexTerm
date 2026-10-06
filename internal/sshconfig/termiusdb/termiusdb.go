// Package termiusdb reads the local Termius IndexedDB/LevelDB database and
// parses host and key records. It replaces the eTerm reference dependency
// (termius_exporter v0.2.1), whose LevelDB reader discarded record IDs and
// whose key retrieval only supported macOS.
package termiusdb

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"golang.org/x/crypto/nacl/secretbox"
)

type KeySource func() ([]byte, error)

type HostRecord struct {
	Aliases  []string
	Host     string
	Port     int
	Username string
	Password string
	KeyName  string
	KeyID    int
}

type KeyRecord struct {
	Aliases    []string
	PrivateKey string
	ID         int
}

type blobEntry struct {
	blob string
	id   int
}

type decryptedEntry struct {
	text string
	id   int
}

var blobRE = regexp.MustCompile(`BA[A-Za-z0-9+/=]{30,}`)

// Export reads, decrypts and parses the Termius database at dbPath ("" uses
// the platform default). The key is obtained from keySource before any
// database file is read.
func Export(dbPath string, keySource KeySource) ([]HostRecord, []KeyRecord, error) {
	if keySource == nil {
		return nil, nil, fmt.Errorf("termiusdb: nil key source")
	}
	key, err := keySource()
	if err != nil {
		return nil, nil, err
	}
	if len(key) != 32 {
		return nil, nil, fmt.Errorf("termiusdb: key length is %d, expected 32 bytes", len(key))
	}
	entries, err := readBlobEntries(dbPath)
	if err != nil {
		return nil, nil, err
	}
	var decrypted []decryptedEntry
	for _, entry := range entries {
		if text, ok := decryptBlob(entry.blob, key); ok {
			decrypted = append(decrypted, decryptedEntry{text: text, id: entry.id})
		}
	}
	if len(decrypted) == 0 {
		return nil, nil, fmt.Errorf("termiusdb: no blobs could be decrypted")
	}
	hosts, keys := parseEntries(decrypted)
	return hosts, keys, nil
}

func DefaultDBPath() string {
	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, `Termius\IndexedDB\file__0.indexeddb.leveldb`)
		}
		return `C:\Users\%APPDATA%\Termius\IndexedDB\file__0.indexeddb.leveldb`
	case "linux":
		return "~/.config/Termius/IndexedDB/file__0.indexeddb.leveldb"
	default:
		return "~/Library/Application Support/Termius/IndexedDB/file__0.indexeddb.leveldb/"
	}
}

func decryptBlob(base64Str string, key []byte) (string, bool) {
	data, err := base64.StdEncoding.DecodeString(base64Str)
	if err != nil {
		return "", false
	}
	if len(data) < 27 || data[0] != 4 {
		return "", false
	}
	nonce := new([24]byte)
	copy(nonce[:], data[2:26])
	plaintext, ok := secretbox.Open(nil, data[26:], nonce, (*[32]byte)(key))
	if !ok {
		return "", false
	}
	return string(plaintext), true
}

func readBlobEntries(dbPath string) ([]blobEntry, error) {
	if dbPath == "" {
		dbPath = DefaultDBPath()
	}
	if strings.HasPrefix(dbPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("termiusdb: get home dir: %w", err)
		}
		dbPath = filepath.Join(home, dbPath[2:])
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("termiusdb: DB path not found: %s", dbPath)
		}
		return nil, fmt.Errorf("termiusdb: stat DB path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("termiusdb: DB path is not a directory: %s", dbPath)
	}

	seen := map[string]bool{}
	var result []blobEntry
	if entries, err := iterateLevelDB(dbPath); err == nil {
		for _, entry := range entries {
			if seen[entry.blob] {
				continue
			}
			seen[entry.blob] = true
			result = append(result, entry)
		}
	}

	dirEntries, err := os.ReadDir(dbPath)
	if err != nil {
		return nil, fmt.Errorf("termiusdb: read DB dir: %w", err)
	}
	var sb strings.Builder
	for _, entry := range dirEntries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".log" && ext != ".ldb" && ext != ".sst" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dbPath, entry.Name()))
		if err != nil {
			continue
		}
		sb.Write(data)
	}
	for _, blob := range blobRE.FindAllString(sb.String(), -1) {
		if seen[blob] {
			continue
		}
		seen[blob] = true
		result = append(result, blobEntry{blob: blob})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("termiusdb: no encrypted blobs found in %s", dbPath)
	}
	return result, nil
}

func iterateLevelDB(dbPath string) ([]blobEntry, error) {
	tmp, err := os.MkdirTemp("", "termius-leveldb-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	dirEntries, err := os.ReadDir(dbPath)
	if err != nil {
		return nil, err
	}
	for _, entry := range dirEntries {
		if entry.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(tmp, entry.Name()), filepath.Join(dbPath, entry.Name())); err != nil {
			return nil, err
		}
	}

	ldb, err := leveldb.OpenFile(tmp, &opt.Options{ReadOnly: true, Comparer: idbComparer{}})
	if err != nil {
		return nil, err
	}
	defer ldb.Close()

	var result []blobEntry
	iter := ldb.NewIterator(nil, nil)
	defer iter.Release()
	for iter.Next() {
		id := decodeRecordID(iter.Key())
		for _, blob := range blobRE.FindAllString(string(iter.Value()), -1) {
			result = append(result, blobEntry{blob: blob, id: id})
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

type idbComparer struct{}

func (idbComparer) Compare(a, b []byte) int { return bytes.Compare(a, b) }
func (idbComparer) Name() string            { return "idb_cmp1" }
func (idbComparer) Separator(dst, a, _ []byte) []byte {
	return append(dst, a...)
}
func (idbComparer) Successor(dst, b []byte) []byte {
	return append(dst, b...)
}

// decodeRecordID extracts the auto-increment record ID from a Chromium
// IndexedDB object-store key: [size descriptor][db id][store id][index id]
// [user key], where the user key of a numeric record is 0x03 followed by an
// 8-byte host-endian double. IDs that are absent or non-numeric yield 0.
func decodeRecordID(key []byte) int {
	if len(key) < 5 {
		return 0
	}
	dbSize := int(key[0]>>5) + 1
	storeSize := int(key[0]>>2&0x7) + 1
	indexSize := int(key[0]&0x3) + 1
	pos := 1 + dbSize + storeSize + indexSize
	if len(key) < pos+9 || key[pos] != 3 {
		return 0
	}
	value := math.Float64frombits(binary.LittleEndian.Uint64(key[pos+1 : pos+9]))
	if value <= 0 || value != math.Trunc(value) || value > math.MaxInt32 {
		return 0
	}
	return int(value)
}

func parseEntries(entries []decryptedEntry) ([]HostRecord, []KeyRecord) {
	var identities []map[string]interface{}
	var connections []map[string]interface{}
	keyLabelByID := map[int]string{}
	keyLabelByStringID := map[string]string{}
	keyAliases := map[string][]string{}
	keyIDByPK := map[string]int{}
	var keyOrder []string

	for _, entry := range entries {
		text := entry.text
		if !strings.HasPrefix(text, "{") {
			continue
		}
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(text), &obj); err != nil {
			continue
		}
		if _, ok := obj["username"]; ok {
			if _, ok2 := obj["password"]; ok2 {
				identities = append(identities, obj)
			}
		}
		if host, ok := obj["host"].(string); ok && host != "" {
			if user, ok2 := obj["user_name"].(string); ok2 && user != "" {
				connections = append(connections, obj)
			}
		}
		if pk, ok := obj["private_key"].(string); ok && pk != "" {
			label, _ := obj["label"].(string)
			if label == "" {
				continue
			}
			if _, exists := keyAliases[pk]; !exists {
				keyOrder = append(keyOrder, pk)
			}
			if id, _ := obj["id"].(string); id != "" {
				keyLabelByStringID[id] = label
			}
			if entry.id != 0 {
				keyLabelByID[entry.id] = label
				keyIDByPK[pk] = entry.id
			}
			keyAliases[pk] = appendUnique(keyAliases[pk], label)
		}
	}

	type groupKey struct {
		host, user string
		port       int
	}
	grouped := map[groupKey]*HostRecord{}
	var order []groupKey

	for _, conn := range connections {
		host, _ := conn["host"].(string)
		if host == "" {
			continue
		}
		user, _ := conn["user_name"].(string)
		portF, _ := conn["port"].(float64)
		port := int(portF)
		if port == 0 {
			port = 22
		}
		title, _ := conn["title"].(string)
		if title == "" {
			title, _ = conn["label"].(string)
		}
		if title == "" {
			title = fmt.Sprintf("%s@%s:%d", user, host, port)
		}

		k := groupKey{host, user, port}
		if rec, exists := grouped[k]; exists {
			rec.Aliases = appendUnique(rec.Aliases, title)
			continue
		}

		password := ""
		for _, id := range identities {
			if id["username"] == user {
				if pw, ok := id["password"].(string); ok && pw != "" {
					password = pw
					break
				}
			}
		}

		keyID := 0
		if kidF, ok := conn["key_id"].(float64); ok && kidF > 0 {
			keyID = int(kidF)
		}
		keyName := keyLabelByID[keyID]
		if keyName == "" {
			if kidS, ok := conn["key_id"].(string); ok && kidS != "" {
				keyName = keyLabelByStringID[kidS]
			}
		}

		grouped[k] = &HostRecord{
			Aliases:  []string{title},
			Host:     host,
			Port:     port,
			Username: user,
			Password: password,
			KeyName:  keyName,
			KeyID:    keyID,
		}
		order = append(order, k)
	}

	hosts := make([]HostRecord, 0, len(order))
	for _, k := range order {
		hosts = append(hosts, *grouped[k])
	}

	keys := make([]KeyRecord, 0, len(keyAliases))
	for _, pk := range keyOrder {
		keys = append(keys, KeyRecord{
			Aliases:    keyAliases[pk],
			PrivateKey: pk,
			ID:         keyIDByPK[pk],
		})
	}
	return hosts, keys
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
