package tools

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/db"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type fakeFS struct {
	mu        sync.Mutex
	files     map[string][]byte
	dirs      map[string][]base.FileEntry
	readErr   error
	writeErr  error
	existsErr error
	listCalls []string
}

func newFakeFS() *fakeFS {
	return &fakeFS{files: make(map[string][]byte), dirs: make(map[string][]base.FileEntry)}
}

func (f *fakeFS) List(_ context.Context, name string) ([]base.FileEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls = append(f.listCalls, name)
	return append([]base.FileEntry(nil), f.dirs[name]...), nil
}

func (f *fakeFS) ReadFile(_ context.Context, name string, maximum int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	content, ok := f.files[name]
	if !ok {
		return nil, errors.New("not found")
	}
	if int64(len(content)) > maximum {
		return nil, errors.New("file too large")
	}
	return append([]byte(nil), content...), nil
}

func (f *fakeFS) WriteFile(_ context.Context, name string, content []byte, backup bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	if previous, ok := f.files[name]; ok && backup {
		f.files[name+".nexterm-bak"] = append([]byte(nil), previous...)
	}
	f.files[name] = append([]byte(nil), content...)
	return nil
}

func (f *fakeFS) Mkdir(context.Context, string) error                      { return nil }
func (f *fakeFS) Rename(context.Context, string, string) error             { return nil }
func (f *fakeFS) Delete(context.Context, string, bool) error               { return nil }
func (f *fakeFS) Chmod(context.Context, string, fs.FileMode) error         { return nil }
func (f *fakeFS) Checksum(context.Context, string, string) (string, error) { return "", nil }
func (f *fakeFS) Exists(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.existsErr != nil {
		return false, f.existsErr
	}
	_, file := f.files[name]
	_, dir := f.dirs[name]
	return file || dir, nil
}
func (f *fakeFS) Size(_ context.Context, name string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.files[name])), nil
}
func (f *fakeFS) OpenRead(context.Context, string) (base.RemoteReader, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeFS) OpenWrite(context.Context, string, bool) (base.RemoteWriter, error) {
	return nil, errors.New("not implemented")
}

type fakeTransport struct {
	files     *fakeFS
	mu        sync.Mutex
	execCalls []string
	result    base.ExecResult
	execErr   error
	block     bool
}

func (f *fakeTransport) Kind() string       { return "fake" }
func (f *fakeTransport) Generation() uint64 { return 1 }
func (f *fakeTransport) Exec(ctx context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	f.mu.Lock()
	f.execCalls = append(f.execCalls, command)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return base.ExecResult{}, ctx.Err()
	}
	return f.result, f.execErr
}
func (f *fakeTransport) Ping(context.Context) (time.Duration, error)         { return 0, nil }
func (f *fakeTransport) IsAlive() bool                                       { return true }
func (f *fakeTransport) Close() error                                        { return nil }
func (f *fakeTransport) FileSystem(context.Context) (base.FileSystem, error) { return f.files, nil }
func (f *fakeTransport) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.execCalls)
}

type fakeTerminal struct {
	mu       sync.Mutex
	screen   Screen
	writes   [][]byte
	writeErr error
}

func (f *fakeTerminal) Snapshot(context.Context, string) (Screen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.screen, nil
}
func (f *fakeTerminal) Write(_ context.Context, _ string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writes = append(f.writes, append([]byte(nil), data...))
	return nil
}

type fakeDatabase struct {
	result db.QueryResult
}

func (f *fakeDatabase) Tables(context.Context, string, string) ([]string, error) {
	return []string{"users"}, nil
}
func (f *fakeDatabase) Describe(context.Context, string, string, string) (db.TableDescribe, error) {
	return db.TableDescribe{Columns: []db.TableColumn{}, Indexes: []db.TableIndex{}}, nil
}
func (f *fakeDatabase) Query(context.Context, string, string, uint64, time.Duration) (db.QueryResult, error) {
	return f.result, nil
}
func (f *fakeDatabase) RedisScan(context.Context, string, uint64, string, uint64) (db.RedisScanResult, error) {
	return db.RedisScanResult{Cursor: 7, Keys: []string{"a", "b"}}, nil
}

type discardCloser struct{ io.Writer }

func (discardCloser) Close() error { return nil }
