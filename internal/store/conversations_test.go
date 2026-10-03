package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/migrations"
)

// Message rows must read back in true insertion order even when many inserts
// land in the same millisecond: created_at has only millisecond resolution
// and the ULID tail is random, so ordering by (created_at, id) let
// same-millisecond rows shuffle (~50% inversion under hot repetition).
func TestMsgListPreservesInsertionOrder(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	conversation, err := db.ConvCreate(ctx, "order", nil)
	if err != nil {
		t.Fatal(err)
	}
	const total = 40
	for i := 0; i < total; i++ {
		content := map[string]any{"role": "user", "content": fmt.Sprintf("m%02d", i)}
		if err := db.MsgInsert(ctx, conversation.ID, "user", content, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.MsgList(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != total {
		t.Fatalf("rows=%d want %d", len(rows), total)
	}
	for i, row := range rows {
		var persisted struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(row.ContentJSON), &persisted); err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("m%02d", i); persisted.Content != want {
			t.Fatalf("row %d content=%q want %q — insertion order lost", i, persisted.Content, want)
		}
	}
}

// Concurrent inserters serialize on the immediate transaction lock: every
// row gets a unique seq, and each writer's own rows keep their FIFO order.
func TestMsgInsertConcurrentAssignsUniqueSeq(t *testing.T) {
	ctx := context.Background()
	db, _ := fileStore(t)
	conversation, err := db.ConvCreate(ctx, "concurrent", nil)
	if err != nil {
		t.Fatal(err)
	}
	const writers, perWriter = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				content := map[string]any{"role": "user", "content": fmt.Sprintf("w%d-%02d", w, i)}
				if err := db.MsgInsert(ctx, conversation.ID, "user", content, nil, nil); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	rows, err := db.MsgList(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != writers*perWriter {
		t.Fatalf("rows=%d want %d", len(rows), writers*perWriter)
	}
	position := map[string]int{}
	for i, row := range rows {
		var persisted struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(row.ContentJSON), &persisted); err != nil {
			t.Fatal(err)
		}
		position[persisted.Content] = i
	}
	for w := 0; w < writers; w++ {
		for i := 1; i < perWriter; i++ {
			previous := fmt.Sprintf("w%d-%02d", w, i-1)
			current := fmt.Sprintf("w%d-%02d", w, i)
			if position[previous] > position[current] {
				t.Fatalf("writer %d rows reordered: %s at %d after %s at %d", w, previous, position[previous], current, position[current])
			}
		}
	}
	var seqs []int64
	seqRows, err := db.DB().Query("SELECT seq FROM ai_message WHERE conversation_id = ? ORDER BY seq", conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer seqRows.Close()
	for seqRows.Next() {
		var seq int64
		if err := seqRows.Scan(&seq); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	if err := seqRows.Err(); err != nil {
		t.Fatal(err)
	}
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("seq[%d]=%d — seq must be contiguous from 1, got %v", i, seq, seqs)
		}
	}
}

// A database created before the seq migration keeps its exact read order:
// legacy rows are backfilled in the old (created_at, id) order, and new
// inserts continue the sequence after them.
func TestMigrationBackfillsMessageSeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (
    version BIGINT PRIMARY KEY,
    description TEXT NOT NULL,
    installed_on TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    checksum BLOB NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	applyLegacyMigrations(t, raw)
	if _, err := raw.Exec(`INSERT INTO ai_conversation(id, title, scope_json, created_at, updated_at)
VALUES('conv', 'legacy', '{}', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	// Same created_at, ids deliberately out of insertion order: the old read
	// order was (created_at, id), so the backfill must rank A < B < C.
	for _, id := range []string{"C", "A", "B"} {
		if _, err := raw.Exec(`INSERT INTO ai_message(id, conversation_id, role, content_json, tokens_in, tokens_out, created_at)
VALUES(?, 'conv', 'user', '{"role":"user","content":"`+id+`"}', NULL, NULL, 1000)`, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.MsgList(context.Background(), "conv")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].ID != "A" || rows[1].ID != "B" || rows[2].ID != "C" {
		t.Fatalf("backfilled order wrong: %+v", rows)
	}
	if err := db.MsgInsert(context.Background(), "conv", "user", map[string]any{"role": "user", "content": "new"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	rows, err = db.MsgList(context.Background(), "conv")
	if err != nil {
		t.Fatal(err)
	}
	var last struct {
		Content string `json:"content"`
	}
	if len(rows) != 4 || json.Unmarshal([]byte(rows[3].ContentJSON), &last) != nil || last.Content != "new" {
		t.Fatalf("post-migration insert must land last: %+v", rows)
	}
}

// applyLegacyMigrations applies every migration except 0006 (the seq
// migration) and registers each in schema_migrations, producing a database
// exactly as a pre-seq build left it.
func applyLegacyMigrations(t *testing.T, raw *sql.DB) {
	t.Helper()
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") && !strings.HasPrefix(entry.Name(), "0006_") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		contents, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(string(contents)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		rest := strings.SplitN(strings.TrimSuffix(name, ".sql"), "_", 2)[1]
		version, err := strconv.ParseInt(strings.SplitN(name, "_", 2)[0], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		checksum := sha256.Sum256(contents)
		if _, err := raw.Exec(`INSERT INTO schema_migrations(version, description, checksum) VALUES(?, ?, ?)`,
			version, strings.ReplaceAll(rest, "_", " "), checksum[:]); err != nil {
			t.Fatal(err)
		}
	}
}
